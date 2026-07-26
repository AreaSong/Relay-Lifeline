package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/areasong/relay-lifeline/internal/config"
)

func TestSafeErrorDetailExtractsOnlyStructuredFieldsAndRedactsSecrets(t *testing.T) {
	cfg := config.Default().Observability
	response := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header)}
	response.Header.Set("Content-Type", "application/json")
	response.Header.Set("Retry-After", "60")
	response.Header.Set("OpenAI-Request-ID", "req-safe")
	body := `{"prompt":"must-never-be-copied","error":{"message":"Bearer top-secret https://user:pass@example.com/fail?api_key=hidden eyJabcdefghijklmno.abcdefghijklmnop.abcdefghijklmnop ghp_abcdefghijklmnopqrstuvwxyz","type":"provider_error","code":"sk-secretvalue123"}}`
	result := attemptResult{response: response, buffer: buffered(t, body)}

	detail := extractSafeErrorDetail(cfg, result, false)
	if detail == nil || !detail.Parsed || detail.Type != "provider_error" || detail.RetryAfter != "60" || detail.UpstreamRequestID != "req-safe" {
		t.Fatalf("安全错误详情异常: %+v", detail)
	}
	encoded := detail.Message + detail.Type + detail.Code + detail.UpstreamRequestID + detail.RetryAfter
	for _, secret := range []string{"top-secret", "user:pass", "api_key=hidden", "sk-secretvalue123", "eyJabcdefghijklmno", "ghp_abcdefghijklmnopqrstuvwxyz", "must-never-be-copied"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("错误详情泄露 %q: %+v", secret, detail)
		}
	}
}

func TestSafeErrorDetailSupportsSSEFailureEnvelope(t *testing.T) {
	cfg := config.Default().Observability
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	body := `data: {"type":"response.failed","response":{"error":{"message":"capacity unavailable","type":"provider_error","code":"capacity"}}}` + "\n\n"
	detail := extractSafeErrorDetail(cfg, attemptResult{response: response, buffer: buffered(t, body)}, true)
	if detail == nil || !detail.Parsed || detail.Message != "capacity unavailable" || detail.Type != "provider_error" || detail.Code != "capacity" {
		t.Fatalf("SSE 错误详情异常: %+v", detail)
	}
}

func TestSafeErrorDetailDoesNotStoreRawUnparseableBody(t *testing.T) {
	cfg := config.Default().Observability
	response := &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"text/plain"}}}
	body := "private raw response"
	detail := extractSafeErrorDetail(cfg, attemptResult{response: response, buffer: buffered(t, body)}, false)
	if detail == nil || detail.Parsed || detail.ResponseBytes != int64(len(body)) || detail.Message != "" {
		t.Fatalf("不可解析响应不应保留正文: %+v", detail)
	}
}

func TestSafeErrorDetailSerializesZeroByteResponse(t *testing.T) {
	detail := extractSafeErrorDetail(
		config.Default().Observability,
		attemptResult{
			response: &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header)},
			buffer:   buffered(t, ""),
		},
		false,
	)
	if detail == nil || detail.ResponseBytes != 0 {
		t.Fatalf("空响应大小异常: %+v", detail)
	}
	encoded, _ := json.Marshal(detail)
	if !strings.Contains(string(encoded), `"responseBytes":0`) {
		t.Fatalf("空响应大小未序列化: %s", encoded)
	}
}

func TestSafeErrorDetailCanBeDisabledAndIsGloballyLimited(t *testing.T) {
	cfg := config.Default().Observability
	response := &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}}
	result := attemptResult{response: response, buffer: buffered(t, `{"error":{"message":"`+strings.Repeat("错", 5000)+`","type":"long-type","code":"long-code"}}`)}

	detail := extractSafeErrorDetail(cfg, result, false)
	total := len(detail.Message) + len(detail.Type) + len(detail.Code) + len(detail.UpstreamRequestID) + len(detail.RetryAfter)
	if total > int(cfg.MaxErrorDetail) || !strings.HasSuffix(detail.Message, "...") {
		t.Fatalf("详情长度未受限: total=%d detail=%+v", total, detail)
	}
	cfg.ErrorDetails = "off"
	if disabled := extractSafeErrorDetail(cfg, result, false); disabled != nil {
		t.Fatalf("off 模式仍采集详情: %+v", disabled)
	}
}
