package admin

import (
	"bytes"
	"context"
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
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/monitoring"
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
	drain := httptest.NewRequest(http.MethodPost, "/admin/api/control/drain", nil)
	drain.Header.Set("Authorization", "Bearer admin-test-key")
	drainRecorder := httptest.NewRecorder()
	handler.ServeHTTP(drainRecorder, drain)
	if drainRecorder.Code != http.StatusOK || controller.Mode() != state.ControlDraining || controller.Accepting() {
		t.Fatalf("主动排空失败: %d %s mode=%s", drainRecorder.Code, drainRecorder.Body.String(), controller.Mode())
	}
	resume := httptest.NewRequest(http.MethodPost, "/admin/api/control/resume", nil)
	resume.Header.Set("Authorization", "Bearer admin-test-key")
	resumeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(resumeRecorder, resume)
	if resumeRecorder.Code != http.StatusOK || controller.Mode() != state.ControlRunning {
		t.Fatalf("排空恢复失败: %d %s", resumeRecorder.Code, resumeRecorder.Body.String())
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
