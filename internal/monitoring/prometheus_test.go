package monitoring

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrometheusHandlerExportsBoundedMetrics(t *testing.T) {
	store := New()
	store.RecordReceived()
	store.RecordAttempt()
	store.RecordAttemptFailure("server")
	store.SetLoad(2, 1, 1, 1)
	recorder := httptest.NewRecorder()
	store.PrometheusHandler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Prometheus 响应异常: %d %s", recorder.Code, recorder.Header())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"relay_lifeline_requests_24h 1",
		"relay_lifeline_active_requests 2",
		`relay_lifeline_failed_attempts_by_category_24h{category="server"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("Prometheus 输出缺少 %q:\n%s", expected, body)
		}
	}
}
