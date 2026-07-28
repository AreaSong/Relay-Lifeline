package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerAllowsChartStyleAttributesWithoutRelaxingScripts(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)

	Handler().ServeHTTP(recorder, request)

	policy := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "style-src-attr 'unsafe-inline'") {
		t.Fatalf("CSP 未允许图表运行时样式属性: %q", policy)
	}
	if strings.Contains(policy, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP 不应放宽内联脚本: %q", policy)
	}
}
