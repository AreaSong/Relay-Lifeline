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
		{name: "Responses 失败", content: "data: {\"type\":\"response.failed\"}\n\n"},
		{name: "Responses 不完整", content: "data: {\"type\":\"response.incomplete\"}\n\n"},
		{name: "流被截断", content: "data: {\"type\":\"response.output_text.delta\"}\n\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validateResponse(response, buffered(t, test.content), true)
			if result.Success != test.success {
				t.Fatalf("校验结果 = %+v，期望成功 = %v", result, test.success)
			}
		})
	}
}

func TestValidateJSONError(t *testing.T) {
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}}
	if result := validateResponse(response, buffered(t, `{"error":{"message":"bad"}}`), false); result.Success {
		t.Fatal("错误 JSON 不应成功")
	}
	if result := validateResponse(response, buffered(t, `{"id":"ok"}`), false); !result.Success {
		t.Fatalf("正常 JSON 应成功: %+v", result)
	}
	if result := validateResponse(response, buffered(t, `{"status":"incomplete"}`), false); result.Success {
		t.Fatal("不完整 JSON 不应成功")
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
