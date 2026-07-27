package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/buildinfo"
	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
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
	return localizedAuthenticatedRequest(handler, method, path, "")
}

func localizedAuthenticatedRequest(handler http.Handler, method, path, locale string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer 123456789012345678901234")
	if locale != "" {
		request.Header.Set("Accept-Language", locale)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAdminLocalizesErrorsAndStoredHistoryPerRequest(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	cfg := config.Default()
	store := config.NewStore("", cfg)
	registry := state.NewRegistry()
	id, _ := registry.Add("POST", "/v1/responses", func() {})
	registry.RecordEvent(id, timeline.Event{Type: "attempt_failed", Attempt: 1, MessageCode: "proxy.connection_failed"})
	registry.Remove(id, "failed")
	handler := New(store, registry, state.NewController())

	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	unauthorizedRequest.Header.Set("Accept-Language", "en-US")
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, unauthorizedRequest)
	if unauthorized.Header().Get("Content-Language") != "en-US" || !strings.Contains(unauthorized.Body.String(), `"code":"INVALID_ADMIN_KEY"`) || !strings.Contains(unauthorized.Body.String(), "Invalid admin key") {
		t.Fatalf("英文错误响应异常: header=%q body=%s", unauthorized.Header().Get("Content-Language"), unauthorized.Body.String())
	}

	english := localizedAuthenticatedRequest(handler, http.MethodGet, "/admin/api/history", "en-US")
	chinese := localizedAuthenticatedRequest(handler, http.MethodGet, "/admin/api/history", "zh-CN")
	if english.Header().Get("Content-Language") != "en-US" || !strings.Contains(english.Body.String(), "Upstream connection failed") {
		t.Fatalf("英文历史异常: header=%q body=%s", english.Header().Get("Content-Language"), english.Body.String())
	}
	if chinese.Header().Get("Content-Language") != "zh-CN" || !strings.Contains(chinese.Body.String(), "上游连接失败") {
		t.Fatalf("中文历史异常: header=%q body=%s", chinese.Header().Get("Content-Language"), chinese.Body.String())
	}
	if !strings.Contains(english.Body.String(), `"messageCode":"proxy.connection_failed"`) || !strings.Contains(chinese.Body.String(), `"messageCode":"proxy.connection_failed"`) {
		t.Fatal("双语历史未保留稳定消息代码")
	}
	if got := l10n.FromAcceptLanguage("fr-FR", cfg.Localization.DefaultLocale); got != "zh-CN" {
		t.Fatalf("不支持的请求语言未回退到默认语言: %s", got)
	}
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

func TestAdminCaptureAndRuntimeLogAPIs(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_SENSITIVE_KEY", "abcdefghijklmnopqrstuvwx")
	cfg := config.Default()
	cfg.Capture.StorageDir = t.TempDir()
	cfg.Capture.MaxBodySize = 1 << 20
	cfg.Capture.MaxTotalSize = 8 << 20
	cfg.Capture.MinimumFreeDisk = 64 << 20
	store := config.NewStore("", cfg)
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32))
	captures := capture.New(func() config.CaptureConfig { return store.Get().Capture }, key)
	logs := runlog.New(func() runlog.Limits { return runlog.Limits{MaxItems: 100, Retention: time.Hour} })
	registry := state.NewRegistry()
	handler := NewWithExtendedServices(store, registry, state.NewController(), risk.New(), diagnostics.New(store, "test", time.Now()), nil, captures, logs)

	startRequest := httptest.NewRequest(http.MethodPost, "/admin/api/capture/start", strings.NewReader(`{"requestLimit":1,"activationTimeout":"1m"}`))
	startRequest.Header.Set("Authorization", "Bearer 123456789012345678901234")
	startRecorder := httptest.NewRecorder()
	handler.ServeHTTP(startRecorder, startRequest)
	if startRecorder.Code != http.StatusOK || !strings.Contains(startRecorder.Body.String(), `"active":true`) {
		t.Fatalf("启动捕获失败: %d %s", startRecorder.Code, startRecorder.Body.String())
	}

	id, err := captures.BeginRequest("request-capture", http.MethodPost, "/v1/responses", http.Header{"Content-Type": []string{"application/json"}}, []byte(`{"api_key":"secret"}`))
	if err != nil || id == "" {
		t.Fatalf("创建捕获失败: %q %v", id, err)
	}
	if err := captures.RecordAttempt("request-capture", 1, 200, http.Header{"Content-Type": []string{"application/json"}}, strings.NewReader(`{"status":"completed"}`), nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := captures.Finish("request-capture", "successful", 1); err != nil {
		t.Fatal(err)
	}
	keyStatus := authenticatedRequest(handler, http.MethodGet, "/admin/api/capture/keys")
	if keyStatus.Code != http.StatusOK || !strings.Contains(keyStatus.Body.String(), `"activeId":"legacy"`) || !strings.Contains(keyStatus.Body.String(), `"legacy":1`) {
		t.Fatalf("捕获密钥状态异常: %d %s", keyStatus.Code, keyStatus.Body.String())
	}
	rewrap := authenticatedRequest(handler, http.MethodPost, "/admin/api/capture/keys/rewrap")
	if rewrap.Code != http.StatusOK || !strings.Contains(rewrap.Body.String(), `"unchanged":1`) {
		t.Fatalf("捕获密钥重包裹接口异常: %d %s", rewrap.Code, rewrap.Body.String())
	}

	preview := authenticatedRequest(handler, http.MethodGet, "/admin/api/captures/"+id+"/preview")
	if preview.Code != http.StatusOK || strings.Contains(preview.Body.String(), "secret") || !strings.Contains(preview.Body.String(), "[REDACTED]") {
		t.Fatalf("过滤预览异常: %d %s", preview.Code, preview.Body.String())
	}
	rawWithoutConfirmation := authenticatedRequest(handler, http.MethodGet, "/admin/api/captures/"+id+"/download?mode=raw")
	if rawWithoutConfirmation.Code != http.StatusForbidden {
		t.Fatalf("Operator 不应下载原文: %d", rawWithoutConfirmation.Code)
	}
	sensitiveWithoutConfirmation := httptest.NewRequest(http.MethodGet, "/admin/api/captures/"+id+"/download?mode=raw", nil)
	sensitiveWithoutConfirmation.Header.Set("Authorization", "Bearer abcdefghijklmnopqrstuvwx")
	sensitiveRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sensitiveRecorder, sensitiveWithoutConfirmation)
	if sensitiveRecorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("Sensitive Data 原文下载未要求确认: %d", sensitiveRecorder.Code)
	}
	rawRequest := httptest.NewRequest(http.MethodGet, "/admin/api/captures/"+id+"/download?mode=raw", nil)
	rawRequest.Header.Set("Authorization", "Bearer abcdefghijklmnopqrstuvwx")
	rawRequest.Header.Set("X-Relay-Lifeline-Confirm", "download-sensitive")
	rawRecorder := httptest.NewRecorder()
	handler.ServeHTTP(rawRecorder, rawRequest)
	if rawRecorder.Code != http.StatusOK || rawRecorder.Header().Get("Content-Type") != "application/zip" || rawRecorder.Body.Len() == 0 {
		t.Fatalf("原文下载异常: %d %s", rawRecorder.Code, rawRecorder.Header().Get("Content-Type"))
	}
	logResponse := authenticatedRequest(handler, http.MethodGet, "/admin/api/runtime-logs")
	if logResponse.Code != http.StatusOK || !strings.Contains(logResponse.Body.String(), "capture.downloaded") {
		t.Fatalf("审计日志缺失: %d %s", logResponse.Code, logResponse.Body.String())
	}
}

func TestManagementRolesSeparateReadOperateAndSensitiveAccess(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_VIEWER_KEY", "viewer-key-12345678901234")
	t.Setenv("RELAY_LIFELINE_SENSITIVE_KEY", "abcdefghijklmnopqrstuvwx")
	cfg := config.Default()
	cfg.Notifications.WebhookURL = "https://example.test/private-hook"
	handler := New(config.NewStore("", cfg), state.NewRegistry(), state.NewController())

	viewerSession := httptest.NewRequest(http.MethodGet, "/admin/api/session", nil)
	viewerSession.Header.Set("Authorization", "Bearer viewer-key-12345678901234")
	viewerRecorder := httptest.NewRecorder()
	handler.ServeHTTP(viewerRecorder, viewerSession)
	if viewerRecorder.Code != http.StatusOK || !strings.Contains(viewerRecorder.Body.String(), `"role":"viewer"`) || strings.Contains(viewerRecorder.Body.String(), "operate") {
		t.Fatalf("Viewer 会话能力异常: %d %s", viewerRecorder.Code, viewerRecorder.Body.String())
	}

	viewerPause := httptest.NewRequest(http.MethodPost, "/admin/api/control/pause", nil)
	viewerPause.Header.Set("Authorization", "Bearer viewer-key-12345678901234")
	pauseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(pauseRecorder, viewerPause)
	if pauseRecorder.Code != http.StatusForbidden || !strings.Contains(pauseRecorder.Body.String(), "INSUFFICIENT_PERMISSION") {
		t.Fatalf("Viewer 写操作未拒绝: %d %s", pauseRecorder.Code, pauseRecorder.Body.String())
	}

	viewerConfig := httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)
	viewerConfig.Header.Set("Authorization", "Bearer viewer-key-12345678901234")
	configRecorder := httptest.NewRecorder()
	handler.ServeHTTP(configRecorder, viewerConfig)
	if configRecorder.Code != http.StatusOK || strings.Contains(configRecorder.Body.String(), "private-hook") {
		t.Fatalf("Viewer 配置未脱敏: %d %s", configRecorder.Code, configRecorder.Body.String())
	}

	operatorSession := authenticatedRequest(handler, http.MethodGet, "/admin/api/session")
	if operatorSession.Code != http.StatusOK || !strings.Contains(operatorSession.Body.String(), `"role":"operator"`) || !strings.Contains(operatorSession.Body.String(), "operate") || strings.Contains(operatorSession.Body.String(), "sensitive") {
		t.Fatalf("Operator 会话能力异常: %d %s", operatorSession.Code, operatorSession.Body.String())
	}
}

func TestAdminMonitoringAPIsAndSecurityEventCursor(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(path, cfg)
	handler := New(store, state.NewRegistry(), state.NewController())
	metricsStore := monitoring.New()
	metricsStore.RecordReceived()
	metricsStore.RecordAttempt()
	metricsStore.RecordAttemptFailure("auth")
	metricsStore.RecordFinal("failed")
	handler.SetMonitoring(metricsStore)

	metricsResponse := authenticatedRequest(handler, http.MethodGet, "/admin/api/metrics?window=15m")
	if metricsResponse.Code != http.StatusOK {
		t.Fatalf("指标接口异常: %d %s", metricsResponse.Code, metricsResponse.Body.String())
	}
	var metrics monitoring.Metrics
	if err := json.NewDecoder(metricsResponse.Body).Decode(&metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Window != "15m" || len(metrics.Series) != 15 || metrics.Totals.Requests != 1 || metrics.Totals.Failed != 1 {
		t.Fatalf("指标响应异常: %+v", metrics)
	}

	errorResponse := authenticatedRequest(handler, http.MethodGet, "/admin/api/metrics/errors?window=15m")
	if errorResponse.Code != http.StatusOK || !strings.Contains(errorResponse.Body.String(), `"window":"15m"`) || !strings.Contains(errorResponse.Body.String(), `"code":"auth","count":1`) {
		t.Fatalf("错误分布接口异常: %d %s", errorResponse.Code, errorResponse.Body.String())
	}
	invalidWindow := authenticatedRequest(handler, http.MethodGet, "/admin/api/metrics?window=30m")
	if invalidWindow.Code != http.StatusBadRequest || !strings.Contains(invalidWindow.Body.String(), "INVALID_METRICS_WINDOW") {
		t.Fatalf("非法指标窗口未拒绝: %d %s", invalidWindow.Code, invalidWindow.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/admin/api/status", nil))
	authenticatedRequest(handler, http.MethodPost, "/admin/api/control/pause")
	authenticatedRequest(handler, http.MethodPost, "/admin/api/control/resume")

	payload, _ := json.Marshal(cfg)
	saveRequest := httptest.NewRequest(http.MethodPut, "/admin/api/config", bytes.NewReader(payload))
	saveRequest.Header.Set("Authorization", "Bearer 123456789012345678901234")
	saveRecorder := httptest.NewRecorder()
	handler.ServeHTTP(saveRecorder, saveRequest)
	if saveRecorder.Code != http.StatusOK {
		t.Fatalf("配置保存事件准备失败: %d %s", saveRecorder.Code, saveRecorder.Body.String())
	}
	reloadRecorder := authenticatedRequest(handler, http.MethodPost, "/admin/api/config/reload")
	if reloadRecorder.Code != http.StatusOK {
		t.Fatalf("配置重载事件准备失败: %d %s", reloadRecorder.Code, reloadRecorder.Body.String())
	}

	eventsResponse := authenticatedRequest(handler, http.MethodGet, "/admin/api/events?after=0&limit=2")
	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("安全事件接口异常: %d %s", eventsResponse.Code, eventsResponse.Body.String())
	}
	var page monitoring.EventPage
	if err := json.NewDecoder(eventsResponse.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || !page.HasMore || page.NextAfter != 2 || page.Events[0].Code != "admin.authentication_failed" || page.Events[1].Code != "admin.pause" {
		t.Fatalf("安全事件游标异常: %+v", page)
	}
	next := authenticatedRequest(handler, http.MethodGet, "/admin/api/events?after=2&limit=10")
	if next.Code != http.StatusOK || !strings.Contains(next.Body.String(), "admin.resume") || !strings.Contains(next.Body.String(), "config.save") || !strings.Contains(next.Body.String(), "config.reload") {
		t.Fatalf("安全事件续页异常: %d %s", next.Code, next.Body.String())
	}
}

func TestRuntimeLogPagingValidationAndDiagnosticEvidence(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	cfg := config.Default()
	store := config.NewStore("", cfg)
	logs := runlog.New(func() runlog.Limits { return runlog.Limits{MaxItems: 3, Retention: time.Hour} })
	for index := 0; index < 5; index++ {
		logs.Add(runlog.Entry{Level: "info", Event: "request.received"})
	}
	registry := state.NewRegistry()
	handler := NewWithExtendedServices(store, registry, state.NewController(), risk.New(), diagnostics.New(store, "test", time.Now()), nil, nil, logs)
	monitor := monitoring.New()
	monitor.RecordReceived()
	handler.SetMonitoring(monitor)

	tail := authenticatedRequest(handler, http.MethodGet, "/admin/api/runtime-logs?tail=true&limit=2")
	if tail.Code != http.StatusOK {
		t.Fatalf("日志尾页接口异常: %d %s", tail.Code, tail.Body.String())
	}
	var page runlog.Page
	if err := json.NewDecoder(tail.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].ID != 4 || page.NextAfter != 5 || !page.HasGap {
		t.Fatalf("日志尾页契约异常: %+v", page)
	}
	invalid := authenticatedRequest(handler, http.MethodGet, "/admin/api/runtime-logs?after=not-a-cursor")
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "INVALID_LOG_CURSOR") {
		t.Fatalf("非法日志游标未拒绝: %d %s", invalid.Code, invalid.Body.String())
	}

	bundle := authenticatedRequest(handler, http.MethodGet, "/admin/api/diagnostics/export")
	if bundle.Code != http.StatusOK || !strings.Contains(bundle.Body.String(), `"runtimeLogs":[`) || !strings.Contains(bundle.Body.String(), `"metrics":{`) || !strings.Contains(bundle.Body.String(), `"errors":{`) {
		t.Fatalf("诊断证据不完整: %d %s", bundle.Code, bundle.Body.String())
	}
}

func TestAdminMetaValidateAndAPIVersionContract(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	cfg := config.Default()
	store := config.NewStore("", cfg)
	handler := New(store, state.NewRegistry(), state.NewController())
	startedAt := time.Now().Add(-time.Minute)
	handler.SetRuntimeInfo(func() buildinfo.Info {
		return buildinfo.New("v0.4.0", "abc123", "2026-07-27T00:00:00Z", "relay-lifeline:test", startedAt).Snapshot(config.CurrentSchemaVersion)
	})

	meta := authenticatedRequest(handler, http.MethodGet, "/admin/api/meta")
	if meta.Code != http.StatusOK || meta.Header().Get("X-Relay-Lifeline-API-Version") != buildinfo.AdminAPIVersion {
		t.Fatalf("运行信息契约异常: status=%d version=%q body=%s", meta.Code, meta.Header().Get("X-Relay-Lifeline-API-Version"), meta.Body.String())
	}
	var info buildinfo.Info
	if err := json.NewDecoder(meta.Body).Decode(&info); err != nil || info.Version != "v0.4.0" || info.Revision != "abc123" || info.ConfigSchemaVersion != config.CurrentSchemaVersion {
		t.Fatalf("运行信息响应异常: %+v err=%v", info, err)
	}

	changed := cfg
	changed.Retry.MaxAttempts = 5
	payload, _ := json.Marshal(changed)
	request := httptest.NewRequest(http.MethodPost, "/admin/api/config/validate", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer 123456789012345678901234")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"changedSections":["retry"]`) || !strings.Contains(recorder.Body.String(), `"restartRequired":false`) {
		t.Fatalf("配置预检响应异常: %d %s", recorder.Code, recorder.Body.String())
	}
	if store.Get().Retry.MaxAttempts != cfg.Retry.MaxAttempts {
		t.Fatal("配置预检不应修改运行配置")
	}
}
