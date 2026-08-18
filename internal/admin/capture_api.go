package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

type captureActivation struct {
	RequestLimit      int             `json:"requestLimit"`
	ActivationTimeout config.Duration `json:"activationTimeout"`
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
