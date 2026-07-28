package admin

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/buildinfo"
	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

type Handler struct {
	store       *config.Store
	registry    *state.Registry
	controller  *state.Controller
	auth        authenticator
	risk        *risk.Manager
	diagnostics *diagnostics.Service
	notifier    *notify.Notifier
	captures    *capture.Manager
	runLogs     *runlog.Store
	monitor     *monitoring.Store
	runtimeInfo func() buildinfo.Info
	sessions    *sessionManager
	incidents   *incident.Store
}

func (h *Handler) SetMonitoring(store *monitoring.Store)         { h.monitor = store }
func (h *Handler) SetRuntimeInfo(provider func() buildinfo.Info) { h.runtimeInfo = provider }
func (h *Handler) SetIncidents(store *incident.Store)            { h.incidents = store }

func New(store *config.Store, registry *state.Registry, controller *state.Controller) *Handler {
	return NewWithServices(store, registry, controller, risk.New(), diagnostics.New(store, "dev", time.Now()), nil)
}

func NewWithServices(store *config.Store, registry *state.Registry, controller *state.Controller, riskManager *risk.Manager, diagnosticService *diagnostics.Service, notifier *notify.Notifier) *Handler {
	return NewWithExtendedServices(store, registry, controller, riskManager, diagnosticService, notifier, nil, nil)
}

func NewWithExtendedServices(store *config.Store, registry *state.Registry, controller *state.Controller, riskManager *risk.Manager, diagnosticService *diagnostics.Service, notifier *notify.Notifier, captures *capture.Manager, runLogs *runlog.Store) *Handler {
	return &Handler{
		store: store, registry: registry, controller: controller, auth: newAuthenticatorFromEnvironment(),
		risk: riskManager, diagnostics: diagnosticService, notifier: notifier, captures: captures, runLogs: runLogs,
		sessions: newSessionManager(store),
	}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	locale, fallback := h.requestLocales(request)
	writer.Header().Set("Content-Language", locale)
	path := strings.TrimPrefix(request.URL.Path, "/admin/api")
	if request.Method == http.MethodPost && path == "/session/login" {
		h.login(writer, request, locale, fallback)
		return
	}
	role, authenticated := h.auth.authenticate(request)
	cookieSession, sessionToken, cookieAuthenticated := h.sessions.authenticate(request)
	if cookieAuthenticated {
		role, authenticated = cookieSession.Role, true
	}
	if !authenticated {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.authentication_failed", Outcome: "denied"})
		h.writeError(writer, http.StatusUnauthorized, "INVALID_ADMIN_KEY", l10n.M("api.admin.invalid_key"), locale, fallback)
		return
	}
	if cookieAuthenticated && request.Method != http.MethodGet && !secureEqual(request.Header.Get("X-CSRF-Token"), cookieSession.CSRF) {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.csrf_failed", Outcome: "denied"})
		h.writeError(writer, http.StatusForbidden, "INVALID_CSRF_TOKEN", l10n.M("api.admin.csrf_invalid"), locale, fallback)
		return
	}
	writer.Header().Set("X-Relay-Lifeline-Role", string(role))
	if !role.allows(requiredRole(request, path)) {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.authorization_failed", Outcome: "denied"})
		h.writeError(writer, http.StatusForbidden, "INSUFFICIENT_PERMISSION", l10n.M("api.admin.permission_denied"), locale, fallback)
		return
	}
	switch {
	case request.Method == http.MethodGet && path == "/session":
		info := sessionFor(role)
		if cookieAuthenticated {
			info.CSRFToken = cookieSession.CSRF
		}
		writeJSON(writer, http.StatusOK, info)
	case request.Method == http.MethodPost && path == "/session/logout":
		if cookieAuthenticated {
			h.sessions.revoke(sessionToken)
			setSessionCookie(writer, request, "", -1)
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"loggedOut": true})
	case request.Method == http.MethodGet && path == "/meta":
		if h.runtimeInfo == nil {
			writeJSON(writer, http.StatusOK, buildinfo.New("dev", "unknown", "unknown", "", time.Now()).Snapshot(config.CurrentSchemaVersion))
			return
		}
		writeJSON(writer, http.StatusOK, h.runtimeInfo())
	case request.Method == http.MethodGet && path == "/status":
		writeJSON(writer, http.StatusOK, h.registry.LocalizedSnapshot(h.controller.IsPaused(), locale, fallback))
	case request.Method == http.MethodGet && path == "/stream":
		h.stream(writer, request, locale, fallback)
	case request.Method == http.MethodGet && path == "/metrics":
		h.metrics(writer, request)
	case request.Method == http.MethodGet && path == "/metrics/errors":
		h.metricErrors(writer, request)
	case request.Method == http.MethodGet && path == "/events":
		h.securityEvents(writer, request)
	case request.Method == http.MethodGet && path == "/config":
		if role == RoleViewer {
			writeJSON(writer, http.StatusOK, diagnostics.RedactedConfig(h.store.Get()))
		} else {
			writeJSON(writer, http.StatusOK, h.store.Get())
		}
	case request.Method == http.MethodPost && path == "/config/validate":
		h.validateConfig(writer, request)
	case request.Method == http.MethodGet && path == "/alerts":
		writeJSON(writer, http.StatusOK, risk.Localize(h.risk.Recent(100), locale, fallback))
	case request.Method == http.MethodGet && path == "/incidents":
		if h.incidents == nil {
			writeJSON(writer, http.StatusOK, []incident.Incident{})
			return
		}
		writeJSON(writer, http.StatusOK, h.incidents.List())
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/incidents/"):
		h.incident(writer, strings.TrimPrefix(path, "/incidents/"), locale, fallback)
	case request.Method == http.MethodGet && path == "/history":
		writeJSON(writer, http.StatusOK, timeline.LocalizeRecords(h.registry.History(), locale, fallback))
	case request.Method == http.MethodGet && path == "/runtime-logs":
		h.runtimeLogs(writer, request)
	case request.Method == http.MethodGet && path == "/runtime-logs/export":
		h.exportRuntimeLogs(writer, request)
	case request.Method == http.MethodGet && path == "/capture/status":
		h.captureStatus(writer, locale, fallback)
	case request.Method == http.MethodPost && path == "/capture/start":
		h.startCapture(writer, request, locale, fallback)
	case request.Method == http.MethodPost && path == "/capture/stop":
		h.stopCapture(writer, locale, fallback)
	case request.Method == http.MethodGet && path == "/capture/keys":
		h.captureKeyStatus(writer, locale, fallback)
	case request.Method == http.MethodPost && path == "/capture/keys/rewrap":
		h.rewrapCaptureKeys(writer, locale, fallback)
	case request.Method == http.MethodGet && path == "/captures":
		h.listCaptures(writer, locale, fallback)
	case request.Method == http.MethodDelete && path == "/captures/expired":
		h.deleteExpiredCaptures(writer, locale, fallback)
	case strings.HasPrefix(path, "/captures/"):
		h.captureRequest(writer, request, path, locale, fallback)
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/requests/") && strings.HasSuffix(path, "/timeline"):
		h.requestTimeline(writer, path, locale, fallback)
	case request.Method == http.MethodPost && path == "/diagnostics/run":
		h.runDiagnostics(writer, request)
	case request.Method == http.MethodGet && path == "/diagnostics/export":
		h.exportDiagnostics(writer, request)
	case request.Method == http.MethodPut && path == "/config":
		h.updateConfig(writer, request)
	case request.Method == http.MethodPost && path == "/config/reload":
		if err := h.store.Reload(); err != nil {
			h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.reload", Outcome: "failed"})
			h.writeConfigError(writer, err, locale, fallback)
			return
		}
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.reload", Outcome: "succeeded"})
		writeJSON(writer, http.StatusOK, map[string]bool{"reloaded": true})
	case request.Method == http.MethodPost && path == "/control/pause":
		changed := h.controller.Pause()
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.pause", Outcome: "succeeded", Changed: monitoring.Bool(changed)})
		writeJSON(writer, http.StatusOK, map[string]bool{"changed": changed, "paused": true})
	case request.Method == http.MethodPost && path == "/control/resume":
		changed := h.controller.Resume()
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.resume", Outcome: "succeeded", Changed: monitoring.Bool(changed)})
		writeJSON(writer, http.StatusOK, map[string]bool{"changed": changed, "paused": false})
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/requests/") && strings.HasSuffix(path, "/retry"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/retry")
		h.requestAction(writer, h.registry.RetryNow(id), locale, fallback)
	case request.Method == http.MethodDelete && strings.HasPrefix(path, "/requests/"):
		id := strings.TrimPrefix(path, "/requests/")
		h.requestAction(writer, h.registry.Cancel(id), locale, fallback)
	default:
		h.writeError(writer, http.StatusNotFound, "ENDPOINT_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
	}
}

type streamSnapshot struct {
	Status    state.Snapshot      `json:"status"`
	Alerts    []risk.Alert        `json:"alerts"`
	Incidents []incident.Incident `json:"incidents"`
	Metrics   *monitoring.Metrics `json:"metrics,omitempty"`
}

func (h *Handler) stream(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		h.writeError(writer, http.StatusInternalServerError, "STREAM_UNSUPPORTED", l10n.M("api.stream.unsupported"), locale, fallback)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache, no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	sequence := uint64(0)
	send := func() error {
		sequence++
		incidents := []incident.Incident{}
		if h.incidents != nil {
			incidents = h.incidents.List()
		}
		var metrics *monitoring.Metrics
		if h.monitor != nil {
			snapshot := h.monitor.Metrics(time.Hour)
			metrics = &snapshot
		}
		payload, err := json.Marshal(streamSnapshot{
			Status: h.registry.LocalizedSnapshot(h.controller.IsPaused(), locale, fallback),
			Alerts: risk.Localize(h.risk.Recent(100), locale, fallback), Incidents: incidents, Metrics: metrics,
		})
		if err != nil {
			return err
		}
		if _, err := writer.Write([]byte("id: " + strconv.FormatUint(sequence, 10) + "\nevent: snapshot\ndata: " + string(payload) + "\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := send(); err != nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
			if err := send(); err != nil {
				return
			}
		}
	}
}

func (h *Handler) login(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	var input struct {
		Key string `json:"key"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 8<<10))
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Key) == "" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_LOGIN_REQUEST", l10n.M("api.admin.invalid_key"), locale, fallback)
		return
	}
	token, session, err := h.sessions.login(request, input.Key, h.auth)
	if err != nil {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.authentication_failed", Outcome: "denied"})
		if errors.Is(err, errLoginRateLimited) {
			h.writeError(writer, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", l10n.M("api.admin.login_limited"), locale, fallback)
			return
		}
		h.writeError(writer, http.StatusUnauthorized, "INVALID_ADMIN_KEY", l10n.M("api.admin.invalid_key"), locale, fallback)
		return
	}
	setSessionCookie(writer, request, token, int(h.store.Get().ManagementSecurity.SessionIdleTimeout.Duration.Seconds()))
	info := sessionFor(session.Role)
	info.CSRFToken = session.CSRF
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.session_created", Outcome: "succeeded"})
	writeJSON(writer, http.StatusOK, info)
}

func (h *Handler) incident(writer http.ResponseWriter, id, locale, fallback string) {
	if h.incidents == nil {
		h.writeError(writer, http.StatusNotFound, "INCIDENT_NOT_FOUND", l10n.M("api.incident.not_found"), locale, fallback)
		return
	}
	item, ok := h.incidents.Get(id)
	if !ok {
		h.writeError(writer, http.StatusNotFound, "INCIDENT_NOT_FOUND", l10n.M("api.incident.not_found"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

type captureActivation struct {
	RequestLimit      int             `json:"requestLimit"`
	ActivationTimeout config.Duration `json:"activationTimeout"`
}

func (h *Handler) runtimeLogs(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	if h.runLogs == nil {
		writeJSON(writer, http.StatusOK, runlog.Page{Entries: []runlog.Entry{}})
		return
	}
	after, limit, ok := h.logPageParameters(writer, request, locale, fallback)
	if !ok {
		return
	}
	query := request.URL.Query()
	if query.Get("tail") == "true" {
		writeJSON(writer, http.StatusOK, h.runLogs.Tail(limit, query.Get("level"), query.Get("event"), query.Get("requestId")))
		return
	}
	writeJSON(writer, http.StatusOK, h.runLogs.Page(after, limit, query.Get("level"), query.Get("event"), query.Get("requestId")))
}

func (h *Handler) logPageParameters(writer http.ResponseWriter, request *http.Request, locale, fallback string) (uint64, int, bool) {
	query := request.URL.Query()
	after := uint64(0)
	if raw := query.Get("after"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			h.writeError(writer, http.StatusBadRequest, "INVALID_LOG_CURSOR", l10n.M("api.logs.cursor_invalid"), locale, fallback)
			return 0, 0, false
		}
		after = parsed
	}
	limit := 200
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			h.writeError(writer, http.StatusBadRequest, "INVALID_LOG_LIMIT", l10n.M("api.logs.limit_invalid"), locale, fallback)
			return 0, 0, false
		}
		limit = parsed
	}
	level := query.Get("level")
	if level != "" && level != "debug" && level != "info" && level != "warn" && level != "error" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_LOG_LEVEL", l10n.M("api.logs.level_invalid"), locale, fallback)
		return 0, 0, false
	}
	if len(query.Get("event")) > 128 || len(query.Get("requestId")) > 128 {
		h.writeError(writer, http.StatusBadRequest, "INVALID_LOG_FILTER", l10n.M("api.logs.filter_invalid"), locale, fallback)
		return 0, 0, false
	}
	if tail := query.Get("tail"); tail != "" && tail != "true" && tail != "false" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_LOG_TAIL", l10n.M("api.logs.tail_invalid"), locale, fallback)
		return 0, 0, false
	}
	return after, limit, true
}

func (h *Handler) metrics(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	if h.monitor == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "MONITORING_UNAVAILABLE", l10n.M("api.monitoring.unavailable"), locale, fallback)
		return
	}
	windowValue := request.URL.Query().Get("window")
	if windowValue == "" {
		windowValue = "1h"
	}
	window, valid := monitoring.ParseWindow(windowValue)
	if !valid {
		h.writeError(writer, http.StatusBadRequest, "INVALID_METRICS_WINDOW", l10n.M("api.monitoring.window_invalid"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, h.monitor.Metrics(window))
}

func (h *Handler) metricErrors(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	if h.monitor == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "MONITORING_UNAVAILABLE", l10n.M("api.monitoring.unavailable"), locale, fallback)
		return
	}
	windowValue := request.URL.Query().Get("window")
	if windowValue == "" {
		windowValue = "24h"
	}
	window, valid := monitoring.ParseWindow(windowValue)
	if !valid {
		h.writeError(writer, http.StatusBadRequest, "INVALID_METRICS_WINDOW", l10n.M("api.monitoring.window_invalid"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, h.monitor.ErrorsFor(window))
}

func (h *Handler) securityEvents(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	if h.monitor == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "MONITORING_UNAVAILABLE", l10n.M("api.monitoring.unavailable"), locale, fallback)
		return
	}
	after := uint64(0)
	if raw := request.URL.Query().Get("after"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			h.writeError(writer, http.StatusBadRequest, "INVALID_EVENT_CURSOR", l10n.M("api.monitoring.cursor_invalid"), locale, fallback)
			return
		}
		after = parsed
	}
	limit := 100
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			h.writeError(writer, http.StatusBadRequest, "INVALID_EVENT_LIMIT", l10n.M("api.monitoring.limit_invalid"), locale, fallback)
			return
		}
		limit = parsed
	}
	writeJSON(writer, http.StatusOK, h.monitor.Events(after, limit))
}

func (h *Handler) exportRuntimeLogs(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Disposition", "attachment; filename=relay-lifeline-runtime-logs.json")
	if h.runLogs == nil {
		writeJSON(writer, http.StatusOK, []runlog.Entry{})
		return
	}
	query := request.URL.Query()
	writeJSON(writer, http.StatusOK, h.runLogs.List(0, query.Get("level"), query.Get("event"), query.Get("requestId")))
}

func (h *Handler) captureStatus(writer http.ResponseWriter, locale, fallback string) {
	if h.captures == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.capture.unavailable"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, h.captures.Status())
}

func (h *Handler) startCapture(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	if h.captures == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.capture.unavailable"), locale, fallback)
		return
	}
	var input captureActivation
	if request.ContentLength != 0 {
		decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			h.writeError(writer, http.StatusBadRequest, "INVALID_CAPTURE_REQUEST", l10n.M("api.capture.invalid_request"), locale, fallback)
			return
		}
	}
	if err := h.captures.Activate(input.RequestLimit, input.ActivationTimeout.Duration); err != nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_START_FAILED", l10n.M("api.capture.start_failed", map[string]any{"Error": err.Error()}), locale, fallback)
		return
	}
	h.audit("capture.started", "诊断捕获已启动", map[string]any{"requestLimit": input.RequestLimit, "activationTimeout": input.ActivationTimeout.String()})
	writeJSON(writer, http.StatusOK, h.captures.Status())
}

func (h *Handler) stopCapture(writer http.ResponseWriter, locale, fallback string) {
	if h.captures == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.capture.unavailable"), locale, fallback)
		return
	}
	h.captures.Stop()
	h.audit("capture.stopped", "诊断捕获已停止", nil)
	writeJSON(writer, http.StatusOK, h.captures.Status())
}

func (h *Handler) captureKeyStatus(writer http.ResponseWriter, locale, fallback string) {
	if h.captures == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.capture.unavailable"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, h.captures.KeyStatus())
}

func (h *Handler) rewrapCaptureKeys(writer http.ResponseWriter, locale, fallback string) {
	if h.captures == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.capture.unavailable"), locale, fallback)
		return
	}
	result, err := h.captures.RewrapAll()
	if err != nil {
		h.audit("capture.keys_rewrap_failed", "捕获密钥重包裹失败", nil)
		h.writeError(writer, http.StatusInternalServerError, "CAPTURE_KEY_REWRAP_FAILED", l10n.M("api.capture.key_rewrap_failed"), locale, fallback)
		return
	}
	h.audit("capture.keys_rewrapped", "捕获密钥已重包裹", map[string]any{"activeId": result.ActiveID, "updated": result.Updated})
	writeJSON(writer, http.StatusOK, result)
}

func (h *Handler) listCaptures(writer http.ResponseWriter, locale, fallback string) {
	if h.captures == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.capture.unavailable"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, h.captures.List())
}

func (h *Handler) deleteExpiredCaptures(writer http.ResponseWriter, locale, fallback string) {
	if h.captures == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.capture.unavailable"), locale, fallback)
		return
	}
	count, err := h.captures.DeleteExpired()
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, "CAPTURE_DELETE_FAILED", l10n.M("api.capture.delete_failed"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]int{"deleted": count})
}

func (h *Handler) captureRequest(writer http.ResponseWriter, request *http.Request, path, locale, fallback string) {
	if h.captures == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.capture.unavailable"), locale, fallback)
		return
	}
	remainder := strings.TrimPrefix(path, "/captures/")
	id, action, _ := strings.Cut(remainder, "/")
	switch {
	case request.Method == http.MethodGet && action == "preview":
		preview, err := h.captures.Preview(id)
		if err != nil {
			h.captureError(writer, err, locale, fallback)
			return
		}
		writeJSON(writer, http.StatusOK, preview)
	case request.Method == http.MethodGet && action == "download":
		mode := request.URL.Query().Get("mode")
		if mode != "raw" && mode != "filtered" {
			h.writeError(writer, http.StatusBadRequest, "INVALID_CAPTURE_MODE", l10n.M("api.capture.invalid_request"), locale, fallback)
			return
		}
		if mode == "raw" && request.Header.Get("X-Relay-Lifeline-Confirm") != "download-sensitive" {
			h.writeError(writer, http.StatusPreconditionRequired, "RAW_DOWNLOAD_CONFIRMATION_REQUIRED", l10n.M("api.capture.confirm_required"), locale, fallback)
			return
		}
		record, ok := h.captures.Get(id)
		if !ok {
			h.captureError(writer, os.ErrNotExist, locale, fallback)
			return
		}
		var requestTimeline any
		if value, found := h.registry.Timeline(record.RequestID); found {
			requestTimeline = timeline.LocalizeRecord(value, locale, fallback)
		}
		writer.Header().Set("Content-Type", "application/zip")
		writer.Header().Set("Content-Disposition", "attachment; filename=relay-lifeline-capture-"+id+"-"+mode+".zip")
		if err := h.captures.Export(id, mode, requestTimeline, writer); err != nil {
			h.audit("capture.download_failed", "捕获包下载失败", map[string]any{"captureId": id, "mode": mode})
			return
		}
		h.audit("capture.downloaded", "捕获包已下载", map[string]any{"captureId": id, "mode": mode})
	case request.Method == http.MethodDelete && action == "":
		if err := h.captures.Delete(id); err != nil {
			h.captureError(writer, err, locale, fallback)
			return
		}
		h.audit("capture.deleted", "捕获记录已删除", map[string]any{"captureId": id})
		writeJSON(writer, http.StatusOK, map[string]bool{"deleted": true})
	default:
		h.writeError(writer, http.StatusNotFound, "ENDPOINT_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
	}
}

func (h *Handler) audit(event, message string, fields map[string]any) {
	if h.runLogs != nil {
		h.runLogs.Add(runlog.Entry{Level: "info", Event: event, Message: message, Fields: fields})
	}
}

func (h *Handler) captureError(writer http.ResponseWriter, err error, locale, fallback string) {
	if errors.Is(err, os.ErrNotExist) {
		h.writeError(writer, http.StatusNotFound, "CAPTURE_NOT_FOUND", l10n.M("api.capture.not_found"), locale, fallback)
		return
	}
	h.writeError(writer, http.StatusInternalServerError, "CAPTURE_FAILED", l10n.M("api.capture.failed"), locale, fallback)
}

func (h *Handler) requestTimeline(writer http.ResponseWriter, path, locale, fallback string) {
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/timeline")
	record, ok := h.registry.Timeline(id)
	if !ok {
		h.writeError(writer, http.StatusNotFound, "REQUEST_NOT_FOUND", l10n.M("api.request.not_found"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, timeline.LocalizeRecord(record, locale, fallback))
}

func (h *Handler) runDiagnostics(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	report := h.diagnostics.Run(request.Context(), locale, fallback)
	h.recordDiagnosticAlerts(report)
	writeJSON(writer, http.StatusOK, report)
}

func (h *Handler) exportDiagnostics(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	report := h.diagnostics.Run(request.Context(), locale, fallback)
	h.recordDiagnosticAlerts(report)
	history := h.registry.History()
	if len(history) > 50 {
		history = history[:50]
	}
	logs := []runlog.Entry{}
	if h.runLogs != nil {
		logs = h.runLogs.List(0, "", "", "")
		if len(logs) > 200 {
			logs = logs[len(logs)-200:]
		}
	}
	var metrics *monitoring.Metrics
	var metricErrors *monitoring.Errors
	if h.monitor != nil {
		snapshot := h.monitor.Metrics(time.Hour)
		errors := h.monitor.ErrorsFor(time.Hour)
		metrics, metricErrors = &snapshot, &errors
	}
	incidents := []incident.Incident{}
	if h.incidents != nil {
		incidents = h.incidents.List()
	}
	files := map[string]any{
		"manifest.json":         map[string]any{"schemaVersion": 1, "generatedAt": time.Now(), "containsRawBodies": false},
		"report.json":           report,
		"config.redacted.json":  diagnostics.RedactedConfig(h.store.Get()),
		"status.json":           h.registry.LocalizedSnapshot(h.controller.IsPaused(), locale, fallback),
		"history.redacted.json": timeline.LocalizeRecords(timeline.WithoutErrorDetails(history), locale, fallback),
		"alerts.json":           risk.Localize(h.risk.Recent(50), locale, fallback),
		"runtime-logs.json":     logs,
		"metrics.json":          metrics,
		"metric-errors.json":    metricErrors,
		"incidents.json":        incidents,
	}
	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Disposition", "attachment; filename=relay-lifeline-diagnostics.zip")
	archive := zip.NewWriter(writer)
	for _, name := range []string{"manifest.json", "report.json", "config.redacted.json", "status.json", "history.redacted.json", "alerts.json", "runtime-logs.json", "metrics.json", "metric-errors.json", "incidents.json"} {
		entry, err := archive.Create(name)
		if err != nil {
			_ = archive.Close()
			return
		}
		encoder := json.NewEncoder(entry)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(files[name]); err != nil {
			_ = archive.Close()
			return
		}
	}
	_ = archive.Close()
}

func (h *Handler) recordDiagnosticAlerts(report diagnostics.Report) {
	for _, check := range report.Checks {
		if check.ID != "disk" {
			continue
		}
		if check.Status != "fail" {
			h.risk.ResolveGlobal("disk_pressure")
			return
		}
		message := l10n.M(check.MessageCode, check.MessageDetails)
		for _, alert := range h.risk.RecordGlobalMessage("disk_pressure", "warning", message) {
			if h.notifier != nil {
				h.notifier.Send(notify.Event{Type: alert.Type, MessageCode: alert.MessageCode, MessageDetails: alert.MessageDetails})
			}
		}
	}
}

func (h *Handler) updateConfig(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	cfg, ok := h.decodeConfig(writer, request, locale, fallback)
	if !ok {
		return
	}
	plan := config.PlanChanges(h.store.Get(), cfg)
	result, err := h.store.UpdateWithResult(cfg, true)
	if err != nil {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.save", Outcome: "failed"})
		h.writeConfigError(writer, err, locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.save", Outcome: "succeeded", RestartRequired: monitoring.Bool(plan.RestartRequired)})
	writeJSON(writer, http.StatusOK, struct {
		Saved      bool   `json:"saved"`
		BackupPath string `json:"backupPath,omitempty"`
		config.ChangePlan
	}{Saved: true, BackupPath: result.BackupPath, ChangePlan: plan})
}

func (h *Handler) validateConfig(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	cfg, ok := h.decodeConfig(writer, request, locale, fallback)
	if !ok {
		return
	}
	if err := cfg.Validate(); err != nil {
		h.writeConfigError(writer, err, locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, config.PlanChanges(h.store.Get(), cfg))
}

func (h *Handler) decodeConfig(writer http.ResponseWriter, request *http.Request, locale, fallback string) (config.Config, bool) {
	reader := io.LimitReader(request.Body, 1<<20)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var cfg config.Config
	if err := decoder.Decode(&cfg); err != nil {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.save", Outcome: "failed"})
		h.writeError(writer, http.StatusBadRequest, "INVALID_CONFIG_JSON", l10n.M("api.config.invalid_json"), locale, fallback)
		return config.Config{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.save", Outcome: "failed"})
		h.writeError(writer, http.StatusBadRequest, "TRAILING_CONFIG_JSON", l10n.M("api.config.trailing_json"), locale, fallback)
		return config.Config{}, false
	}
	migrated, err := config.Migrate(cfg)
	if err != nil {
		h.writeConfigError(writer, err, locale, fallback)
		return config.Config{}, false
	}
	return migrated, true
}

func (h *Handler) recordSecurityEvent(event monitoring.SecurityEvent) {
	if h.monitor != nil {
		h.monitor.RecordSecurityEvent(event)
	}
}

func (h *Handler) requestAction(writer http.ResponseWriter, found bool, locale, fallback string) {
	if !found {
		h.writeError(writer, http.StatusNotFound, "REQUEST_NOT_FOUND", l10n.M("api.request.not_found"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"accepted": true})
}

type errorResponse struct {
	Code    string         `json:"code"`
	Error   string         `json:"error"`
	Details map[string]any `json:"details,omitempty"`
}

func (h *Handler) requestLocales(request *http.Request) (string, string) {
	cfg := h.store.Get().Localization
	return l10n.FromAcceptLanguage(request.Header.Get("Accept-Language"), cfg.DefaultLocale), cfg.FallbackLocale
}

func (h *Handler) writeError(writer http.ResponseWriter, status int, code string, message l10n.Message, locale, fallback string) {
	writeJSON(writer, status, errorResponse{Code: code, Error: l10n.Default.Text(locale, fallback, message), Details: message.Data})
}

func (h *Handler) writeConfigError(writer http.ResponseWriter, err error, locale, fallback string) {
	writeJSON(writer, http.StatusBadRequest, errorResponse{Code: "INVALID_CONFIG", Error: l10n.Default.Error(locale, fallback, err)})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func setSecurityHeaders(headers http.Header) {
	headers.Set("Cache-Control", "no-store")
	headers.Set("X-Relay-Lifeline-API-Version", buildinfo.AdminAPIVersion)
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("X-Frame-Options", "DENY")
	headers.Set("Referrer-Policy", "no-referrer")
}
