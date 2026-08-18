package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/state"
)

type retrySchedulePayload struct {
	Mode                        string `json:"mode"`
	IntervalMilliseconds        *int64 `json:"intervalMilliseconds"`
	MinimumIntervalMilliseconds *int64 `json:"minimumIntervalMilliseconds"`
	MaximumIntervalMilliseconds *int64 `json:"maximumIntervalMilliseconds"`
	BaseIntervalMilliseconds    *int64 `json:"baseIntervalMilliseconds"`
}

type retryPolicyPayload struct {
	DurationMilliseconds  *int64                `json:"durationMilliseconds"`
	Duration              string                `json:"duration"`
	IntervalMilliseconds  *int64                `json:"intervalMilliseconds"`
	Interval              string                `json:"interval"`
	Schedule              *retrySchedulePayload `json:"schedule"`
	MaxAdditionalAttempts *int                  `json:"maxAdditionalAttempts"`
	HonorRetryAfter       *bool                 `json:"honorRetryAfter"`
	Overwrite             *bool                 `json:"overwrite"`
}

type batchRetryPayload struct {
	RequestIDs     []string `json:"requestIds"`
	AllowUncertain bool     `json:"allowUncertain"`
}

type batchPolicyPayload struct {
	RequestIDs      []string            `json:"requestIds"`
	Policy          *retryPolicyPayload `json:"policy"`
	Reset           bool                `json:"reset"`
	Overwrite       *bool               `json:"overwrite"`
	RetryWaitingNow bool                `json:"retryWaitingNow"`
}

type batchActionResponse struct {
	OperationID string                      `json:"operationId"`
	Requested   int                         `json:"requested"`
	Accepted    int                         `json:"accepted"`
	Skipped     int                         `json:"skipped"`
	Triggered   int                         `json:"triggered,omitempty"`
	Results     []state.RequestActionResult `json:"results"`
}

func (h *Handler) retryRequest(writer http.ResponseWriter, request *http.Request, id, locale, fallback string) {
	input := struct {
		AllowUncertain bool `json:"allowUncertain"`
	}{}
	if !decodeOptionalJSON(request, &input) {
		h.writeError(writer, http.StatusBadRequest, "INVALID_RETRY_REQUEST", l10n.M("api.request.batch_invalid"), locale, fallback)
		return
	}
	result := h.registry.RetryNowChecked(id, input.AllowUncertain)
	if result.Outcome != state.RequestActionAccepted {
		if result.Reason == state.RequestReasonAlreadyRequested {
			writeJSON(writer, http.StatusOK, map[string]any{"accepted": false, "result": result})
			return
		}
		h.writeRequestActionError(writer, result, locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "request.retry", Outcome: "succeeded", RequestID: id})
	writeJSON(writer, http.StatusOK, map[string]any{"accepted": true, "result": result})
}

func decodeOptionalJSON(request *http.Request, destination any) bool {
	if request.Body == nil || request.Body == http.NoBody || request.ContentLength == 0 {
		return true
	}
	return decodeSmallJSON(request, destination)
}

func (h *Handler) setRetryPolicy(writer http.ResponseWriter, request *http.Request, id, locale, fallback string) {
	var payload retryPolicyPayload
	if !decodeSmallJSON(request, &payload) {
		h.writeError(writer, http.StatusBadRequest, "INVALID_RETRY_POLICY", l10n.M("api.request.retry_policy_invalid"), locale, fallback)
		return
	}
	spec, err := payload.spec()
	if err != nil {
		h.writeError(writer, http.StatusBadRequest, "INVALID_RETRY_POLICY", l10n.M("api.request.retry_policy_invalid"), locale, fallback)
		return
	}
	overwrite := true
	if payload.Overwrite != nil {
		overwrite = *payload.Overwrite
	}
	result := h.registry.ApplyRetryPolicy(id, spec, overwrite)
	if result.Outcome != state.RequestActionAccepted {
		h.writeRequestActionError(writer, result, locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "request.retry_policy", Outcome: "succeeded", RequestID: id, Details: map[string]any{"mode": string(spec.Schedule.Mode)}})
	if h.runLogs != nil {
		h.runLogs.Add(runlog.Entry{Level: "info", Event: "request.retry_policy", Message: "已更新请求重试策略", RequestID: id, Fields: map[string]any{"mode": string(spec.Schedule.Mode)}})
	}
	policy, _ := h.registry.RetryPolicy(id)
	writeJSON(writer, http.StatusOK, map[string]any{
		"accepted": true, "retryDeadline": policy.Deadline,
		"retryIntervalMilliseconds": policy.Interval.Milliseconds(), "retryPolicy": policy.Info(0),
	})
}

func (h *Handler) clearRetryPolicy(writer http.ResponseWriter, request *http.Request, id, locale, fallback string) {
	result := h.registry.ClearRetryPolicy(id)
	if result.Outcome != state.RequestActionAccepted {
		h.writeRequestActionError(writer, result, locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "request.retry_policy_reset", Outcome: "succeeded", RequestID: id})
	writeJSON(writer, http.StatusOK, map[string]bool{"accepted": true})
}

func (h *Handler) batchRetry(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	var payload batchRetryPayload
	if !decodeBatchJSON(request, &payload) {
		h.writeError(writer, http.StatusBadRequest, "INVALID_BATCH_REQUEST", l10n.M("api.request.batch_invalid"), locale, fallback)
		return
	}
	ids, ok := normalizeRequestIDs(payload.RequestIDs)
	if !ok {
		h.writeError(writer, http.StatusBadRequest, "INVALID_BATCH_REQUEST", l10n.M("api.request.batch_invalid"), locale, fallback)
		return
	}
	operation := newOperationID("retry")
	response := batchActionResponse{OperationID: operation, Requested: len(ids), Results: make([]state.RequestActionResult, 0, len(ids))}
	for _, id := range ids {
		result := h.registry.RetryNowChecked(id, payload.AllowUncertain)
		response.Results = append(response.Results, result)
		if result.Outcome == state.RequestActionAccepted {
			response.Accepted++
		} else {
			response.Skipped++
		}
	}
	h.recordBatchAction("request.retry_batch", operation, response)
	writeJSON(writer, http.StatusOK, response)
}

func (h *Handler) batchRetryPolicy(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	var payload batchPolicyPayload
	if !decodeBatchJSON(request, &payload) {
		h.writeError(writer, http.StatusBadRequest, "INVALID_BATCH_REQUEST", l10n.M("api.request.batch_invalid"), locale, fallback)
		return
	}
	ids, ok := normalizeRequestIDs(payload.RequestIDs)
	if !ok || payload.Reset && payload.Policy != nil || !payload.Reset && payload.Policy == nil {
		h.writeError(writer, http.StatusBadRequest, "INVALID_BATCH_REQUEST", l10n.M("api.request.batch_invalid"), locale, fallback)
		return
	}
	var spec state.RetryPolicySpec
	if !payload.Reset {
		var err error
		spec, err = payload.Policy.spec()
		if err != nil {
			h.writeError(writer, http.StatusBadRequest, "INVALID_RETRY_POLICY", l10n.M("api.request.retry_policy_invalid"), locale, fallback)
			return
		}
	}
	overwrite := true
	if payload.Overwrite != nil {
		overwrite = *payload.Overwrite
	}
	operation := newOperationID("policy")
	response := batchActionResponse{OperationID: operation, Requested: len(ids), Results: make([]state.RequestActionResult, 0, len(ids))}
	for _, id := range ids {
		var result state.RequestActionResult
		if payload.Reset {
			result = h.registry.ClearRetryPolicy(id)
		} else {
			result = h.registry.ApplyRetryPolicy(id, spec, overwrite)
		}
		response.Results = append(response.Results, result)
		if result.Outcome == state.RequestActionAccepted {
			response.Accepted++
			if payload.RetryWaitingNow && result.State == "waiting" {
				if h.registry.RetryNowChecked(id, false).Outcome == state.RequestActionAccepted {
					response.Triggered++
				}
			}
		} else {
			response.Skipped++
		}
	}
	h.recordBatchAction("request.retry_policy_batch", operation, response)
	writeJSON(writer, http.StatusOK, response)
}

func (p retryPolicyPayload) spec() (state.RetryPolicySpec, error) {
	duration, err := parseDurationInput(p.DurationMilliseconds, p.Duration)
	if err != nil {
		return state.RetryPolicySpec{}, state.ErrInvalidRetryPolicy
	}
	maxAttempts := 0
	if p.MaxAdditionalAttempts != nil {
		maxAttempts = *p.MaxAdditionalAttempts
	}
	honorRetryAfter := true
	if p.HonorRetryAfter != nil {
		honorRetryAfter = *p.HonorRetryAfter
	}
	spec := state.RetryPolicySpec{Duration: duration, MaxAdditionalAttempts: maxAttempts, HonorRetryAfter: honorRetryAfter}
	if p.Schedule == nil {
		interval, intervalErr := parseDurationInput(p.IntervalMilliseconds, p.Interval)
		if intervalErr != nil {
			return state.RetryPolicySpec{}, state.ErrInvalidRetryPolicy
		}
		spec.Schedule = state.RetrySchedule{Mode: state.RetryScheduleFixed, Interval: interval}
		return spec, spec.Validate()
	}
	schedule := p.Schedule
	spec.Schedule.Mode = state.RetryScheduleMode(schedule.Mode)
	spec.Schedule.Interval, _ = parseDurationInput(schedule.IntervalMilliseconds, "")
	spec.Schedule.Minimum, _ = parseDurationInput(schedule.MinimumIntervalMilliseconds, "")
	spec.Schedule.Maximum, _ = parseDurationInput(schedule.MaximumIntervalMilliseconds, "")
	spec.Schedule.Base, _ = parseDurationInput(schedule.BaseIntervalMilliseconds, "")
	return spec, spec.Validate()
}

func parseDurationInput(milliseconds *int64, text string) (time.Duration, error) {
	if milliseconds != nil {
		if *milliseconds <= 0 || *milliseconds > state.MaximumRetryPolicyDuration.Milliseconds() {
			return 0, state.ErrInvalidRetryPolicy
		}
		return time.Duration(*milliseconds) * time.Millisecond, nil
	}
	if strings.TrimSpace(text) == "" {
		return 0, state.ErrInvalidRetryPolicy
	}
	return time.ParseDuration(strings.TrimSpace(text))
}

func normalizeRequestIDs(ids []string) ([]string, bool) {
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, false
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, len(result) > 0
}

func decodeBatchJSON(request *http.Request, destination any) bool {
	if request.Body == nil || request.Body == http.NoBody {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func newOperationID(prefix string) string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return prefix + "-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return prefix + "-" + hex.EncodeToString(buffer)
}

func (h *Handler) recordBatchAction(code, operation string, response batchActionResponse) {
	outcome := "skipped"
	if response.Accepted == response.Requested {
		outcome = "succeeded"
	} else if response.Accepted > 0 {
		outcome = "partial"
	}
	details := map[string]any{
		"operationId": operation, "requested": response.Requested,
		"accepted": response.Accepted, "skipped": response.Skipped,
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: code, Outcome: outcome, Details: details})
	if h.runLogs != nil {
		h.runLogs.Add(runlog.Entry{Level: "info", Event: code, Message: "批量请求操作已完成", Fields: details})
	}
}

func (h *Handler) writeRequestActionError(writer http.ResponseWriter, result state.RequestActionResult, locale, fallback string) {
	status := http.StatusConflict
	code := "REQUEST_ACTION_UNAVAILABLE"
	message := l10n.M("api.request.retry_unavailable")
	switch result.Reason {
	case state.RequestReasonNotFound:
		status, code, message = http.StatusNotFound, "REQUEST_NOT_FOUND", l10n.M("api.request.not_found")
	case state.RequestReasonConfirmationRequired:
		status, code, message = http.StatusPreconditionRequired, "RETRY_CONFIRMATION_REQUIRED", l10n.M("api.request.retry_confirmation_required")
	case state.RequestReasonPolicyExists:
		status, code = http.StatusConflict, "RETRY_POLICY_EXISTS"
	case state.RequestReasonNoPolicy:
		status, code = http.StatusNotFound, "RETRY_POLICY_NOT_FOUND"
	}
	h.writeError(writer, status, code, message, locale, fallback)
}
