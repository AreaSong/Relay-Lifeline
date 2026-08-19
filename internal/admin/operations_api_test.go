package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/governance"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/telemetry"
	"github.com/areasong/relay-lifeline/internal/upstream"
)

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

func TestNotificationOperationsAPIs(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	received := make(chan struct{}, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		received <- struct{}{}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	cfg := config.Default()
	cfg.Notifications.WebhookURL = endpoint.URL
	store := config.NewStore("", cfg)
	notifier := notify.NewWithSigning(store, slog.New(slog.NewTextHandler(io.Discard, nil)), notify.SigningConfig{
		KeyID: "test-key", Secret: strings.Repeat("s", 32),
	})
	defer notifier.Close()
	handler := NewWithServices(store, state.NewRegistry(), state.NewController(), risk.New(), diagnostics.New(store, "test", time.Now()), notifier)

	queued := authenticatedRequest(handler, http.MethodPost, "/admin/api/notifications/test")
	if queued.Code != http.StatusAccepted || !strings.Contains(queued.Body.String(), `"queued":true`) {
		t.Fatalf("测试通知接口异常: %d %s", queued.Code, queued.Body.String())
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("测试通知未送达")
	}
	deadline := time.Now().Add(time.Second)
	for notifier.Snapshot().Delivered == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := authenticatedRequest(handler, http.MethodGet, "/admin/api/notifications/status")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"delivered":1`) {
		t.Fatalf("通知状态接口异常: %d %s", status.Code, status.Body.String())
	}
	history := authenticatedRequest(handler, http.MethodGet, "/admin/api/notifications/deliveries?limit=10")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"eventType":"test"`) {
		t.Fatalf("通知历史接口异常: %d %s", history.Code, history.Body.String())
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
	filtered := authenticatedRequest(handler, http.MethodGet, "/admin/api/runtime-logs?tail=true&q=received&since=2020-01-01T00:00:00Z")
	if filtered.Code != http.StatusOK || !strings.Contains(filtered.Body.String(), "request.received") {
		t.Fatalf("日志全文和时间筛选异常: %d %s", filtered.Code, filtered.Body.String())
	}
	invalidSince := authenticatedRequest(handler, http.MethodGet, "/admin/api/runtime-logs?since=yesterday")
	if invalidSince.Code != http.StatusBadRequest || !strings.Contains(invalidSince.Body.String(), "INVALID_LOG_FILTER") {
		t.Fatalf("非法日志时间未拒绝: %d %s", invalidSince.Code, invalidSince.Body.String())
	}

	bundle := authenticatedRequest(handler, http.MethodGet, "/admin/api/diagnostics/export")
	files := diagnosticFiles(t, bundle.Body.Bytes())
	if bundle.Code != http.StatusOK || !strings.Contains(files["runtime-logs.json"], `"event": "request.received"`) || !strings.Contains(files["metrics.json"], `"requests": 1`) || files["metric-errors.json"] == "" {
		t.Fatalf("诊断证据不完整: %d %s", bundle.Code, bundle.Body.String())
	}
}

func TestRuntimeStatusAPIsRedactUpstreamCredentials(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	handler := New(config.NewStore("", config.Default()), state.NewRegistry(), state.NewController())
	handler.SetRuntimeStatus(func() upstream.PoolStatus {
		return upstream.PoolStatus{Strategy: "weighted-priority", Targets: []upstream.TargetStatus{{
			Target: upstream.Target{ID: "a", BaseURL: "https://user:secret@relay.example.test/v1?token=hidden", Weight: 1}, State: upstream.CircuitClosed,
		}}}
	}, func() governance.Snapshot {
		return governance.Snapshot{Mode: "observe", Principals: 1, Entries: []governance.PrincipalUsage{{Principal: "bearer:0123456789abcdef"}}}
	})
	handler.SetTelemetryStatus(func() telemetry.Status {
		return telemetry.Status{Enabled: true, Healthy: false, TraceHealthy: false, MetricHealthy: true, TraceExportFailures: 2}
	})
	upstreams := authenticatedRequest(handler, http.MethodGet, "/admin/api/upstreams/status")
	if upstreams.Code != http.StatusOK || strings.Contains(upstreams.Body.String(), "secret") || strings.Contains(upstreams.Body.String(), "hidden") || !strings.Contains(upstreams.Body.String(), "relay.example.test") {
		t.Fatalf("上游状态脱敏异常: %d %s", upstreams.Code, upstreams.Body.String())
	}
	governanceStatus := authenticatedRequest(handler, http.MethodGet, "/admin/api/governance/status")
	if governanceStatus.Code != http.StatusOK || !strings.Contains(governanceStatus.Body.String(), `"mode":"observe"`) {
		t.Fatalf("治理状态接口异常: %d %s", governanceStatus.Code, governanceStatus.Body.String())
	}
	telemetryStatus := authenticatedRequest(handler, http.MethodGet, "/admin/api/telemetry/status")
	if telemetryStatus.Code != http.StatusOK || !strings.Contains(telemetryStatus.Body.String(), `"traceExportFailures":2`) || !strings.Contains(telemetryStatus.Body.String(), `"healthy":false`) {
		t.Fatalf("Telemetry 状态接口异常: %d %s", telemetryStatus.Code, telemetryStatus.Body.String())
	}
}

func TestConfigHistoryDiffAndProtectedRollback(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(path, cfg)
	next := cfg
	next.Queue.MaxActive++
	if _, err := store.UpdateWithResult(next, true); err != nil {
		t.Fatal(err)
	}
	monitor := monitoring.New()
	handler := New(store, state.NewRegistry(), state.NewController())
	handler.SetMonitoring(monitor)

	list := authenticatedRequest(handler, http.MethodGet, "/admin/api/config/backups")
	if list.Code != http.StatusOK {
		t.Fatalf("配置历史接口异常: %d %s", list.Code, list.Body.String())
	}
	var versions struct {
		Items []configVersion `json:"items"`
	}
	if json.NewDecoder(list.Body).Decode(&versions) != nil || len(versions.Items) != 1 || versions.Items[0].SHA256 == "" || len(versions.Items[0].Diff.Fields) == 0 {
		t.Fatalf("配置历史差异不完整: %+v", versions)
	}
	version := versions.Items[0]
	body := `{"sha256":"` + version.SHA256 + `"}`
	rollback := httptest.NewRequest(http.MethodPost, "/admin/api/config/backups/"+version.Name+"/rollback", strings.NewReader(body))
	rollback.Header.Set("Authorization", "Bearer 123456789012345678901234")
	rollback.Header.Set("X-Relay-Lifeline-Confirm", "rollback-config")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, rollback)
	if recorder.Code != http.StatusOK || store.Desired().Queue.MaxActive != cfg.Queue.MaxActive {
		t.Fatalf("配置回滚失败: %d %s desired=%d", recorder.Code, recorder.Body.String(), store.Desired().Queue.MaxActive)
	}
	events := monitor.Events(0, 10).Events
	if len(events) != 1 || events[0].Code != "config.rollback" || events[0].Outcome != "succeeded" {
		t.Fatalf("配置回滚审计缺失: %+v", events)
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
