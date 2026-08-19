package webui

import (
	"io/fs"
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

func TestHandlerCachesFingerprintedAssetsButRevalidatesShell(t *testing.T) {
	entries, err := fs.ReadDir(assets, "dist/assets")
	if err != nil {
		t.Fatal(err)
	}
	assetPath := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".js") {
			assetPath = "/admin/assets/" + entry.Name()
			break
		}
	}
	if assetPath == "" {
		t.Fatal("嵌入资源中缺少指纹 JavaScript 文件")
	}
	tests := []struct {
		path string
		want string
	}{
		{path: "/admin/", want: "no-cache"},
		{path: assetPath, want: "public, max-age=31536000, immutable"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		Handler().ServeHTTP(recorder, request)
		if got := recorder.Header().Get("Cache-Control"); got != test.want {
			t.Fatalf("%s Cache-Control = %q, want %q", test.path, got, test.want)
		}
	}
}
