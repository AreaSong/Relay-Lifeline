package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/repeat"
)

func (h *Handler) createRepeatTask(writer http.ResponseWriter, request *http.Request, id, locale, fallback string) {
	if h.repeater == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "REPEAT_UNAVAILABLE", l10n.M("api.repeat.unavailable"), locale, fallback)
		return
	}
	var input struct {
		Interval         string `json:"interval"`
		Duration         string `json:"duration"`
		Idempotency      string `json:"idempotency"`
		ConfirmForever   bool   `json:"confirmForever"`
		MaxExecutions    int    `json:"maxExecutions"`
		MaxFailures      int    `json:"maxFailures"`
		FailureThreshold int    `json:"failureThreshold"`
		MaxTokens        int64  `json:"maxTokens"`
	}
	if !decodeSmallJSON(request, &input) {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REPEAT_TASK", l10n.M("api.repeat.invalid_input"), locale, fallback)
		return
	}
	interval, intervalErr := time.ParseDuration(input.Interval)
	duration, durationErr := time.ParseDuration(input.Duration)
	if input.Duration == "" {
		duration, durationErr = 0, nil
	}
	if intervalErr != nil || durationErr != nil {
		h.writeRepeatError(writer, repeat.ErrInvalidInput, locale, fallback)
		return
	}
	task, err := h.repeater.Create(id, repeat.CreateInput{
		Interval: interval, Duration: duration, Idempotency: input.Idempotency, ConfirmForever: input.ConfirmForever,
		MaxExecutions: input.MaxExecutions, MaxFailures: input.MaxFailures, FailureThreshold: input.FailureThreshold,
		MaxTokens: input.MaxTokens,
	})
	if err != nil {
		h.writeRepeatError(writer, err, locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "repeat.task_created", Outcome: "succeeded", RequestID: id})
	writeJSON(writer, http.StatusCreated, task)
}

func (h *Handler) repeatTaskAction(writer http.ResponseWriter, request *http.Request, path, locale, fallback string) {
	if h.repeater == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "REPEAT_UNAVAILABLE", l10n.M("api.repeat.unavailable"), locale, fallback)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		h.writeError(writer, http.StatusNotFound, "REPEAT_TASK_NOT_FOUND", l10n.M("api.repeat.not_found"), locale, fallback)
		return
	}
	var task repeat.Task
	var err error
	switch {
	case request.Method == http.MethodDelete && len(parts) == 1:
		task, err = h.repeater.Stop(parts[0])
	case request.Method == http.MethodPost && len(parts) == 2 && parts[1] == "pause":
		task, err = h.repeater.Pause(parts[0])
	case request.Method == http.MethodPost && len(parts) == 2 && parts[1] == "resume":
		task, err = h.repeater.Resume(parts[0])
	case request.Method == http.MethodPost && len(parts) == 2 && parts[1] == "run":
		task, err = h.repeater.RunNow(parts[0])
	default:
		h.writeError(writer, http.StatusNotFound, "ENDPOINT_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
		return
	}
	if err != nil {
		h.writeRepeatError(writer, err, locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "repeat.task_updated", Outcome: "succeeded", RequestID: task.SourceRequestID})
	writeJSON(writer, http.StatusOK, task)
}

func (h *Handler) writeRepeatError(writer http.ResponseWriter, err error, locale, fallback string) {
	switch {
	case errors.Is(err, repeat.ErrSourceNotFound), errors.Is(err, repeat.ErrTaskNotFound):
		h.writeError(writer, http.StatusNotFound, "REPEAT_TASK_NOT_FOUND", l10n.M("api.repeat.not_found"), locale, fallback)
	case errors.Is(err, repeat.ErrTaskExists):
		h.writeError(writer, http.StatusConflict, "REPEAT_TASK_EXISTS", l10n.M("api.repeat.exists"), locale, fallback)
	default:
		h.writeError(writer, http.StatusBadRequest, "INVALID_REPEAT_TASK", l10n.M("api.repeat.invalid_input"), locale, fallback)
	}
}
