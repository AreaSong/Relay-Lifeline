package admin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	trafficpolicy "github.com/areasong/relay-lifeline/internal/policy"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/state"
)

func TestPolicyReleaseLifecyclePermissionConflictAndRollback(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_VIEWER_KEY", "viewer-key-12345678901234")
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Upstream.BaseURL = "https://relay.example.test"
	cfg.Egress.AllowedHosts = []string{"relay.example.test"}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(path, cfg)
	handler := NewWithServices(store, state.NewRegistry(), state.NewController(), risk.New(), diagnostics.New(store, "test", time.Now()), nil)
	runtime := trafficpolicy.New(cfg.TrafficPolicy)
	handler.SetPolicyRuntime(runtime.Status, func(input trafficpolicy.Input) trafficpolicy.Decision { return runtime.Evaluate(input, true) })

	draft := cfg.TrafficPolicy
	draft.Enabled, draft.Mode = true, "enforce"
	draft.Rules = []config.TrafficPolicyRule{{ID: "deny-test", Enabled: true, Priority: 1, Method: "POST", PathPrefix: "/v1/", Action: "deny"}}
	draftPayload, _ := json.Marshal(map[string]any{"policy": draft})
	viewer := repeatAPIRequest(handler, "viewer-key-12345678901234", http.MethodPut, "/admin/api/policies/draft", string(draftPayload))
	if viewer.Code != http.StatusForbidden {
		t.Fatalf("Viewer 不应保存策略草稿: %d", viewer.Code)
	}
	saved := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPut, "/admin/api/policies/draft", string(draftPayload))
	if saved.Code != http.StatusOK {
		t.Fatalf("保存策略草稿失败: %d %s", saved.Code, saved.Body.String())
	}
	var savedBody struct {
		DraftRevision string `json:"draftRevision"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &savedBody); err != nil || savedBody.DraftRevision == "" {
		t.Fatalf("草稿响应异常: %v %s", err, saved.Body.String())
	}

	conflict := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/policies/publish", `{"configRevision":"stale","stage":"canary","canaryPercent":10}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("过期配置版本未冲突: %d", conflict.Code)
	}
	publishPayload, _ := json.Marshal(map[string]any{"configRevision": store.State().DesiredRevision, "draftRevision": savedBody.DraftRevision, "stage": "canary", "canaryPercent": 10})
	published := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/policies/publish", string(publishPayload))
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"stage":"canary"`) {
		t.Fatalf("灰度发布失败: %d %s", published.Code, published.Body.String())
	}
	var publishedBody struct {
		Release trafficpolicy.ReleaseRecord `json:"release"`
	}
	if err := json.Unmarshal(published.Body.Bytes(), &publishedBody); err != nil {
		t.Fatal(err)
	}
	if current := store.Get().TrafficPolicy; current.ReleaseStage != "canary" || current.CanaryPercent != 10 {
		t.Fatalf("灰度发布未应用: %+v", current)
	}

	rollbackPayload, _ := json.Marshal(map[string]any{"configRevision": store.State().DesiredRevision, "policyRevision": publishedBody.Release.Revision})
	rolledBack := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/policies/rollback", string(rollbackPayload))
	if rolledBack.Code != http.StatusOK || store.Get().TrafficPolicy.ReleaseStage != "full" {
		t.Fatalf("策略回滚失败: %d %s", rolledBack.Code, rolledBack.Body.String())
	}
	releases := repeatAPIRequest(handler, "viewer-key-12345678901234", http.MethodGet, "/admin/api/policies/releases", "")
	if releases.Code != http.StatusOK || !strings.Contains(releases.Body.String(), `"history"`) {
		t.Fatalf("策略版本读取失败: %d %s", releases.Code, releases.Body.String())
	}
}

func TestPolicyDraftSimulationNeverExecutes(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	cfg := config.Default()
	store := config.NewStore("", cfg)
	handler := New(store, state.NewRegistry(), state.NewController())
	draft := cfg.TrafficPolicy
	draft.Enabled, draft.Mode = true, "enforce"
	draft.Rules = []config.TrafficPolicyRule{{ID: "deny", Enabled: true, Action: "deny"}}
	payload, _ := json.Marshal(map[string]any{"policy": draft})
	if saved := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPut, "/admin/api/policies/draft", string(payload)); saved.Code != http.StatusOK {
		t.Fatalf("保存草稿失败: %s", saved.Body.String())
	}
	simulated := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/policies/simulate?source=draft", `{"method":"POST","path":"/v1/responses","sloHealthy":true,"errorBudgetRemaining":1}`)
	if simulated.Code != http.StatusOK || !strings.Contains(simulated.Body.String(), `"dryRun":true`) || !strings.Contains(simulated.Body.String(), `"enforced":false`) {
		t.Fatalf("草稿模拟不安全: %d %s", simulated.Code, simulated.Body.String())
	}
}

func TestPolicyCollectionEndpointsReturnArraysWhenEmpty(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_VIEWER_KEY", "viewer-key-12345678901234")
	handler := New(config.NewStore("", config.Default()), state.NewRegistry(), state.NewController())

	status := repeatAPIRequest(handler, "viewer-key-12345678901234", http.MethodGet, "/admin/api/policies/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("读取策略状态失败: %d %s", status.Code, status.Body.String())
	}
	var statusBody map[string]json.RawMessage
	if err := json.Unmarshal(status.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	if string(statusBody["recent"]) != "[]" {
		t.Fatalf("策略状态 recent 必须为 []，实际为 %s", statusBody["recent"])
	}

	history := repeatAPIRequest(handler, "viewer-key-12345678901234", http.MethodGet, "/admin/api/policies/releases", "")
	if history.Code != http.StatusOK {
		t.Fatalf("读取策略发布历史失败: %d %s", history.Code, history.Body.String())
	}
	var historyBody map[string]json.RawMessage
	if err := json.Unmarshal(history.Body.Bytes(), &historyBody); err != nil {
		t.Fatal(err)
	}
	if string(historyBody["history"]) != "[]" {
		t.Fatalf("策略发布 history 必须为 []，实际为 %s", historyBody["history"])
	}
}
