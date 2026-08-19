package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

func uncertainFixture(t *testing.T) (*Handler, *state.Registry, string, context.Context) {
	t.Helper()
	registry := state.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	id, _ := registry.Add("POST", "/v1/responses", cancel)
	registry.UpdateMessage(id, lifecycle.StateForwarding, 2, l10n.Message{}, time.Time{})
	registry.RecordEvent(id, timeline.Event{Type: "uncertain", Attempt: 2, TargetID: "primary", TargetDomain: "primary-domain", WroteRequest: true, IdempotencyKeyHash: "0123456789abcdef", RequestBytes: 128, LatencyMilliseconds: 42, AttemptPhase: string(lifecycle.PhaseResponseHeaders)})
	registry.UpdateMessage(id, lifecycle.StateUncertain, 2, l10n.Message{}, time.Time{})
	handler := New(config.NewStore("", config.Default()), registry, state.NewController())
	return handler, registry, id, ctx
}

func TestUncertainResolutionRequiresBoundTwoPhaseConfirmation(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_VIEWER_KEY", "viewer-key-12345678901234")
	handler, registry, id, ctx := uncertainFixture(t)

	viewer := repeatAPIRequest(handler, "viewer-key-12345678901234", http.MethodPost, "/admin/api/requests/"+id+"/uncertain/preview", `{"action":"confirm_success"}`)
	if viewer.Code != http.StatusForbidden {
		t.Fatalf("viewer 不应发起不确定处置: %d %s", viewer.Code, viewer.Body.String())
	}

	preview := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/uncertain/preview", `{"action":"confirm_success"}`)
	if preview.Code != http.StatusOK || strings.Contains(preview.Body.String(), "raw-secret") || !strings.Contains(preview.Body.String(), "0123456789abcdef") {
		t.Fatalf("处置预览未返回安全证据: %d %s", preview.Code, preview.Body.String())
	}
	var previewBody struct {
		Token string `json:"confirmationToken"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewBody); err != nil || previewBody.Token == "" {
		t.Fatalf("确认 token 缺失: %v %s", err, preview.Body.String())
	}

	wrongAction := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/uncertain/resolve", `{"action":"abandon","confirmationToken":"`+previewBody.Token+`","reason":"wrong action"}`)
	if wrongAction.Code != http.StatusConflict {
		t.Fatalf("跨 action token 未被拒绝: %d %s", wrongAction.Code, wrongAction.Body.String())
	}
	secondPreview := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/uncertain/preview", `{"action":"confirm_success"}`)
	if secondPreview.Code != http.StatusOK {
		t.Fatalf("重发预览失败: %d %s", secondPreview.Code, secondPreview.Body.String())
	}
	if err := json.Unmarshal(secondPreview.Body.Bytes(), &previewBody); err != nil {
		t.Fatal(err)
	}
	resolved := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/uncertain/resolve", `{"action":"confirm_success","confirmationToken":"`+previewBody.Token+`","reason":"operator verified provider receipt"}`)
	if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), `"accepted":true`) {
		t.Fatalf("确认成功失败: %d %s", resolved.Code, resolved.Body.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("确认成功未唤醒原请求")
	}
	if info, ok := registry.RequestInfo(id); !ok || info.UncertainResolution != state.UncertainConfirmSuccess {
		t.Fatalf("不确定决议未写入请求状态: %+v ok=%v", info, ok)
	}
	usedAgain := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/uncertain/resolve", `{"action":"confirm_success","confirmationToken":"`+previewBody.Token+`","reason":"replay"}`)
	if usedAgain.Code != http.StatusConflict {
		t.Fatalf("确认 token 可重放: %d %s", usedAgain.Code, usedAgain.Body.String())
	}
}

func TestUncertainStateBlocksCancelAndRepeatAtAPI(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	handler, registry, id, _ := uncertainFixture(t)
	manager, err := repeat.New(nil, func(context.Context, repeat.Template, string, string) repeat.Execution {
		return repeat.Execution{Success: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.RegisterSource(id, repeat.Template{Method: "POST", Path: "/v1/responses"})
	handler.SetRepeatManager(manager)

	cancel := repeatAPIRequest(handler, "123456789012345678901234", http.MethodDelete, "/admin/api/requests/"+id, "")
	if cancel.Code != http.StatusPreconditionRequired {
		t.Fatalf("不确定请求可被普通取消: %d %s", cancel.Code, cancel.Body.String())
	}
	repeatResponse := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/repeat", `{"interval":"5s","duration":"1m","idempotency":"preserve"}`)
	if repeatResponse.Code != http.StatusPreconditionRequired || len(manager.List()) != 0 {
		t.Fatalf("不确定请求可创建重复任务: %d %s tasks=%+v", repeatResponse.Code, repeatResponse.Body.String(), manager.List())
	}
	if info, ok := registry.RequestInfo(id); !ok || info.State != lifecycle.StateUncertain {
		t.Fatalf("取消/重复操作改变了不确定状态: %+v ok=%v", info, ok)
	}
}

func TestUncertainConfirmationExpires(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	handler, _, id, _ := uncertainFixture(t)
	preview := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/uncertain/preview", `{"action":"abandon"}`)
	var body struct {
		Token string `json:"confirmationToken"`
	}
	if preview.Code != http.StatusOK || json.Unmarshal(preview.Body.Bytes(), &body) != nil {
		t.Fatalf("预览失败: %d %s", preview.Code, preview.Body.String())
	}
	handler.uncertainConfirm.mu.Lock()
	handler.uncertainConfirm.now = func() time.Time { return time.Now().Add(uncertainConfirmationTTL + time.Second) }
	handler.uncertainConfirm.mu.Unlock()
	expired := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/uncertain/resolve", `{"action":"abandon","confirmationToken":"`+body.Token+`","reason":"expired"}`)
	if expired.Code != http.StatusConflict || !strings.Contains(expired.Body.String(), "UNCERTAIN_CONFIRMATION_EXPIRED") {
		t.Fatalf("过期 token 未被拒绝: %d %s", expired.Code, expired.Body.String())
	}
}
