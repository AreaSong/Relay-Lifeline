package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/l10n"
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
	adminKey    string
	risk        *risk.Manager
	diagnostics *diagnostics.Service
	notifier    *notify.Notifier
	captures    *capture.Manager
	runLogs     *runlog.Store
}

func New(store *config.Store, registry *state.Registry, controller *state.Controller) *Handler {
	return NewWithServices(store, registry, controller, risk.New(), diagnostics.New(store, "dev", time.Now()), nil)
}

func NewWithServices(store *config.Store, registry *state.Registry, controller *state.Controller, riskManager *risk.Manager, diagnosticService *diagnostics.Service, notifier *notify.Notifier) *Handler {
	return NewWithExtendedServices(store, registry, controller, riskManager, diagnosticService, notifier, nil, nil)
}

func NewWithExtendedServices(store *config.Store, registry *state.Registry, controller *state.Controller, riskManager *risk.Manager, diagnosticService *diagnostics.Service, notifier *notify.Notifier, captures *capture.Manager, runLogs *runlog.Store) *Handler {
	return &Handler{
		store: store, registry: registry, controller: controller, adminKey: os.Getenv("RELAY_LIFELINE_ADMIN_KEY"),
		risk: riskManager, diagnostics: diagnosticService, notifier: notifier, captures: captures, runLogs: runLogs,
	}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	locale, fallback := h.requestLocales(request)
	writer.Header().Set("Content-Language", locale)
	if !h.authorized(request) {
		h.writeError(writer, http.StatusUnauthorized, "INVALID_ADMIN_KEY", l10n.M("api.admin.invalid_key"), locale, fallback)
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/admin/api")
	switch {
	case request.Method == http.MethodGet && path == "/session":
		writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true})
	case request.Method == http.MethodGet && path == "/status":
		writeJSON(writer, http.StatusOK, h.registry.LocalizedSnapshot(h.controller.IsPaused(), locale, fallback))
	case request.Method == http.MethodGet && path == "/config":
		writeJSON(writer, http.StatusOK, h.store.Get())
	case request.Method == http.MethodGet && path == "/alerts":
		writeJSON(writer, http.StatusOK, risk.Localize(h.risk.Recent(100), locale, fallback))
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
			h.writeConfigError(writer, err, locale, fallback)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"reloaded": true})
	case request.Method == http.MethodPost && path == "/control/pause":
		writeJSON(writer, http.StatusOK, map[string]bool{"changed": h.controller.Pause(), "paused": true})
	case request.Method == http.MethodPost && path == "/control/resume":
		writeJSON(writer, http.StatusOK, map[string]bool{"changed": h.controller.Resume(), "paused": false})
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

type captureActivation struct {
	RequestLimit      int             `json:"requestLimit"`
	ActivationTimeout config.Duration `json:"activationTimeout"`
}

func (h *Handler) runtimeLogs(writer http.ResponseWriter, request *http.Request) {
	if h.runLogs == nil {
		writeJSON(writer, http.StatusOK, []runlog.Entry{})
		return
	}
	after, _ := strconv.ParseUint(request.URL.Query().Get("after"), 10, 64)
	writeJSON(writer, http.StatusOK, h.runLogs.List(after, request.URL.Query().Get("level"), request.URL.Query().Get("event"), request.URL.Query().Get("requestId")))
}

func (h *Handler) exportRuntimeLogs(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Disposition", "attachment; filename=relay-lifeline-runtime-logs.json")
	h.runtimeLogs(writer, request)
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

type diagnosticBundle struct {
	GeneratedAt time.Time          `json:"generatedAt"`
	Report      diagnostics.Report `json:"report"`
	Config      config.Config      `json:"config"`
	Status      state.Snapshot     `json:"status"`
	History     []timeline.Record  `json:"history"`
	Alerts      []risk.Alert       `json:"alerts"`
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
	writer.Header().Set("Content-Disposition", "attachment; filename=relay-lifeline-diagnostics.json")
	writeJSON(writer, http.StatusOK, diagnosticBundle{
		GeneratedAt: time.Now(), Report: report, Config: diagnostics.RedactedConfig(h.store.Get()),
		Status: h.registry.LocalizedSnapshot(h.controller.IsPaused(), locale, fallback), History: timeline.LocalizeRecords(timeline.WithoutErrorDetails(history), locale, fallback), Alerts: risk.Localize(h.risk.Recent(50), locale, fallback),
	})
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

func (h *Handler) authorized(request *http.Request) bool {
	if h.adminKey == "" {
		return false
	}
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	if len(provided) != len(h.adminKey) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(h.adminKey)) == 1
}

func (h *Handler) updateConfig(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	reader := io.LimitReader(request.Body, 1<<20)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var cfg config.Config
	if err := decoder.Decode(&cfg); err != nil {
		h.writeError(writer, http.StatusBadRequest, "INVALID_CONFIG_JSON", l10n.M("api.config.invalid_json"), locale, fallback)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		h.writeError(writer, http.StatusBadRequest, "TRAILING_CONFIG_JSON", l10n.M("api.config.trailing_json"), locale, fallback)
		return
	}
	before := h.store.Get()
	if err := h.store.Update(cfg, true); err != nil {
		h.writeConfigError(writer, err, locale, fallback)
		return
	}
	restartRequired := before.Server.Listen != cfg.Server.Listen || before.Server.AdminEnabled != cfg.Server.AdminEnabled || before.Upstream != cfg.Upstream || before.Server.ReadHeaderTimeout != cfg.Server.ReadHeaderTimeout || before.Server.ShutdownTimeout != cfg.Server.ShutdownTimeout || before.Logging.Level != cfg.Logging.Level || before.Capture.StorageDir != cfg.Capture.StorageDir
	writeJSON(writer, http.StatusOK, map[string]bool{"saved": true, "restartRequired": restartRequired})
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
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("X-Frame-Options", "DENY")
	headers.Set("Referrer-Policy", "no-referrer")
}
