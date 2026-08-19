package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

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
	filter := runlog.Filter{Level: query.Get("level"), Event: query.Get("event"), RequestID: query.Get("requestId"), Search: query.Get("q")}
	if query.Get("since") != "" {
		filter.Since, _ = time.Parse(time.RFC3339, query.Get("since"))
	}
	if query.Get("tail") == "true" {
		writeJSON(writer, http.StatusOK, h.runLogs.QueryTail(limit, filter))
		return
	}
	writeJSON(writer, http.StatusOK, h.runLogs.QueryPage(after, limit, filter))
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
	if len(query.Get("event")) > 128 || len(query.Get("requestId")) > 128 || len(query.Get("q")) > 128 {
		h.writeError(writer, http.StatusBadRequest, "INVALID_LOG_FILTER", l10n.M("api.logs.filter_invalid"), locale, fallback)
		return 0, 0, false
	}
	if raw := query.Get("since"); raw != "" {
		if _, err := time.Parse(time.RFC3339, raw); err != nil {
			h.writeError(writer, http.StatusBadRequest, "INVALID_LOG_FILTER", l10n.M("api.logs.filter_invalid"), locale, fallback)
			return 0, 0, false
		}
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

func (h *Handler) requestTimeline(writer http.ResponseWriter, path, locale, fallback string) {
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/timeline")
	record, ok := h.registry.Timeline(id)
	if !ok {
		h.writeError(writer, http.StatusNotFound, "REQUEST_NOT_FOUND", l10n.M("api.request.not_found"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, timeline.LocalizeRecord(record, locale, fallback))
}
