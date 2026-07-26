package admin

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

func TestAdminRequiresKeyAndControlsGateway(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "admin-test-key")
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	controller := state.NewController()
	handler := New(config.NewStore(path, cfg), state.NewRegistry(), controller)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/admin/api/status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未鉴权请求状态: %d", unauthorized.Code)
	}
	bareKey := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	bareKey.Header.Set("Authorization", "admin-test-key")
	bareKeyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(bareKeyRecorder, bareKey)
	if bareKeyRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("裸密钥鉴权状态: %d", bareKeyRecorder.Code)
	}

	pause := httptest.NewRequest(http.MethodPost, "/admin/api/control/pause", nil)
	pause.Header.Set("Authorization", "Bearer admin-test-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, pause)
	if recorder.Code != http.StatusOK || !controller.IsPaused() {
		t.Fatal("暂停失败")
	}
}

func TestAdminHistoryTimelineAndRedactedDiagnosticBundle(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			connection.Close()
		}
	}()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Upstream.BaseURL = "http://user:upstream-secret@" + listener.Addr().String() + "?token=hidden"
	cfg.Notifications.WebhookURL = "https://hooks.example.com/webhook-secret"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(path, cfg)
	registry := state.NewRegistry()
	id, _ := registry.Add("POST", "/v1/responses", func() {})
	registry.RecordEvent(id, timeline.Event{Type: "attempt_failed", Attempt: 1, StatusCode: 503, Message: "HTTP 503", ErrorDetail: &timeline.ErrorDetail{Message: "diagnostic-only-secret", Parsed: true}})
	registry.Remove(id, "successful")
	handler := NewWithServices(store, registry, state.NewController(), risk.New(), diagnostics.New(store, "test", time.Now()), nil)

	history := authenticatedRequest(handler, http.MethodGet, "/admin/api/history")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), id) {
		t.Fatalf("历史接口异常: %d %s", history.Code, history.Body.String())
	}
	timelineResponse := authenticatedRequest(handler, http.MethodGet, "/admin/api/requests/"+id+"/timeline")
	if timelineResponse.Code != http.StatusOK || !strings.Contains(timelineResponse.Body.String(), "HTTP 503") {
		t.Fatalf("时间线接口异常: %d %s", timelineResponse.Code, timelineResponse.Body.String())
	}
	if !strings.Contains(timelineResponse.Body.String(), "diagnostic-only-secret") {
		t.Fatal("时间线未返回安全错误详情")
	}
	bundle := authenticatedRequest(handler, http.MethodGet, "/admin/api/diagnostics/export")
	if bundle.Code != http.StatusOK || !strings.Contains(bundle.Header().Get("Content-Disposition"), "diagnostics.json") {
		t.Fatalf("诊断包接口异常: %d %s", bundle.Code, bundle.Body.String())
	}
	for _, secret := range []string{"upstream-secret", "hidden", "webhook-secret", "diagnostic-only-secret"} {
		if strings.Contains(bundle.Body.String(), secret) {
			t.Fatalf("诊断包泄露敏感值 %q", secret)
		}
	}
}

func authenticatedRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer 123456789012345678901234")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAdminRejectsTrailingConfigJSON(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "admin-test-key")
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	handler := New(config.NewStore(path, cfg), state.NewRegistry(), state.NewController())
	payload, _ := json.Marshal(cfg)
	payload = append(payload, []byte(` {"unexpected":true}`)...)
	request := httptest.NewRequest(http.MethodPut, "/admin/api/config", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer admin-test-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("尾随 JSON 应被拒绝，实际状态: %d，响应: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminPersistsValidConfig(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "admin-test-key")
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(path, cfg)
	handler := New(store, state.NewRegistry(), state.NewController())
	cfg.Retry.MaxAttempts = 42
	payload, _ := json.Marshal(cfg)
	request := httptest.NewRequest(http.MethodPut, "/admin/api/config", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer admin-test-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("保存失败: %s", recorder.Body.String())
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.Retry.MaxAttempts != 42 {
		t.Fatalf("配置未持久化: %+v %v", loaded, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("配置权限错误: %v %v", info.Mode(), err)
	}
}
