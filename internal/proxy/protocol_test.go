package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
)

func buffered(t *testing.T, content string) *ReplayBuffer {
	t.Helper()
	buffer := NewReplayBuffer(8, t.TempDir())
	if _, err := io.WriteString(buffer, content); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { buffer.Close() })
	return buffer
}

func TestValidateEventStreamCompletionMatrix(t *testing.T) {
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	tests := []struct {
		name    string
		content string
		success bool
	}{
		{name: "Responses 完成", content: "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.completed\"}\n\n", success: true},
		{name: "Chat Completions 完成", content: "data: {\"choices\":[]}\n\ndata: [DONE]\n\n", success: true},
		{name: "Responses 不接受 DONE", content: "data: [DONE]\n\n"},
		{name: "Chat Completions 不接受 Responses 标记", content: "data: {\"type\":\"response.completed\"}\n\n"},
		{name: "Responses 失败", content: "data: {\"type\":\"response.failed\"}\n\n"},
		{name: "Responses 不完整", content: "data: {\"type\":\"response.incomplete\"}\n\n"},
		{name: "流被截断", content: "data: {\"type\":\"response.output_text.delta\"}\n\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := protocolGenericJSON
			if strings.HasPrefix(test.name, "Responses") {
				profile = protocolResponses
			} else if strings.HasPrefix(test.name, "Chat") {
				profile = protocolChatCompletions
			}
			result := validateResponse(response, buffered(t, test.content), profile, true)
			if result.Success != test.success {
				t.Fatalf("校验结果 = %+v，期望成功 = %v", result, test.success)
			}
		})
	}
}

func TestValidateJSONError(t *testing.T) {
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}}
	if result := validateResponse(response, buffered(t, `{"error":{"message":"bad"}}`), protocolGenericJSON, false); result.Success {
		t.Fatal("错误 JSON 不应成功")
	}
	if result := validateResponse(response, buffered(t, `{"id":"ok"}`), protocolGenericJSON, false); !result.Success {
		t.Fatalf("正常 JSON 应成功: %+v", result)
	}
	if result := validateResponse(response, buffered(t, `{"status":"incomplete"}`), protocolGenericJSON, false); result.Success {
		t.Fatal("不完整 JSON 不应成功")
	}
}

func TestValidateResponseProfiles(t *testing.T) {
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}}
	tests := []struct {
		name    string
		profile protocolProfile
		body    string
		success bool
	}{
		{name: "Responses 完成", profile: protocolResponses, body: `{"id":"resp","status":"completed"}`, success: true},
		{name: "Responses 仍在处理", profile: protocolResponses, body: `{"id":"resp","status":"in_progress"}`},
		{name: "Responses 缺少状态", profile: protocolResponses, body: `{"id":"resp"}`},
		{name: "Chat Completions 完成", profile: protocolChatCompletions, body: `{"id":"chat","choices":[]}`, success: true},
		{name: "Chat Completions 缺少 choices", profile: protocolChatCompletions, body: `{"id":"chat"}`},
		{name: "普通 JSON", profile: protocolGenericJSON, body: `{"data":[]}`, success: true},
		{name: "普通 JSON 保留业务状态", profile: protocolGenericJSON, body: `{"id":"job","status":"in_progress"}`, success: true},
		{name: "空对象", profile: protocolGenericJSON, body: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validateResponse(response, buffered(t, test.body), test.profile, false)
			if result.Success != test.success {
				t.Fatalf("校验结果 = %+v，期望成功 = %v", result, test.success)
			}
		})
	}
}

func TestValidateResponseExtractsAuthoritativeTokenUsage(t *testing.T) {
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}}
	result := validateResponse(response, buffered(t, `{"id":"resp","status":"completed","usage":{"input_tokens":12,"output_tokens":8,"total_tokens":20}}`), protocolResponses, false)
	if !result.Success || result.Usage == nil || result.Usage.TotalTokens != 20 || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 8 {
		t.Fatalf("Responses usage 解析异常: %+v", result)
	}
	result = validateResponse(response, buffered(t, `{"id":"chat","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`), protocolChatCompletions, false)
	if !result.Success || result.Usage == nil || result.Usage.TotalTokens != 7 {
		t.Fatalf("Chat usage 解析异常: %+v", result)
	}
	stream := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	result = validateResponse(stream, buffered(t, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"total_tokens\":11}}}\n\n"), protocolResponses, true)
	if !result.Success || result.Usage == nil || result.Usage.TotalTokens != 11 {
		t.Fatalf("SSE usage 解析异常: %+v", result)
	}
	result = validateResponse(response, buffered(t, `{"id":"resp","status":"completed","usage":{"input_tokens":12}}`), protocolResponses, false)
	if !result.Success || result.Usage != nil {
		t.Fatalf("缺失 total_tokens 不应伪造 usage: %+v", result)
	}
}

func TestValidateRejectsUnsupportedContentTypePermanently(t *testing.T) {
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"audio/mpeg"}}}
	result := validateResponse(response, buffered(t, "binary"), protocolGenericJSON, false)
	if result.Success || !result.Permanent || result.Message.ID != "proxy.content_type_unsupported" {
		t.Fatalf("不支持的响应类型未被永久拒绝: %+v", result)
	}
}

func TestResponseProfileMatchesKnownEndpoints(t *testing.T) {
	if responseProfile("/v1/responses") != protocolResponses || responseProfile("/v1/chat/completions/") != protocolChatCompletions || responseProfile("/v1/embeddings") != protocolGenericJSON {
		t.Fatal("请求路径协议识别异常")
	}
}

func TestRequestWantsStream(t *testing.T) {
	if !requestWantsStream([]byte(`{"stream":true}`), http.Header{}) {
		t.Fatal("未识别 stream")
	}
	if requestWantsStream([]byte(`{"stream":false}`), http.Header{}) {
		t.Fatal("误识别 stream")
	}
	if !strings.Contains("text/event-stream", "event-stream") {
		t.Fatal("sanity")
	}
}

func TestRetryPolicyCoversAllErrors(t *testing.T) {
	cfg := config.Default()
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		result := attemptResult{response: &http.Response{StatusCode: status}, validation: Validation{Message: l10n.M("proxy.http_error", map[string]any{"Status": status})}}
		if !shouldRetry(cfg, result) {
			t.Fatalf("all-errors 应重试 %d", status)
		}
	}
	result := attemptResult{response: &http.Response{StatusCode: http.StatusBadRequest}, validation: Validation{Message: l10n.M("proxy.http_error", map[string]any{"Status": 400})}}
	cfg.Retry.Mode = "transient-errors"
	if shouldRetry(cfg, result) {
		t.Fatal("transient-errors 不应重试 400")
	}
	result.response.StatusCode = http.StatusTooManyRequests
	if !shouldRetry(cfg, result) {
		t.Fatal("transient-errors 应重试 429")
	}
	result = attemptResult{err: context.DeadlineExceeded, validation: Validation{Message: l10n.M("proxy.connection_timeout")}}
	if !shouldRetry(cfg, result) {
		t.Fatal("transient-errors 应重试传输超时")
	}
	cfg.Retry.Mode = "all-errors"
	result.validation.Permanent = true
	if shouldRetry(cfg, result) {
		t.Fatal("本地资源或不支持的协议错误不应重试")
	}
}

func TestRetryAfterAndLimiter(t *testing.T) {
	response := &http.Response{Header: http.Header{"Retry-After": []string{"3"}}}
	if delay := retryAfter(response); delay != 3*time.Second {
		t.Fatalf("Retry-After 解析异常: %s", delay)
	}
	limiter := &Limiter{}
	limits := func() (int, int) { return 1, 0 }
	if err := limiter.Acquire(context.Background(), limits); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Acquire(context.Background(), limits); err != ErrQueueFull {
		t.Fatalf("队列满时返回异常: %v", err)
	}
	limiter.Release()
}
