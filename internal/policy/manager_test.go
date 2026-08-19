package policy

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/journal"
)

func TestStatusCollectionsEncodeAsEmptyArrays(t *testing.T) {
	manager := New(config.Default().TrafficPolicy)
	status := manager.Status(50)
	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["recent"]) != "[]" {
		t.Fatalf("空决策集合必须编码为 []，实际为 %s", decoded["recent"])
	}

	releases := NewReleaseManager().Status(config.Default().TrafficPolicy)
	payload, err = json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	decoded = map[string]json.RawMessage{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["history"]) != "[]" {
		t.Fatalf("空发布历史必须编码为 []，实际为 %s", decoded["history"])
	}
}

func TestEvaluateRuleDryRunAndEnforcement(t *testing.T) {
	cfg := config.Default().TrafficPolicy
	cfg.Enabled, cfg.Mode = true, "enforce"
	cfg.Rules = []config.TrafficPolicyRule{{ID: "deny-model", Enabled: true, Priority: 1, Model: "blocked", Action: "deny"}}
	manager := New(cfg)
	dry := manager.Evaluate(Input{Method: "POST", Path: "/v1/responses", Model: "blocked"}, true)
	if !dry.Denied || dry.Enforced || dry.MatchedRuleID != "deny-model" {
		t.Fatalf("dry-run decision mismatch: %+v", dry)
	}
	live := manager.Evaluate(Input{Method: "POST", Path: "/v1/responses", Model: "blocked"}, false)
	if !live.Denied || !live.Enforced {
		t.Fatalf("enforced decision mismatch: %+v", live)
	}
}

func TestCanarySelectionIsStableAndDraftNeverEnforces(t *testing.T) {
	cfg := config.Default().TrafficPolicy
	cfg.Enabled, cfg.Mode, cfg.ReleaseStage, cfg.CanaryPercent = true, "enforce", "canary", 25
	cfg.Rules = []config.TrafficPolicyRule{{ID: "deny", Enabled: true, Action: "deny"}}
	manager := New(cfg)
	first := manager.Evaluate(Input{RequestID: "stable-request"}, false)
	second := manager.Evaluate(Input{RequestID: "stable-request"}, false)
	if first.CanarySelected != second.CanarySelected || first.Enforced != second.Enforced {
		t.Fatalf("灰度选择必须稳定: first=%+v second=%+v", first, second)
	}
	cfg.ReleaseStage = "draft"
	manager.Apply(cfg)
	if decision := manager.Evaluate(Input{RequestID: "stable-request"}, false); decision.Enforced {
		t.Fatalf("草稿策略不得执行: %+v", decision)
	}
}

func TestNonEnforcingStagesNeverExposeRuntimeTarget(t *testing.T) {
	for _, stage := range []string{"draft", "shadow", "canary"} {
		t.Run(stage, func(t *testing.T) {
			cfg := config.Default().TrafficPolicy
			cfg.Enabled, cfg.Mode, cfg.ReleaseStage = true, "enforce", stage
			cfg.CanaryPercent = 1
			cfg.Rules = []config.TrafficPolicyRule{{ID: "route", Enabled: true, Action: "route", TargetID: "primary"}}
			requestID := "outside-canary"
			manager := New(cfg)
			decision := manager.Evaluate(Input{RequestID: requestID, Method: "POST", Path: "/v1/responses"}, false)
			if stage == "canary" && decision.CanarySelected {
				for index := 0; index < 100 && decision.CanarySelected; index++ {
					requestID = "outside-canary-" + string(rune('a'+index))
					decision = manager.Evaluate(Input{RequestID: requestID, Method: "POST", Path: "/v1/responses"}, false)
				}
			}
			if stage == "canary" && decision.CanarySelected {
				t.Skip("could not find a request outside a 1% canary sample")
			}
			if decision.Enforced || decision.TargetID != "" {
				t.Fatalf("stage %s exposed a target without enforcement: %+v", stage, decision)
			}
		})
	}
}

func TestDryRunDoesNotMutateAdaptiveStopState(t *testing.T) {
	cfg := config.Default().TrafficPolicy
	cfg.Enabled, cfg.Mode, cfg.Adaptive.Enabled = true, "enforce", true
	manager := New(cfg)
	input := Input{SLOHealthy: false, ErrorBudgetRemaining: 0, Targets: []TargetSignal{{ID: "primary", CircuitState: "closed", Observations: 20, LatencyMs: 10}}}
	decision := manager.Evaluate(input, true)
	if !decision.DryRun || manager.Status(10).AdaptiveStopped {
		t.Fatalf("dry-run changed adaptive stop state: decision=%+v status=%+v", decision, manager.Status(10))
	}
}

func TestAdaptiveScoringCooldownAndAutomaticStop(t *testing.T) {
	cfg := config.Default().TrafficPolicy
	cfg.Enabled, cfg.Mode, cfg.Adaptive.Enabled = true, "enforce", true
	cfg.Adaptive.MinimumObservations = 10
	cfg.Adaptive.SwitchCooldown.Duration = time.Hour
	manager := New(cfg)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	input := Input{SLOHealthy: true, ErrorBudgetRemaining: 1, Targets: []TargetSignal{
		{ID: "reliable", CircuitState: "closed", Observations: 20, LatencyMs: 150, ErrorRate: 0.01, CostMicrosPer1K: 100, CapabilityScore: 1},
		{ID: "fast-risky", CircuitState: "closed", Observations: 20, LatencyMs: 20, ErrorRate: 0.9, CostMicrosPer1K: 100, CapabilityScore: 1},
	}}
	decision := manager.Evaluate(input, false)
	if decision.TargetID != "reliable" || decision.AdaptiveScore <= 0 {
		t.Fatalf("综合评分未选择可靠目标: %+v", decision)
	}
	input.Targets[1].ErrorRate = 0
	input.Targets[0].ErrorRate = 0.8
	if held := manager.Evaluate(input, false); held.TargetID != "reliable" {
		t.Fatalf("切换冷却期未抑制抖动: %+v", held)
	}
	input.ErrorBudgetBurnRate = cfg.Adaptive.AutoStopBurnRate + 1
	stopped := manager.Evaluate(input, false)
	if stopped.Adaptive || !manager.Status(10).AdaptiveStopped {
		t.Fatalf("burn rate 未触发自动停止: %+v", stopped)
	}
}

func TestReleaseManagerPersistsDraftPublishAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.jsonl")
	ledger, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().TrafficPolicy
	cfg.Enabled = true
	cfg.Rules = []config.TrafficPolicyRule{{ID: "route", Enabled: true, Action: "route", TargetID: "primary"}}
	manager, err := NewPersistentReleaseManager(ledger)
	if err != nil {
		t.Fatal(err)
	}
	draftRevision, err := manager.SaveDraft(cfg, "")
	if err != nil || draftRevision == "" {
		t.Fatalf("保存草稿失败: %s %v", draftRevision, err)
	}
	policy, release, err := manager.PreparePublish(nil, "canary", 10, "operator")
	if err != nil || policy.ReleaseStage != "canary" {
		t.Fatalf("准备发布失败: %+v %v", policy, err)
	}
	if err := manager.Commit(release, false); err != nil {
		t.Fatal(err)
	}
	rollbackPolicy, rollback, err := manager.PrepareRollback(release, "operator")
	if err != nil || rollbackPolicy.ReleaseStage != "full" {
		t.Fatalf("准备回滚失败: %+v %v", rollbackPolicy, err)
	}
	if err := manager.Commit(rollback, true); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, err := NewPersistentReleaseManager(reopened)
	if err != nil {
		t.Fatal(err)
	}
	status := replayed.Status(rollbackPolicy)
	if status.Draft != nil || len(status.History) != 2 || status.History[1].Stage != "full" {
		t.Fatalf("发布历史重放异常: %+v", status)
	}
}

func TestReleaseManagerReconcilesPreparedIntentAgainstActivePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy-reconcile.jsonl")
	ledger, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().TrafficPolicy
	cfg.Enabled = true
	cfg.Rules = []config.TrafficPolicyRule{{ID: "route", Enabled: true, Action: "route", TargetID: "primary"}}
	manager, err := NewPersistentReleaseManager(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveDraft(cfg, ""); err != nil {
		t.Fatal(err)
	}
	policy, record, err := manager.PreparePublish(nil, "full", 0, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PrepareCommit(record, false); err != nil {
		t.Fatal(err)
	}
	if status := manager.Status(policy); status.Pending != 1 {
		t.Fatalf("prepared release not tracked: %+v", status)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, err := NewPersistentReleaseManager(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayed.Reconcile(policy); err != nil {
		t.Fatal(err)
	}
	status := replayed.Status(policy)
	if status.Pending != 0 || len(status.History) != 1 || status.History[0].Revision != record.Revision {
		t.Fatalf("prepared release was not finalized during reconciliation: %+v", status)
	}

	// A prepared intent whose revision was never applied is explicitly aborted.
	path2 := filepath.Join(t.TempDir(), "policy-abort.jsonl")
	ledger2, err := journal.Open(path2, true)
	if err != nil {
		t.Fatal(err)
	}
	abortManager, err := NewPersistentReleaseManager(ledger2)
	if err != nil {
		t.Fatal(err)
	}
	if err := abortManager.PrepareCommit(record, false); err != nil {
		t.Fatal(err)
	}
	if err := abortManager.Reconcile(config.Default().TrafficPolicy); err != nil {
		t.Fatal(err)
	}
	if status := abortManager.Status(config.Default().TrafficPolicy); status.Pending != 0 || len(status.History) != 0 {
		t.Fatalf("unapplied prepared release should abort: %+v", status)
	}
	if err := ledger2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdaptiveRoutingRequiresHealthySLOAndBudget(t *testing.T) {
	cfg := config.Default().TrafficPolicy
	cfg.Enabled, cfg.Mode, cfg.Adaptive.Enabled = true, "enforce", true
	manager := New(cfg)
	input := Input{SLOHealthy: true, ErrorBudgetRemaining: 0.8, Targets: []TargetSignal{{ID: "slow", CircuitState: "closed", Observations: 12, LatencyMs: 200}, {ID: "fast", CircuitState: "closed", Observations: 15, LatencyMs: 50}}}
	if decision := manager.Evaluate(input, false); decision.TargetID != "fast" || !decision.Adaptive {
		t.Fatalf("adaptive target mismatch: %+v", decision)
	}
	input.ErrorBudgetRemaining = 0.1
	if decision := manager.Evaluate(input, false); decision.Adaptive {
		t.Fatalf("adaptive routing ignored error budget: %+v", decision)
	}
}

func TestShadowLeaseEnforcesConcurrencyAndHourlyBudget(t *testing.T) {
	cfg := config.Default().TrafficPolicy
	cfg.Enabled = true
	cfg.Shadow.Enabled, cfg.Shadow.TargetID, cfg.Shadow.SamplePercent = true, "primary", 100
	cfg.Shadow.MaxConcurrent, cfg.Shadow.RequestBudgetPerHour = 1, 1
	cfg.Shadow.CostReservationMicros, cfg.Shadow.CostBudgetMicrosPerHour = 10, 10
	manager := New(cfg)
	decision := manager.Evaluate(Input{RequestID: "r", IdempotencyKey: "k", SLOHealthy: true, BodyBytes: 1}, false)
	first, ok := manager.AcquireShadow(decision)
	if !ok {
		t.Fatal("first shadow lease denied")
	}
	if _, ok := manager.AcquireShadow(decision); ok {
		t.Fatal("concurrent shadow lease exceeded limit")
	}
	first.Complete(false)
	if _, ok := manager.AcquireShadow(decision); ok {
		t.Fatal("hourly shadow budget exceeded")
	}
	status := manager.Status(10)
	if status.ShadowActive != 0 || status.ShadowSent != 1 || status.ShadowFailed != 1 || status.ShadowSkipped != 2 {
		t.Fatalf("shadow accounting mismatch: %+v", status)
	}
}
