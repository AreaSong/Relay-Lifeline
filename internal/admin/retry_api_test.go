package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/state"
)

func TestBatchRetryReturnsPerRequestOutcomes(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	registry := state.NewRegistry()
	waitingID, retry := registry.Add("POST", "/waiting", func() {})
	requestingID, _ := registry.Add("POST", "/requesting", func() {})
	registry.UpdateMessage(waitingID, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(waitingID, lifecycle.StateWaiting, 1, l10n.Message{}, time.Now().Add(time.Minute))
	registry.UpdateMessage(requestingID, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	handler := New(config.NewStore("", config.Default()), registry, state.NewController())

	body := `{"requestIds":["` + waitingID + `","` + requestingID + `","missing","` + waitingID + `"]}`
	recorder := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/batch/retry", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("批量重试失败: %d %s", recorder.Code, recorder.Body.String())
	}
	var response batchActionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Requested != 3 || response.Accepted != 1 || response.Skipped != 2 || len(response.Results) != 3 {
		t.Fatalf("批量结果统计异常: %+v", response)
	}
	select {
	case <-retry:
	case <-time.After(time.Second):
		t.Fatal("等待请求未收到批量重试信号")
	}
}

func TestBatchRetryPolicySupportsStructuredSchedulesOverwriteAndReset(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	registry := state.NewRegistry()
	waitingID, _ := registry.Add("POST", "/waiting", func() {})
	queuedID, _ := registry.Add("POST", "/queued", func() {})
	registry.UpdateMessage(waitingID, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(waitingID, lifecycle.StateWaiting, 1, l10n.Message{}, time.Now().Add(time.Minute))
	changes := registry.PolicyChanges(waitingID)
	handler := New(config.NewStore("", config.Default()), registry, state.NewController())

	body := `{"requestIds":["` + waitingID + `","` + queuedID + `"],"policy":{"durationMilliseconds":3600000,"schedule":{"mode":"random","minimumIntervalMilliseconds":60000,"maximumIntervalMilliseconds":120000},"honorRetryAfter":true},"overwrite":true}`
	recorder := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/batch/retry-policy", body)
	if recorder.Code != http.StatusOK || !containsAll(recorder.Body.String(), `"accepted":2`, `"skipped":0`) {
		t.Fatalf("批量策略设置失败: %d %s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("等待请求策略更新未触发重排")
	}
	waitingPolicy, waitingOK := registry.RetryPolicy(waitingID)
	queuedPolicy, queuedOK := registry.RetryPolicy(queuedID)
	if !waitingOK || !queuedOK || waitingPolicy.Schedule.Mode != state.RetryScheduleRandom || !waitingPolicy.Active() || queuedPolicy.Active() {
		t.Fatalf("批量策略状态异常: waiting=%+v queued=%+v", waitingPolicy, queuedPolicy)
	}

	noOverwrite := `{"requestIds":["` + waitingID + `","` + queuedID + `"],"policy":{"durationMilliseconds":3600000,"schedule":{"mode":"fixed","intervalMilliseconds":60000},"honorRetryAfter":true},"overwrite":false}`
	recorder = repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/batch/retry-policy", noOverwrite)
	if recorder.Code != http.StatusOK || !containsAll(recorder.Body.String(), `"accepted":0`, `"reason":"policy_exists"`) {
		t.Fatalf("已有策略未被保护: %d %s", recorder.Code, recorder.Body.String())
	}

	reset := `{"requestIds":["` + waitingID + `","` + queuedID + `"],"reset":true}`
	recorder = repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/batch/retry-policy", reset)
	if recorder.Code != http.StatusOK || !containsAll(recorder.Body.String(), `"accepted":2`, `"skipped":0`) {
		t.Fatalf("批量恢复全局策略失败: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := registry.RetryPolicy(waitingID); ok {
		t.Fatal("等待请求仍保留覆盖策略")
	}
}

func TestRetryPolicyAPIRejectsUnsafeImmediateAndViewerWrites(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_VIEWER_KEY", "viewer-key-12345678901234")
	registry := state.NewRegistry()
	id, _ := registry.Add("POST", "/v1/responses", func() {})
	handler := New(config.NewStore("", config.Default()), registry, state.NewController())
	body := `{"requestIds":["` + id + `"],"policy":{"durationMilliseconds":60000,"schedule":{"mode":"immediate"},"honorRetryAfter":true}}`
	invalid := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/batch/retry-policy", body)
	if invalid.Code != http.StatusBadRequest || !containsAll(invalid.Body.String(), "INVALID_RETRY_POLICY") {
		t.Fatalf("无限立即策略未被拒绝: %d %s", invalid.Code, invalid.Body.String())
	}
	viewer := repeatAPIRequest(handler, "viewer-key-12345678901234", http.MethodPost, "/admin/api/requests/batch/retry", `{"requestIds":["`+id+`"]}`)
	if viewer.Code != http.StatusForbidden {
		t.Fatalf("Viewer 不应执行批量写操作: %d %s", viewer.Code, viewer.Body.String())
	}
}

func TestSingleRetryPolicyHonorsOverwriteFlagAndEmptyRetryBody(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	registry := state.NewRegistry()
	id, retry := registry.Add("POST", "/v1/responses", func() {})
	handler := New(config.NewStore("", config.Default()), registry, state.NewController())
	first := `{"durationMilliseconds":60000,"schedule":{"mode":"fixed","intervalMilliseconds":5000},"overwrite":true}`
	if response := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/retry-policy", first); response.Code != http.StatusOK {
		t.Fatalf("首次设置策略失败: %d %s", response.Code, response.Body.String())
	}
	second := `{"durationMilliseconds":60000,"schedule":{"mode":"fixed","intervalMilliseconds":15000},"overwrite":false}`
	if response := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/retry-policy", second); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "RETRY_POLICY_EXISTS") {
		t.Fatalf("未尊重单请求覆盖开关: %d %s", response.Code, response.Body.String())
	}
	retryRequest := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+id+"/retry", "")
	if retryRequest.Code != http.StatusConflict {
		t.Fatalf("空请求体立即重试不应被 JSON 解析拒绝: %d %s", retryRequest.Code, retryRequest.Body.String())
	}
	select {
	case <-retry:
		t.Fatal("排队请求不应被立即重试")
	default:
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
