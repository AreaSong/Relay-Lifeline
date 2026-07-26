package admin

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/risk"
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
}

func New(store *config.Store, registry *state.Registry, controller *state.Controller) *Handler {
	return NewWithServices(store, registry, controller, risk.New(), diagnostics.New(store, "dev", time.Now()), nil)
}

func NewWithServices(store *config.Store, registry *state.Registry, controller *state.Controller, riskManager *risk.Manager, diagnosticService *diagnostics.Service, notifier *notify.Notifier) *Handler {
	return &Handler{
		store: store, registry: registry, controller: controller, adminKey: os.Getenv("RELAY_LIFELINE_ADMIN_KEY"),
		risk: riskManager, diagnostics: diagnosticService, notifier: notifier,
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
	restartRequired := before.Server.Listen != cfg.Server.Listen || before.Server.AdminEnabled != cfg.Server.AdminEnabled || before.Upstream != cfg.Upstream || before.Server.ReadHeaderTimeout != cfg.Server.ReadHeaderTimeout || before.Server.ShutdownTimeout != cfg.Server.ShutdownTimeout || before.Logging.Level != cfg.Logging.Level
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
