package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/buildinfo"
	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/repeat"
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
	journals    map[string]*journal.Store
	repeater    *repeat.Manager
	streamMu    sync.Mutex
	streamFeeds map[string]*realtimeFeed
}

func (h *Handler) SetMonitoring(store *monitoring.Store)         { h.monitor = store }
func (h *Handler) SetRuntimeInfo(provider func() buildinfo.Info) { h.runtimeInfo = provider }
func (h *Handler) SetIncidents(store *incident.Store)            { h.incidents = store }
func (h *Handler) SetRepeatManager(manager *repeat.Manager)      { h.repeater = manager }
func (h *Handler) SetJournals(requests, incidents *journal.Store, repeats ...*journal.Store) {
	h.journals = map[string]*journal.Store{"requests": requests, "incidents": incidents}
	if len(repeats) > 0 {
		h.journals["repeat-tasks"] = repeats[0]
	}
}

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
		sessions: newSessionManager(store), streamFeeds: make(map[string]*realtimeFeed),
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
	cookieSession, sessionToken, cookieAuthenticated, cookieState := h.sessions.authenticate(request)
	if cookieAuthenticated {
		role, authenticated = cookieSession.Role, true
	}
	if !authenticated {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.authentication_failed", Outcome: "denied"})
		switch {
		case cookieState == "invalidated" || cookieState == "idle_timeout":
			h.writeError(writer, http.StatusUnauthorized, "SESSION_EXPIRED", l10n.M("api.admin.session_expired"), locale, fallback)
		case request.Header.Get("Authorization") == "":
			h.writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", l10n.M("api.admin.authentication_required"), locale, fallback)
		default:
			h.writeError(writer, http.StatusUnauthorized, "INVALID_ADMIN_KEY", l10n.M("api.admin.invalid_key"), locale, fallback)
		}
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
	case request.Method == http.MethodGet && path == "/notifications/status":
		h.notificationStatus(writer, locale, fallback)
	case request.Method == http.MethodGet && path == "/notifications/deliveries":
		h.notificationDeliveries(writer, request, locale, fallback)
	case request.Method == http.MethodPost && path == "/notifications/test":
		h.testNotification(writer, locale, fallback)
	case request.Method == http.MethodGet && path == "/incidents":
		h.incidentList(writer, request, locale, fallback)
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/incidents/"):
		h.incident(writer, strings.TrimPrefix(path, "/incidents/"), locale, fallback)
	case request.Method == http.MethodGet && path == "/history":
		h.historyList(writer, request, locale, fallback)
	case request.Method == http.MethodGet && path == "/repeat-tasks":
		if h.repeater == nil {
			writeJSON(writer, http.StatusOK, []repeat.Task{})
			return
		}
		writeJSON(writer, http.StatusOK, h.repeater.List())
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
	case request.Method == http.MethodPost && path == "/requests/batch/retry":
		h.batchRetry(writer, request, locale, fallback)
	case request.Method == http.MethodPost && path == "/requests/batch/retry-policy":
		h.batchRetryPolicy(writer, request, locale, fallback)
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/requests/") && strings.HasSuffix(path, "/retry"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/retry")
		h.retryRequest(writer, request, id, locale, fallback)
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/requests/") && strings.HasSuffix(path, "/retry-policy"):
		h.setRetryPolicy(writer, request, strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/retry-policy"), locale, fallback)
	case request.Method == http.MethodDelete && strings.HasPrefix(path, "/requests/") && strings.HasSuffix(path, "/retry-policy"):
		h.clearRetryPolicy(writer, request, strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/retry-policy"), locale, fallback)
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/requests/") && strings.HasSuffix(path, "/repeat"):
		h.createRepeatTask(writer, request, strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/repeat"), locale, fallback)
	case strings.HasPrefix(path, "/repeat-tasks/"):
		h.repeatTaskAction(writer, request, strings.TrimPrefix(path, "/repeat-tasks/"), locale, fallback)
	case request.Method == http.MethodDelete && strings.HasPrefix(path, "/requests/"):
		id := strings.TrimPrefix(path, "/requests/")
		h.requestAction(writer, h.registry.Cancel(id), locale, fallback)
	default:
		h.writeError(writer, http.StatusNotFound, "ENDPOINT_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
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
	const requestLimit = 100
	requests := make([]timeline.Record, 0, min(len(item.AffectedRequests), requestLimit))
	for _, requestID := range item.AffectedRequests {
		if len(requests) == requestLimit {
			break
		}
		if record, found := h.registry.Timeline(requestID); found {
			requests = append(requests, timeline.LocalizeRecord(record, locale, fallback))
		}
	}
	writeJSON(writer, http.StatusOK, incidentDetail{Incident: item, Requests: requests, AffectedRequestsTruncated: len(item.AffectedRequests) > requestLimit})
}

func (h *Handler) historyList(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	query, err := parseListQuery(request.URL.Query(), 200)
	if err != nil {
		h.writeError(writer, http.StatusBadRequest, "INVALID_HISTORY_QUERY", l10n.M("api.history.query_invalid"), locale, fallback)
		return
	}
	page := queryHistory(h.registry.History(), query)
	page.Items = timeline.LocalizeRecords(page.Items, locale, fallback)
	writeJSON(writer, http.StatusOK, page)
}

func (h *Handler) incidentList(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	query, err := parseListQuery(request.URL.Query(), 200)
	if err != nil {
		h.writeError(writer, http.StatusBadRequest, "INVALID_INCIDENT_QUERY", l10n.M("api.incident.query_invalid"), locale, fallback)
		return
	}
	items := []incident.Incident{}
	if h.incidents != nil {
		items = h.incidents.List()
	}
	writeJSON(writer, http.StatusOK, queryIncidents(items, query))
}

func (h *Handler) notificationStatus(writer http.ResponseWriter, locale, fallback string) {
	if h.notifier == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", l10n.M("api.notifications.unavailable"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, h.notifier.Snapshot())
}

func (h *Handler) notificationDeliveries(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	if h.notifier == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", l10n.M("api.notifications.unavailable"), locale, fallback)
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			h.writeError(writer, http.StatusBadRequest, "INVALID_NOTIFICATION_LIMIT", l10n.M("api.notifications.limit_invalid"), locale, fallback)
			return
		}
		limit = parsed
	}
	writeJSON(writer, http.StatusOK, h.notifier.Deliveries(limit))
}

func (h *Handler) testNotification(writer http.ResponseWriter, locale, fallback string) {
	if h.notifier == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", l10n.M("api.notifications.unavailable"), locale, fallback)
		return
	}
	if err := h.notifier.Test(); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, notify.ErrWebhookNotConfigured) {
			status = http.StatusConflict
		}
		h.writeError(writer, status, "NOTIFICATION_TEST_REJECTED", l10n.M("api.notifications.test_rejected"), locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "notification.test_queued", Outcome: "succeeded"})
	writeJSON(writer, http.StatusAccepted, map[string]bool{"queued": true})
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

func decodeSmallJSON(request *http.Request, destination any) bool {
	if request.Body == nil || request.Body == http.NoBody {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
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
