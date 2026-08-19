package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

const uncertainConfirmationTTL = 2 * time.Minute

var (
	errUncertainConfirmationInvalid = errors.New("uncertain confirmation is invalid")
	errUncertainConfirmationExpired = errors.New("uncertain confirmation expired")
)

type uncertainConfirmation struct {
	RequestID string
	Action    string
	Actor     string
	ExpiresAt time.Time
}

type uncertainConfirmationStore struct {
	mu     sync.Mutex
	tokens map[string]uncertainConfirmation
	now    func() time.Time
}

func newUncertainConfirmationStore() *uncertainConfirmationStore {
	return &uncertainConfirmationStore{tokens: make(map[string]uncertainConfirmation), now: time.Now}
}

func (s *uncertainConfirmationStore) Issue(requestID, action, actor string) (string, time.Time, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", time.Time{}, err
	}
	now := s.now().UTC()
	expires := now.Add(uncertainConfirmationTTL)
	token := hex.EncodeToString(buffer)
	s.mu.Lock()
	for value, confirmation := range s.tokens {
		if !confirmation.ExpiresAt.After(now) {
			delete(s.tokens, value)
		}
	}
	s.tokens[token] = uncertainConfirmation{RequestID: requestID, Action: action, Actor: actor, ExpiresAt: expires}
	s.mu.Unlock()
	return token, expires, nil
}

func (s *uncertainConfirmationStore) Consume(token, requestID, action, actor string) error {
	now := s.now().UTC()
	s.mu.Lock()
	confirmation, ok := s.tokens[token]
	delete(s.tokens, token)
	s.mu.Unlock()
	if !ok || token == "" || confirmation.RequestID != requestID || confirmation.Action != action || confirmation.Actor != actor {
		return errUncertainConfirmationInvalid
	}
	if !confirmation.ExpiresAt.After(now) {
		return errUncertainConfirmationExpired
	}
	return nil
}

type uncertainResolutionPayload struct {
	Action            string `json:"action"`
	ConfirmationToken string `json:"confirmationToken,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

type uncertainAttemptEvidence struct {
	Attempt             int    `json:"attempt"`
	TargetID            string `json:"targetId,omitempty"`
	TargetDomain        string `json:"targetDomain,omitempty"`
	StatusCode          int    `json:"statusCode,omitempty"`
	Category            string `json:"category,omitempty"`
	AttemptPhase        string `json:"attemptPhase,omitempty"`
	WroteRequest        bool   `json:"wroteRequest"`
	IdempotencyKeyHash  string `json:"idempotencyKeyHash,omitempty"`
	RequestBytes        int64  `json:"requestBytes,omitempty"`
	LatencyMilliseconds int64  `json:"latencyMilliseconds,omitempty"`
	UpstreamRequestID   string `json:"upstreamRequestId,omitempty"`
}

type uncertainEvidence struct {
	RequestID      string                     `json:"requestId"`
	Method         string                     `json:"method"`
	Path           string                     `json:"path"`
	State          string                     `json:"state"`
	Attempt        int                        `json:"attempt"`
	StartedAt      time.Time                  `json:"startedAt"`
	UncertainSince time.Time                  `json:"uncertainSince"`
	Attempts       []uncertainAttemptEvidence `json:"attempts"`
}

func (h *Handler) previewUncertainResolution(writer http.ResponseWriter, request *http.Request, id, actor, locale, fallback string) {
	payload, ok := decodeUncertainPayload(request)
	if !ok || !validUncertainAction(payload.Action) {
		h.writeError(writer, http.StatusBadRequest, "INVALID_UNCERTAIN_ACTION", l10n.M("api.request.uncertain_action_invalid"), locale, fallback)
		return
	}
	info, exists := h.registry.RequestInfo(id)
	if !exists {
		h.writeError(writer, http.StatusNotFound, "REQUEST_NOT_FOUND", l10n.M("api.request.not_found"), locale, fallback)
		return
	}
	if info.State != lifecycle.StateUncertain || info.UncertainResolution != "" {
		h.writeError(writer, http.StatusConflict, "UNCERTAIN_STATE_CONFLICT", l10n.M("api.request.retry_unavailable"), locale, fallback)
		return
	}
	record, _ := h.registry.Timeline(id)
	token, expires, err := h.uncertainConfirm.Issue(id, payload.Action, actor)
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, "CONFIRMATION_UNAVAILABLE", l10n.M("api.persistence.unavailable"), locale, fallback)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"confirmationToken": token, "expiresAt": expires, "evidence": buildUncertainEvidence(info, record)})
}

func (h *Handler) resolveUncertain(writer http.ResponseWriter, request *http.Request, id, actor, locale, fallback string) {
	payload, ok := decodeUncertainPayload(request)
	if !ok || !validUncertainAction(payload.Action) || utf8.RuneCountInString(strings.TrimSpace(payload.Reason)) > 500 || strings.TrimSpace(payload.Reason) == "" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_UNCERTAIN_ACTION", l10n.M("api.request.uncertain_action_invalid"), locale, fallback)
		return
	}
	if err := h.uncertainConfirm.Consume(payload.ConfirmationToken, id, payload.Action, actor); err != nil {
		code := "UNCERTAIN_CONFIRMATION_INVALID"
		if errors.Is(err, errUncertainConfirmationExpired) {
			code = "UNCERTAIN_CONFIRMATION_EXPIRED"
		}
		message := l10n.M("api.request.uncertain_confirmation_invalid")
		if errors.Is(err, errUncertainConfirmationExpired) {
			message = l10n.M("api.request.uncertain_confirmation_expired")
		}
		h.writeError(writer, http.StatusConflict, code, message, locale, fallback)
		return
	}
	result := h.registry.ResolveUncertain(id, payload.Action, strings.TrimSpace(payload.Reason))
	if result.Outcome != state.RequestActionAccepted {
		h.writeRequestActionError(writer, result, locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "request.uncertain_" + payload.Action, Outcome: "succeeded", RequestID: id, Details: map[string]any{"reason": strings.TrimSpace(payload.Reason)}})
	if h.monitor != nil {
		h.monitor.RecordUncertainResolution(payload.Action)
		h.monitor.RecordEvent(monitoring.Event{Code: "request.uncertain_" + payload.Action, RequestID: id, Outcome: "succeeded"})
	}
	if h.notifier != nil {
		h.notifier.Send(notify.Event{Type: "uncertain_resolved", RequestID: id, MessageCode: "notify.uncertain_resolved", MessageDetails: map[string]any{"Action": payload.Action}})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"accepted": true, "action": payload.Action, "result": result})
}

func decodeUncertainPayload(request *http.Request) (uncertainResolutionPayload, bool) {
	var payload uncertainResolutionPayload
	decoder := json.NewDecoder(io.LimitReader(request.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return uncertainResolutionPayload{}, false
	}
	payload.Action = strings.TrimSpace(payload.Action)
	return payload, true
}

func validUncertainAction(action string) bool {
	return action == state.UncertainAbandon || action == state.UncertainConfirmSuccess || action == state.UncertainRequestCompensation
}

func buildUncertainEvidence(info state.RequestInfo, record timeline.Record) uncertainEvidence {
	evidence := uncertainEvidence{RequestID: info.ID, Method: info.Method, Path: info.Path, State: string(info.State), Attempt: info.Attempt, StartedAt: info.StartedAt, UncertainSince: info.UncertainSince, Attempts: make([]uncertainAttemptEvidence, 0)}
	for _, event := range record.Events {
		if event.Type != "attempt_failed" && event.Type != "uncertain" {
			continue
		}
		item := uncertainAttemptEvidence{Attempt: event.Attempt, TargetID: event.TargetID, TargetDomain: event.TargetDomain, StatusCode: event.StatusCode, Category: event.Category, AttemptPhase: event.AttemptPhase, WroteRequest: event.WroteRequest, IdempotencyKeyHash: event.IdempotencyKeyHash, RequestBytes: event.RequestBytes, LatencyMilliseconds: event.LatencyMilliseconds}
		if event.ErrorDetail != nil {
			item.UpstreamRequestID = event.ErrorDetail.UpstreamRequestID
		}
		evidence.Attempts = append(evidence.Attempts, item)
	}
	return evidence
}

func uncertainActor(authMethod, sessionToken, authorization string) string {
	value := sessionToken
	if value == "" {
		value = authorization
	}
	digest := sha256.Sum256([]byte(value))
	return authMethod + ":" + hex.EncodeToString(digest[:8])
}
