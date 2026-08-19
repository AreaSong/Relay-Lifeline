package governance

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/journal"
)

func TestManagerObserveAndEnforceUsage(t *testing.T) {
	cfg := config.GovernanceConfig{Mode: "enforce", UnknownUsagePolicy: "observe", MaxConcurrent: 1, TokenLimit: 10, CostLimitMicros: 100, Prices: []config.ModelPriceConfig{{Model: "m", InputMicrosPerToken: 2, OutputMicrosPerToken: 4}}}
	m := New(cfg)
	first, _, err := m.Admit(context.Background(), "principal", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Admit(context.Background(), "principal", "m"); !errors.Is(err, ErrConcurrentLimit) {
		t.Fatalf("expected concurrent limit, got %v", err)
	}
	if cost, err := first.Record("attempt-1", Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7, Known: true}); err != nil || cost != 22 {
		t.Fatalf("unexpected settlement cost=%d err=%v", cost, err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, _, err := m.Admit(context.Background(), "principal", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Record("attempt-1", Usage{TotalTokens: 4, Known: true}); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Admit(context.Background(), "principal", "m"); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("expected token limit, got %v", err)
	}
}

func TestAdmissionReservesTokenAndCostBudgetUntilSettlementOrRelease(t *testing.T) {
	cfg := config.GovernanceConfig{Mode: "enforce", UnknownUsagePolicy: "observe", TokenLimit: 10, CostLimitMicros: 100, TokenReservation: 6, CostReservationMicros: 60}
	m := New(cfg)
	first, decision, err := m.Admit(context.Background(), "p", "m")
	if err != nil || decision.ReservedTokens != 6 || decision.ReservedCostMicros != 60 {
		t.Fatalf("首次预留异常: decision=%+v err=%v", decision, err)
	}
	if _, _, err := m.Admit(context.Background(), "p", "m"); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("活动预留未计入 token limit: %v", err)
	}
	snapshot := m.Snapshot()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].ReservedTokens != 6 || snapshot.Entries[0].ReservedCostMicros != 60 {
		t.Fatalf("预留未进入快照: %+v", snapshot)
	}
	if _, err := first.Record("attempt-1", Usage{InputTokens: 2, TotalTokens: 2, Known: true}); err != nil {
		t.Fatal(err)
	}
	if snapshot := m.Snapshot(); snapshot.Entries[0].ReservedTokens != 0 || snapshot.Entries[0].Tokens != 2 {
		t.Fatalf("结算后预留未释放: %+v", snapshot)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Admit(context.Background(), "p", "m"); err != nil {
		t.Fatalf("释放后应可重新预留: %v", err)
	}
}

func TestManagerKeepsActiveReservationsAcrossUsageWindows(t *testing.T) {
	m := New(config.GovernanceConfig{Mode: "enforce", UnknownUsagePolicy: "observe", MaxConcurrent: 1, RequestsPerMinute: 1})
	now := time.Unix(60, 0).UTC()
	m.now = func() time.Time { return now }
	first, _, err := m.Admit(context.Background(), "p", "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, _, err := m.Admit(context.Background(), "p", ""); !errors.Is(err, ErrConcurrentLimit) {
		t.Fatalf("跨窗口的活动预约仍应占用并发额度，got %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Admit(context.Background(), "p", ""); err != nil {
		t.Fatalf("释放后新窗口应允许准入: %v", err)
	}
}

func TestSettlementIsIdempotentByReservationAndSettlementID(t *testing.T) {
	m := New(config.GovernanceConfig{Mode: "enforce", UnknownUsagePolicy: "observe", Prices: []config.ModelPriceConfig{{Model: "m", InputMicrosPerToken: 2}}})
	reservation, _, err := m.AdmitWithID(context.Background(), "reservation-1", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	usage := Usage{InputTokens: 3, TotalTokens: 3, Known: true}
	for range 2 {
		if cost, err := reservation.Record("attempt-1", usage); err != nil || cost != 6 {
			t.Fatalf("重复结算结果异常 cost=%d err=%v", cost, err)
		}
	}
	snapshot := m.Snapshot()
	if snapshot.Entries[0].Tokens != 3 || snapshot.Entries[0].CostMicros != 6 || snapshot.Counters.Settlements != 1 {
		t.Fatalf("重复结算不应重复累计: %+v", snapshot)
	}
}

func TestUnknownUsageDenyOnlyAppliesToCurrentWindow(t *testing.T) {
	m := New(config.GovernanceConfig{Mode: "enforce", UnknownUsagePolicy: "deny"})
	now := time.Unix(60, 0).UTC()
	m.now = func() time.Time { return now }
	reservation, _, err := m.Admit(context.Background(), "p", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.Record("attempt-1", Usage{Known: false}); err != nil {
		t.Fatal(err)
	}
	if err := reservation.Release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Admit(context.Background(), "p", ""); !errors.Is(err, ErrUnknownUsage) {
		t.Fatalf("未知用量后应拒绝当前窗口，got %v", err)
	}
	now = now.Add(time.Minute)
	if _, _, err := m.Admit(context.Background(), "p", ""); err != nil {
		t.Fatalf("下一窗口应恢复准入: %v", err)
	}
}

func TestPersistentManagerReplaysSettledWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-ledger.jsonl")
	ledger := openLedger(t, path, true)
	cfg := config.GovernanceConfig{Mode: "enforce", UnknownUsagePolicy: "observe", TokenLimit: 10}
	m, err := NewPersistent(cfg, ledger)
	if err != nil {
		t.Fatal(err)
	}
	reservation, _, err := m.AdmitWithID(context.Background(), "reservation-1", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.Record("attempt-1", Usage{TotalTokens: 7, Known: true}); err != nil {
		t.Fatal(err)
	}
	if err := reservation.Release(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	replayedLedger := openLedger(t, path, true)
	defer replayedLedger.Close()
	replayed, err := NewPersistent(cfg, replayedLedger)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := replayed.Snapshot()
	if snapshot.Reservations != 0 || len(snapshot.Entries) != 1 || snapshot.Entries[0].Tokens != 7 {
		t.Fatalf("账本 replay 未恢复当前窗口: %+v", snapshot)
	}
}

func TestPersistentManagerReconcilesInterruptedReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-ledger.jsonl")
	ledger := openLedger(t, path, true)
	cfg := config.GovernanceConfig{Mode: "enforce", UnknownUsagePolicy: "observe", MaxConcurrent: 1}
	m, err := NewPersistent(cfg, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.AdmitWithID(context.Background(), "interrupted", "p", "m"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	replayedLedger := openLedger(t, path, true)
	defer replayedLedger.Close()
	replayed, err := NewPersistent(cfg, replayedLedger)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := replayed.Snapshot()
	if snapshot.Reservations != 0 || snapshot.Counters.Reconciled != 1 {
		t.Fatalf("中断预约应对账但不恢复活动并发: %+v", snapshot)
	}
	entries := replayedLedger.Entries()
	if entries[len(entries)-1].Type != eventReconciled {
		t.Fatalf("缺少 reconciliation 事件: %+v", entries)
	}
}

func TestLedgerFailureFailsClosedOnlyInEnforceMode(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		allowed bool
	}{
		{name: "enforce", mode: "enforce", allowed: false},
		{name: "observe", mode: "observe", allowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := openLedger(t, filepath.Join(t.TempDir(), "usage-ledger.jsonl"), true)
			defer ledger.Close()
			m, err := NewPersistent(config.GovernanceConfig{Mode: test.mode, UnknownUsagePolicy: "observe"}, ledger)
			if err != nil {
				t.Fatal(err)
			}
			ledger.SetHooks(journal.Hooks{Write: func(_ *os.File, data []byte) (int, error) { return len(data) - 1, nil }})
			reservation, decision, err := m.Admit(context.Background(), "p", "m")
			if test.allowed {
				if err != nil || reservation == nil || !decision.PersistenceDegraded {
					t.Fatalf("observe 模式应降级放行 reservation=%v decision=%+v err=%v", reservation, decision, err)
				}
			} else if !errors.Is(err, ErrLedgerUnavailable) || decision.Allowed {
				t.Fatalf("enforce 模式应拒绝 err=%v decision=%+v", err, decision)
			}
			if snapshot := m.Snapshot(); snapshot.Ledger.Healthy || snapshot.Counters.PersistenceFailures != 1 {
				t.Fatalf("账本降级状态异常: %+v", snapshot)
			}
		})
	}
}

func TestLedgerSyncFailureRejectsEnforcedAdmission(t *testing.T) {
	ledger := openLedger(t, filepath.Join(t.TempDir(), "usage-ledger.jsonl"), true)
	defer ledger.Close()
	m, err := NewPersistent(config.GovernanceConfig{Mode: "enforce", UnknownUsagePolicy: "observe"}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	ledger.SetHooks(journal.Hooks{Sync: func(*os.File) error { return io.ErrClosedPipe }})
	if _, _, err := m.Admit(context.Background(), "p", "m"); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("sync 失败必须 fail closed: %v", err)
	}
}

func TestScopedBudgetReservesWithoutGlobalLimitAndKeepsDimensionsDistinct(t *testing.T) {
	cfg := config.GovernanceConfig{
		Mode: "enforce", UnknownUsagePolicy: "observe", TokenReservation: 6,
		Budgets: []config.GovernanceBudgetConfig{
			{Scope: "tenant", Key: "same:value", TokenLimit: 10},
			{Scope: "model", Key: "same:value", TokenLimit: 20},
		},
	}
	m := New(cfg)
	first, decision, err := m.AdmitContext(context.Background(), AdmissionContext{Principal: "p", Tenant: "same:value", Model: "same:value"})
	if err != nil || first == nil {
		t.Fatalf("scoped admission failed: decision=%+v err=%v", decision, err)
	}
	if decision.ReservedTokens != 6 {
		t.Fatalf("scoped budget must drive reservation when global limit is zero: %+v", decision)
	}
	if _, _, err := m.AdmitContext(context.Background(), AdmissionContext{Principal: "other", Tenant: "same:value", Model: "same:value"}); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("tenant dimension reservation was not enforced: %v", err)
	}
	if got := len(m.dimensionWindows); got != 2 {
		t.Fatalf("tenant/model dimensions collided: got %d states=%v", got, m.dimensionWindows)
	}
	snapshot := m.Snapshot()
	seen := map[string]bool{}
	for _, entry := range snapshot.Entries {
		seen[entry.Scope+":"+entry.Key] = true
	}
	for _, key := range []string{"principal:p", "tenant:same:value", "model:same:value"} {
		if !seen[key] {
			t.Fatalf("snapshot omitted scoped usage %q: %+v", key, snapshot.Entries)
		}
	}
}

func TestScopedBudgetWildcardAndExactMatch(t *testing.T) {
	cfg := config.GovernanceConfig{Mode: "enforce", TokenReservation: 4, Budgets: []config.GovernanceBudgetConfig{
		{Scope: "tenant", Key: "acme-*", TokenLimit: 8},
		{Scope: "tenant", Key: "acme-prod", TokenLimit: 5},
	}}
	m := New(cfg)
	first, _, err := m.AdmitContext(context.Background(), AdmissionContext{Principal: "p1", Tenant: "acme-prod", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.AdmitContext(context.Background(), AdmissionContext{Principal: "p2", Tenant: "acme-prod", Model: "m"}); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("exact budget should tighten wildcard budget: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.AdmitContext(context.Background(), AdmissionContext{Principal: "p3", Tenant: "acme-test", Model: "m"}); err != nil {
		t.Fatalf("wildcard tenant should match: %v", err)
	}
}

func TestEnforcedTenantBudgetRequiresTenantContext(t *testing.T) {
	m := New(config.GovernanceConfig{
		Mode:    "enforce",
		Budgets: []config.GovernanceBudgetConfig{{Scope: "tenant", Key: "acme-*", RequestsPerMinute: 10}},
	})
	reservation, decision, err := m.AdmitContext(context.Background(), AdmissionContext{Principal: "p", Model: "m"})
	if !errors.Is(err, ErrTenantContext) || reservation != nil || decision.Reason != "tenant_required" || decision.BudgetScope != "tenant" {
		t.Fatalf("missing tenant context bypassed scoped budget: reservation=%v decision=%+v err=%v", reservation, decision, err)
	}
	if _, _, err := m.AdmitContext(context.Background(), AdmissionContext{Principal: "p", Tenant: "acme-prod", Model: "m"}); err != nil {
		t.Fatalf("trusted tenant context should satisfy scoped admission: %v", err)
	}

	observe := New(config.GovernanceConfig{
		Mode:    "observe",
		Budgets: []config.GovernanceBudgetConfig{{Scope: "tenant", Key: "acme-*", RequestsPerMinute: 10}},
	})
	if _, _, err := observe.AdmitContext(context.Background(), AdmissionContext{Principal: "p", Model: "m"}); err != nil {
		t.Fatalf("observe mode should report without blocking missing tenant context: %v", err)
	}
}

func TestRetryAttemptReservationIsPerAttempt(t *testing.T) {
	m := New(config.GovernanceConfig{Mode: "enforce", TokenLimit: 10, TokenReservation: 6})
	reservation, _, err := m.Admit(context.Background(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.BeginAttempt("attempt-2"); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("retry should reserve against the still-active first attempt: %v", err)
	}
	if _, err := reservation.Record("attempt-1", Usage{TotalTokens: 1, Known: true}); err != nil {
		t.Fatal(err)
	}
	decision, err := reservation.BeginAttempt("attempt-2")
	if err != nil || decision.ReservedTokens != 6 {
		t.Fatalf("retry reservation after settlement failed: decision=%+v err=%v", decision, err)
	}
	if snapshot := m.Snapshot(); snapshot.Entries[0].ReservedTokens != 6 {
		t.Fatalf("active retry reservation missing from snapshot: %+v", snapshot)
	}
}

func TestBindingConcreteUpstreamAddsScopedReservation(t *testing.T) {
	m := New(config.GovernanceConfig{Mode: "enforce", TokenReservation: 4, Budgets: []config.GovernanceBudgetConfig{{Scope: "upstream", Key: "target-a", TokenLimit: 5}}})
	reservation, decision, err := m.AdmitContext(context.Background(), AdmissionContext{Principal: "p", Model: "m"})
	if err != nil || decision.ReservedTokens != 0 {
		t.Fatalf("unknown upstream should not apply target budget before binding: decision=%+v err=%v", decision, err)
	}
	bound, err := reservation.BindUpstreamWithDecision("target-a")
	if err != nil || bound.ReservedTokens != 4 {
		t.Fatalf("concrete upstream did not add scoped reservation: decision=%+v err=%v", bound, err)
	}
	if _, err := reservation.BeginAttempt("attempt-2"); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("bound upstream budget was not enforced for retry: %v", err)
	}
}

func TestWindowStateIsNotOverwrittenAndForecastHonorsHorizon(t *testing.T) {
	m := New(config.GovernanceConfig{Mode: "enforce", TokenLimit: 100, TokenReservation: 1, ForecastWindow: config.Duration{Duration: 2 * time.Minute}})
	now := time.Unix(60, 0).UTC()
	m.now = func() time.Time { return now }
	reservation, _, err := m.Admit(context.Background(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.Record("attempt-1", Usage{TotalTokens: 10, Known: true}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	if _, _, err := m.Admit(context.Background(), "p", "m"); err != nil {
		t.Fatal(err)
	}
	if len(m.principalWindows["p"]) < 2 {
		t.Fatalf("window rollover overwrote prior state: %#v", m.principalWindows["p"])
	}
	if snapshot := m.Snapshot(); snapshot.EstimatedExhaustionMinutes != 0 {
		t.Fatalf("forecast beyond configured horizon should be suppressed: %+v", snapshot)
	}
}

func TestTotalTokensOnlyUsesConservativePrice(t *testing.T) {
	m := New(config.GovernanceConfig{Mode: "observe", Prices: []config.ModelPriceConfig{{Model: "m", InputMicrosPerToken: 2, OutputMicrosPerToken: 5}}})
	reservation, _, err := m.Admit(context.Background(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	cost, err := reservation.Record("attempt-1", Usage{TotalTokens: 3, Known: true})
	if err != nil || cost != 15 {
		t.Fatalf("total-only usage should use highest configured unit price: cost=%d err=%v", cost, err)
	}
}

func TestSettlementAndReleaseFailClosedBeforeApplyingState(t *testing.T) {
	ledger := openLedger(t, filepath.Join(t.TempDir(), "usage-ledger.jsonl"), true)
	defer ledger.Close()
	m, err := NewPersistent(config.GovernanceConfig{Mode: "enforce", TokenLimit: 10, TokenReservation: 4}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	reservation, _, err := m.Admit(context.Background(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	ledger.SetHooks(journal.Hooks{Write: func(_ *os.File, data []byte) (int, error) { return len(data) - 1, nil }})
	if _, err := reservation.Record("attempt-1", Usage{TotalTokens: 2, Known: true}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("settlement must fail closed: %v", err)
	}
	if snapshot := m.Snapshot(); snapshot.Entries[0].Tokens != 0 || snapshot.Entries[0].ReservedTokens != 4 {
		t.Fatalf("state changed before durable settlement: %+v", snapshot)
	}
	if err := reservation.Release(); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("release must fail closed: %v", err)
	}
	if snapshot := m.Snapshot(); snapshot.Reservations != 1 {
		t.Fatalf("failed release should retain reservation: %+v", snapshot)
	}
}

func TestCompactRetainsActiveReservationForReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-ledger.jsonl")
	ledger := openLedger(t, path, true)
	cfg := config.GovernanceConfig{Mode: "enforce", TokenLimit: 10, TokenReservation: 4}
	m, err := NewPersistent(cfg, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.AdmitWithID(context.Background(), "active", "p", "m"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	cutoff := time.Now()
	if _, err := m.Compact(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	replayedLedger := openLedger(t, path, true)
	defer replayedLedger.Close()
	replayed, err := NewPersistent(cfg, replayedLedger)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Snapshot().Reservations != 0 || replayed.Snapshot().Counters.Reconciled != 1 {
		t.Fatalf("active reservation should replay then reconcile: %+v", replayed.Snapshot())
	}
}

func openLedger(t *testing.T, path string, syncWrites bool) *journal.Store {
	t.Helper()
	store, err := journal.Open(path, syncWrites)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
