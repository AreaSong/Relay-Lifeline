package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
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
	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/repeat"
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
	requestJournal, err := journal.Open(filepath.Join(t.TempDir(), "requests.jsonl"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer requestJournal.Close()
	incidentJournal, err := journal.Open(filepath.Join(t.TempDir(), "incidents.jsonl"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer incidentJournal.Close()
	registry := state.NewRegistry()
	id, _ := registry.Add("POST", "/v1/responses", func() {})
	registry.UpdateMessage(id, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	registry.RecordEvent(id, timeline.Event{Type: "attempt_failed", Attempt: 1, StatusCode: 503, Message: "HTTP 503", ErrorDetail: &timeline.ErrorDetail{Message: "diagnostic-only-secret", Parsed: true}})
	registry.UpdateMessage(id, lifecycle.StateBuffering, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(id, lifecycle.StateDelivering, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(id, lifecycle.StateCompleted, 1, l10n.Message{}, time.Time{})
	registry.Remove(id, lifecycle.StateSuccessful)
	handler := NewWithServices(store, registry, state.NewController(), risk.New(), diagnostics.New(store, "test", time.Now()), nil)
	handler.SetJournals(requestJournal, incidentJournal)

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
	if bundle.Code != http.StatusOK || !strings.Contains(bundle.Header().Get("Content-Disposition"), "diagnostics.zip") {
		t.Fatalf("诊断包接口异常: %d %s", bundle.Code, bundle.Body.String())
	}
	files := diagnosticFiles(t, bundle.Body.Bytes())
	for _, name := range []string{"recovery-check.json", "journal-summary.json", "config-backups.json"} {
		if files[name] == "" {
			t.Fatalf("诊断包缺少 %s", name)
		}
	}
	if !strings.Contains(files["manifest.json"], `"schemaVersion": 2`) || !strings.Contains(files["manifest.json"], `"containsRawBodies": false`) {
		t.Fatalf("诊断清单契约异常: %s", files["manifest.json"])
	}
	for _, secret := range []string{"upstream-secret", "hidden", "webhook-secret", "diagnostic-only-secret"} {
		for name, body := range files {
			if strings.Contains(body, secret) {
				t.Fatalf("诊断包文件 %s 泄露敏感值 %q", name, secret)
			}
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
	registry.UpdateMessage(id, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	registry.RecordEvent(id, timeline.Event{Type: "attempt_failed", Attempt: 1, MessageCode: "proxy.connection_failed"})
	registry.Remove(id, lifecycle.StateFailed)
	handler := New(store, registry, state.NewController())

	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	unauthorizedRequest.Header.Set("Accept-Language", "en-US")
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, unauthorizedRequest)
	if unauthorized.Header().Get("Content-Language") != "en-US" || !strings.Contains(unauthorized.Body.String(), `"code":"AUTHENTICATION_REQUIRED"`) || !strings.Contains(unauthorized.Body.String(), "A management session or Bearer key is required") {
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

func TestRepeatTaskAndRetryPolicyAPIs(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_VIEWER_KEY", "viewer-key-12345678901234")
	registry := state.NewRegistry()
	requestID, _ := registry.Add("POST", "/v1/responses", func() {})
	manager, err := repeat.New(nil, func(_ context.Context, _ repeat.Template, _ string, id string) repeat.Execution {
		return repeat.Execution{ID: id, Success: true, Completed: time.Now()}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	manager.RegisterSource(requestID, repeat.Template{Method: "POST", Path: "/v1/responses", Headers: make(http.Header)})
	handler := New(config.NewStore("", config.Default()), registry, state.NewController())
	handler.SetRepeatManager(manager)

	viewer := repeatAPIRequest(handler, "viewer-key-12345678901234", http.MethodPost, "/admin/api/requests/"+requestID+"/retry-policy", `{"duration":"1m","interval":"5s"}`)
	if viewer.Code != http.StatusForbidden {
		t.Fatalf("Viewer 不应修改请求策略: %d %s", viewer.Code, viewer.Body.String())
	}
	policy := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+requestID+"/retry-policy", `{"duration":"1m","interval":"5s"}`)
	if policy.Code != http.StatusOK {
		t.Fatalf("设置限时恢复失败: %d %s", policy.Code, policy.Body.String())
	}
	storedPolicy, ok := registry.RetryPolicy(requestID)
	if !ok || storedPolicy.Interval != 5*time.Second {
		t.Fatalf("限时恢复策略未生效: %+v", storedPolicy)
	}
	missing := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/missing/retry-policy", `{"duration":"1m","interval":"5s"}`)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("非活动请求应返回 404: %d %s", missing.Code, missing.Body.String())
	}

	invalid := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+requestID+"/repeat", `{"interval":"5s","duration":"invalid","idempotency":"preserve","confirmForever":true}`)
	if invalid.Code != http.StatusBadRequest || len(manager.List()) != 0 {
		t.Fatalf("无效时长不应创建任务: code=%d tasks=%+v", invalid.Code, manager.List())
	}
	forever := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+requestID+"/repeat", `{"interval":"5s","duration":"","idempotency":"preserve","confirmForever":false}`)
	if forever.Code != http.StatusBadRequest || len(manager.List()) != 0 {
		t.Fatalf("永久任务未经确认不应创建: code=%d tasks=%+v", forever.Code, manager.List())
	}
	created := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/requests/"+requestID+"/repeat", `{"interval":"5s","duration":"1m","idempotency":"preserve"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("创建持续任务失败: %d %s", created.Code, created.Body.String())
	}
	var task repeat.Task
	if err := json.NewDecoder(created.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	listed := repeatAPIRequest(handler, "viewer-key-12345678901234", http.MethodGet, "/admin/api/repeat-tasks", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), task.ID) {
		t.Fatalf("Viewer 应可读取持续任务: %d %s", listed.Code, listed.Body.String())
	}
	for _, action := range []string{"pause", "resume", "run"} {
		response := repeatAPIRequest(handler, "123456789012345678901234", http.MethodPost, "/admin/api/repeat-tasks/"+task.ID+"/"+action, "")
		if response.Code != http.StatusOK {
			t.Fatalf("持续任务操作 %s 失败: %d %s", action, response.Code, response.Body.String())
		}
	}
	stopped := repeatAPIRequest(handler, "123456789012345678901234", http.MethodDelete, "/admin/api/repeat-tasks/"+task.ID, "")
	if stopped.Code != http.StatusOK || !strings.Contains(stopped.Body.String(), `"state":"stopped"`) {
		t.Fatalf("停止持续任务失败: %d %s", stopped.Code, stopped.Body.String())
	}
}

func repeatAPIRequest(handler http.Handler, key, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
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
	files := diagnosticFiles(t, bundle.Body.Bytes())
	if bundle.Code != http.StatusOK || !strings.Contains(files["runtime-logs.json"], `"event": "request.received"`) || !strings.Contains(files["metrics.json"], `"requests": 1`) || files["metric-errors.json"] == "" {
		t.Fatalf("诊断证据不完整: %d %s", bundle.Code, bundle.Body.String())
	}
}

func diagnosticFiles(t *testing.T, data []byte) map[string]string {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("诊断 ZIP 无效: %v", err)
	}
	result := make(map[string]string, len(archive.File))
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		result[file.Name] = string(body)
	}
	return result
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

func TestManagementSessionCookieCSRFAndRevocation(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	store := config.NewStore("", config.Default())
	handler := New(store, state.NewRegistry(), state.NewController())
	login := httptest.NewRequest(http.MethodPost, "/admin/api/session/login", strings.NewReader(`{"key":"123456789012345678901234"}`))
	login.RemoteAddr = "127.0.0.1:12345"
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("会话登录失败: %d %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	var info sessionInfo
	if err := json.NewDecoder(loginRecorder.Body).Decode(&info); err != nil || info.CSRFToken == "" {
		t.Fatalf("登录响应缺少 CSRF: %+v err=%v", info, err)
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("会话 Cookie 防护异常: %+v", cookies)
	}

	status := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	status.AddCookie(cookies[0])
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, status)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("Cookie 会话读取失败: %d", statusRecorder.Code)
	}

	pause := httptest.NewRequest(http.MethodPost, "/admin/api/control/pause", nil)
	pause.AddCookie(cookies[0])
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, pause)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺少 CSRF 未拒绝: %d %s", denied.Code, denied.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "/admin/api/session/logout", nil)
	logout.AddCookie(cookies[0])
	logout.Header.Set("X-CSRF-Token", info.CSRFToken)
	logoutRecorder := httptest.NewRecorder()
	handler.ServeHTTP(logoutRecorder, logout)
	cleared := logoutRecorder.Result().Cookies()
	if logoutRecorder.Code != http.StatusOK || len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("注销未撤销 Cookie: %d %+v", logoutRecorder.Code, cleared)
	}

	afterLogout := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	afterLogout.AddCookie(cookies[0])
	afterRecorder := httptest.NewRecorder()
	handler.ServeHTTP(afterRecorder, afterLogout)
	if afterRecorder.Code != http.StatusUnauthorized || !strings.Contains(afterRecorder.Body.String(), `"code":"SESSION_EXPIRED"`) {
		t.Fatalf("注销后会话仍有效: %d", afterRecorder.Code)
	}
}

func TestManagementAuthenticationErrorClassification(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	store := config.NewStore("", config.Default())
	handler := New(store, state.NewRegistry(), state.NewController())

	invalidBearer := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	invalidBearer.Header.Set("Authorization", "Bearer wrong-key")
	invalidRecorder := httptest.NewRecorder()
	handler.ServeHTTP(invalidRecorder, invalidBearer)
	if invalidRecorder.Code != http.StatusUnauthorized || !strings.Contains(invalidRecorder.Body.String(), `"code":"INVALID_ADMIN_KEY"`) {
		t.Fatalf("无效 Bearer 分类异常: %d %s", invalidRecorder.Code, invalidRecorder.Body.String())
	}

	handler.sessions.now = func() time.Time { return time.Unix(100, 0) }
	token, _, err := handler.sessions.login(httptest.NewRequest(http.MethodPost, "/admin/api/session/login", nil), "123456789012345678901234", handler.auth)
	if err != nil {
		t.Fatal(err)
	}
	handler.sessions.now = func() time.Time {
		return time.Unix(100, 0).Add(store.Get().ManagementSecurity.SessionIdleTimeout.Duration + time.Second)
	}
	expired := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	expired.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: token})
	expiredRecorder := httptest.NewRecorder()
	handler.ServeHTTP(expiredRecorder, expired)
	if expiredRecorder.Code != http.StatusUnauthorized || !strings.Contains(expiredRecorder.Body.String(), `"code":"SESSION_EXPIRED"`) {
		t.Fatalf("空闲超时分类异常: %d %s", expiredRecorder.Code, expiredRecorder.Body.String())
	}
}

func TestManagementLoginRateLimit(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	store := config.NewStore("", config.Default())
	handler := New(store, state.NewRegistry(), state.NewController())
	for attempt := 1; attempt <= store.Get().ManagementSecurity.LoginFailuresPerMinute; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/admin/api/session/login", strings.NewReader(`{"key":"wrong"}`))
		request.RemoteAddr = "192.0.2.10:12345"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败响应异常: %d", attempt, recorder.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/api/session/login", strings.NewReader(`{"key":"123456789012345678901234"}`))
	request.RemoteAddr = "192.0.2.10:54321"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("冷却期间未限速: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestManagementSSEStreamsControlSnapshot(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	store := config.NewStore("", config.Default())
	handler := New(store, state.NewRegistry(), state.NewController())
	handler.SetMonitoring(monitoring.New())
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/admin/api/stream", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer 123456789012345678901234")
	recorder := &sseTestRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()
	select {
	case <-recorder.flushed:
	case <-time.After(time.Second):
		t.Fatal("SSE 首个快照超时")
	}
	cancel()
	<-done
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(recorder.Body.String(), `"status"`) || !strings.Contains(recorder.Body.String(), `"incidents":[]`) {
		t.Fatalf("SSE 快照异常: headers=%v body=%s", recorder.Header(), recorder.Body.String())
	}
}

type sseTestRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func (r *sseTestRecorder) Flush() {
	r.ResponseRecorder.Flush()
	select {
	case r.flushed <- struct{}{}:
	default:
	}
}
