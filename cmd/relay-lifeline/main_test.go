package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/state"
)

func TestValidateManagementKeys(t *testing.T) {
	tests := []struct {
		name         string
		adminEnabled bool
		operatorKey  string
		viewerKey    string
		sensitiveKey string
		wantError    bool
	}{
		{name: "控制台关闭时允许空密钥", adminEnabled: false},
		{name: "控制台开启时拒绝空运维密钥", adminEnabled: true, sensitiveKey: "abcdefghijklmnopqrstuvwx", wantError: true},
		{name: "控制台开启时拒绝空敏感密钥", adminEnabled: true, operatorKey: "123456789012345678901234", wantError: true},
		{name: "拒绝相同的运维和敏感密钥", adminEnabled: true, operatorKey: "123456789012345678901234", sensitiveKey: "123456789012345678901234", wantError: true},
		{name: "接受独立分层密钥", adminEnabled: true, operatorKey: "123456789012345678901234", viewerKey: "viewer-key-12345678901234", sensitiveKey: "abcdefghijklmnopqrstuvwx"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateManagementKeys(test.adminEnabled, test.operatorKey, test.viewerKey, test.sensitiveKey)
			if (err != nil) != test.wantError {
				t.Fatalf("validateAdminKey() error = %v, wantError = %v", err, test.wantError)
			}
		})
	}
}

func TestReadinessHandlerDistinguishesReadyAndDraining(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	handler := readinessHandler(&ready)

	available := httptest.NewRecorder()
	handler.ServeHTTP(available, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if available.Code != http.StatusOK || !strings.Contains(available.Body.String(), "ready") {
		t.Fatalf("就绪响应异常: %d %s", available.Code, available.Body.String())
	}

	ready.Store(false)
	draining := httptest.NewRecorder()
	handler.ServeHTTP(draining, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if draining.Code != http.StatusServiceUnavailable || !strings.Contains(draining.Body.String(), "draining") {
		t.Fatalf("排空响应异常: %d %s", draining.Code, draining.Body.String())
	}
}

func TestReadinessHandlerReportsAdministrativeMaintenance(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	mode := state.ControlMaintenance
	handler := readinessHandlerWithMode(&ready, func() string { return mode })
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "maintenance") {
		t.Fatalf("维护 Readiness 异常: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestReadinessHandlerRejectsFailedDependency(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	handler := readinessHandler(&ready, func() error { return errors.New("journal unavailable") })
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "unavailable") {
		t.Fatalf("依赖异常时仍然就绪: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLocaleResolution(t *testing.T) {
	if locale, ok := environmentLocale("zh_CN.UTF-8"); !ok || locale != "zh-CN" {
		t.Fatalf("中文环境语言解析异常: %s %v", locale, ok)
	}
	if locale, ok := environmentLocale("C"); ok || locale != "en-US" {
		t.Fatalf("C 环境语言解析异常: %s %v", locale, ok)
	}
	if !hasLocaleArgument([]string{"--locale=en-US"}) || hasLocaleArgument([]string{"--config", "x"}) {
		t.Fatal("locale 参数识别异常")
	}
}

func TestRequestLoggerUsesCurrentConfiguredLocale(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := config.Default()
	store := config.NewStore("", cfg)
	handler := requestLogger(store, logger, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/first", nil))
	cfg.Logging.Locale = "en-US"
	if err := store.Update(cfg, false); err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/second", nil))

	logs := output.String()
	if !strings.Contains(logs, "收到请求") || !strings.Contains(logs, "Request received") || strings.Count(logs, `"event":"request.received"`) != 2 {
		t.Fatalf("日志语言未热更新或稳定事件缺失: %s", logs)
	}
}
