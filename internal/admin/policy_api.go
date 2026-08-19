package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	trafficpolicy "github.com/areasong/relay-lifeline/internal/policy"
)

func (h *Handler) policyList(writer http.ResponseWriter) {
	state := h.store.State()
	writeJSON(writer, http.StatusOK, map[string]any{"policy": state.Desired.TrafficPolicy, "revision": state.DesiredRevision})
}

func (h *Handler) policyGet(writer http.ResponseWriter, id, locale, fallback string) {
	for _, rule := range h.store.Desired().TrafficPolicy.Rules {
		if rule.ID == id {
			writeJSON(writer, http.StatusOK, rule)
			return
		}
	}
	h.writeError(writer, http.StatusNotFound, "POLICY_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
}

func (h *Handler) policySave(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	// Traffic policy changes must carry a draft/release journal entry. Keeping
	// this legacy endpoint read-only prevents a global config save from
	// hot-applying an unreviewed route or deny rule.
	h.writeError(writer, http.StatusPreconditionRequired, "POLICY_RELEASE_REQUIRED", l10n.M("api.policy.release_required"), locale, fallback)
}

func (h *Handler) policyDraft(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	h.policyMu.Lock()
	defer h.policyMu.Unlock()
	var input struct {
		Policy        config.TrafficPolicyConfig `json:"policy"`
		DraftRevision string                     `json:"draftRevision"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		h.writeError(writer, http.StatusBadRequest, "INVALID_POLICY_DRAFT", l10n.M("api.config.invalid_json"), locale, fallback)
		return
	}
	next := h.store.Desired()
	next.TrafficPolicy = input.Policy
	if err := next.Validate(); err != nil {
		h.writeConfigError(writer, err, locale, fallback)
		return
	}
	revision, err := h.policyReleases.SaveDraft(input.Policy, input.DraftRevision)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, trafficpolicy.ErrReleaseConflict) {
			status = http.StatusConflict
		}
		h.writeError(writer, status, "POLICY_DRAFT_REJECTED", l10n.M("api.config.revision_conflict"), locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.draft_saved", Outcome: "succeeded", Details: map[string]any{"revision": revision, "rules": len(input.Policy.Rules)}})
	writeJSON(writer, http.StatusOK, map[string]any{"saved": true, "draftRevision": revision})
}

func (h *Handler) policyReleaseStatus(writer http.ResponseWriter) {
	status := h.policyReleases.Status(h.store.Desired().TrafficPolicy)
	if status.History == nil {
		status.History = make([]trafficpolicy.ReleaseRecord, 0)
	}
	writeJSON(writer, http.StatusOK, status)
}

func (h *Handler) policyPublish(writer http.ResponseWriter, request *http.Request, locale, fallback string, role Role) {
	h.policyMu.Lock()
	defer h.policyMu.Unlock()
	var input struct {
		ConfigRevision string                      `json:"configRevision"`
		DraftRevision  string                      `json:"draftRevision"`
		Stage          string                      `json:"stage"`
		CanaryPercent  int                         `json:"canaryPercent"`
		Policy         *config.TrafficPolicyConfig `json:"policy,omitempty"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.ConfigRevision == "" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_POLICY_RELEASE", l10n.M("api.config.invalid_json"), locale, fallback)
		return
	}
	state := h.store.State()
	if input.ConfigRevision != state.DesiredRevision {
		h.writeError(writer, http.StatusConflict, "POLICY_REVISION_CONFLICT", l10n.M("api.config.revision_conflict"), locale, fallback)
		return
	}
	previous := state.Desired
	if input.Policy == nil && input.DraftRevision != "" {
		status := h.policyReleases.Status(state.Desired.TrafficPolicy)
		if status.DraftRevision != input.DraftRevision {
			h.writeError(writer, http.StatusConflict, "POLICY_DRAFT_CHANGED", l10n.M("api.config.revision_conflict"), locale, fallback)
			return
		}
	}
	policy, record, err := h.policyReleases.PreparePublish(input.Policy, input.Stage, input.CanaryPercent, string(role))
	if err != nil {
		h.writeError(writer, http.StatusBadRequest, "POLICY_RELEASE_REJECTED", l10n.M("api.config.invalid_json"), locale, fallback)
		return
	}
	next := state.Desired
	next.TrafficPolicy = policy
	if err := next.Validate(); err != nil {
		h.writeConfigError(writer, err, locale, fallback)
		return
	}
	if err := h.policyReleases.PrepareCommit(record, false); err != nil {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.release_prepare_failed", Outcome: "failed", Details: map[string]any{"revision": record.Revision}})
		h.writeError(writer, http.StatusServiceUnavailable, "POLICY_RELEASE_AUDIT_FAILED", l10n.M("api.persistence.unavailable"), locale, fallback)
		return
	}
	result, err := h.store.UpdateWithResultIfRevision(next, true, input.ConfigRevision)
	if err != nil {
		_ = h.policyReleases.AbortCommit(record, false, "config_update_failed")
		if errors.Is(err, config.ErrRevisionConflict) {
			h.writeError(writer, http.StatusConflict, "POLICY_REVISION_CONFLICT", l10n.M("api.config.revision_conflict"), locale, fallback)
			return
		}
		h.writeConfigError(writer, err, locale, fallback)
		return
	}
	if err := h.policyReleases.FinalizeCommit(record, false); err != nil {
		if _, rollbackErr := h.store.UpdateWithResultIfRevision(previous, true, result.DesiredRevision); rollbackErr == nil {
			_ = h.policyReleases.AbortCommit(record, false, "audit_failed_compensated")
		} else {
			h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.release_compensation_failed", Outcome: "failed", Details: map[string]any{"revision": record.Revision, "error": rollbackErr.Error()}})
		}
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.release_audit_failed", Outcome: "failed", Details: map[string]any{"revision": record.Revision}})
		h.writeError(writer, http.StatusInternalServerError, "POLICY_RELEASE_AUDIT_FAILED", l10n.M("api.persistence.unavailable"), locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.published", Outcome: "succeeded", Details: map[string]any{"policyRevision": record.Revision, "stage": record.Stage, "canaryPercent": record.CanaryPercent}})
	writeJSON(writer, http.StatusOK, map[string]any{"published": true, "release": record, "configRevision": result.DesiredRevision})
}

func (h *Handler) policyRollback(writer http.ResponseWriter, request *http.Request, locale, fallback string, role Role) {
	h.policyMu.Lock()
	defer h.policyMu.Unlock()
	var input struct {
		ConfigRevision string `json:"configRevision"`
		PolicyRevision string `json:"policyRevision"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.ConfigRevision == "" || input.PolicyRevision == "" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_POLICY_ROLLBACK", l10n.M("api.config.invalid_json"), locale, fallback)
		return
	}
	state := h.store.State()
	if state.DesiredRevision != input.ConfigRevision {
		h.writeError(writer, http.StatusConflict, "POLICY_REVISION_CONFLICT", l10n.M("api.config.revision_conflict"), locale, fallback)
		return
	}
	previous := state.Desired
	record, found := h.policyReleases.FindRevision(input.PolicyRevision)
	if !found {
		h.writeError(writer, http.StatusNotFound, "POLICY_RELEASE_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
		return
	}
	policy, rollback, err := h.policyReleases.PrepareRollback(record, string(role))
	if err != nil {
		h.writeError(writer, http.StatusBadRequest, "POLICY_ROLLBACK_REJECTED", l10n.M("api.config.invalid_json"), locale, fallback)
		return
	}
	next := state.Desired
	next.TrafficPolicy = policy
	if err := h.policyReleases.PrepareCommit(rollback, true); err != nil {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.rollback_prepare_failed", Outcome: "failed", Details: map[string]any{"revision": rollback.Revision}})
		h.writeError(writer, http.StatusServiceUnavailable, "POLICY_ROLLBACK_AUDIT_FAILED", l10n.M("api.persistence.unavailable"), locale, fallback)
		return
	}
	result, err := h.store.UpdateWithResultIfRevision(next, true, input.ConfigRevision)
	if err != nil {
		_ = h.policyReleases.AbortCommit(rollback, true, "config_update_failed")
		if errors.Is(err, config.ErrRevisionConflict) {
			h.writeError(writer, http.StatusConflict, "POLICY_REVISION_CONFLICT", l10n.M("api.config.revision_conflict"), locale, fallback)
			return
		}
		h.writeConfigError(writer, err, locale, fallback)
		return
	}
	if err := h.policyReleases.FinalizeCommit(rollback, true); err != nil {
		if _, rollbackErr := h.store.UpdateWithResultIfRevision(previous, true, result.DesiredRevision); rollbackErr == nil {
			_ = h.policyReleases.AbortCommit(rollback, true, "audit_failed_compensated")
		} else {
			h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.rollback_compensation_failed", Outcome: "failed", Details: map[string]any{"revision": rollback.Revision, "error": rollbackErr.Error()}})
		}
		h.writeError(writer, http.StatusInternalServerError, "POLICY_ROLLBACK_AUDIT_FAILED", l10n.M("api.persistence.unavailable"), locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.rolled_back", Outcome: "succeeded", Details: map[string]any{"from": input.PolicyRevision, "to": rollback.Revision}})
	writeJSON(writer, http.StatusOK, map[string]any{"rolledBack": true, "release": rollback, "configRevision": result.DesiredRevision})
}

func (h *Handler) policyRuntimeStatus(writer http.ResponseWriter, request *http.Request) {
	limit := policyLimit(request)
	if h.policyStatus == nil {
		writeJSON(writer, http.StatusOK, trafficpolicy.Status{Recent: make([]trafficpolicy.Decision, 0)})
		return
	}
	status := h.policyStatus(limit)
	if status.Recent == nil {
		status.Recent = make([]trafficpolicy.Decision, 0)
	}
	writeJSON(writer, http.StatusOK, status)
}

func (h *Handler) policyDecisions(writer http.ResponseWriter, request *http.Request) {
	if h.policyStatus == nil {
		writeJSON(writer, http.StatusOK, []trafficpolicy.Decision{})
		return
	}
	writeJSON(writer, http.StatusOK, h.policyStatus(policyLimit(request)).Recent)
}

func (h *Handler) policySimulation(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	useDraft := request.URL.Query().Get("source") == "draft"
	if h.policySimulate == nil && !useDraft {
		h.writeError(writer, http.StatusServiceUnavailable, "POLICY_UNAVAILABLE", l10n.M("api.monitoring.unavailable"), locale, fallback)
		return
	}
	input, ok := decodePolicyInput(writer, request, h, locale, fallback)
	if !ok {
		return
	}
	if useDraft {
		candidate, err := h.policyReleases.Candidate(nil)
		if err != nil {
			h.writeError(writer, http.StatusNotFound, "POLICY_DRAFT_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
			return
		}
		writeJSON(writer, http.StatusOK, trafficpolicy.New(candidate).Evaluate(input, true))
		return
	}
	writeJSON(writer, http.StatusOK, h.policySimulate(input))
}

func (h *Handler) policyReplay(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	if h.policySimulate == nil {
		h.writeError(writer, http.StatusServiceUnavailable, "POLICY_UNAVAILABLE", l10n.M("api.monitoring.unavailable"), locale, fallback)
		return
	}
	var input struct {
		CaptureID string              `json:"captureId"`
		Request   trafficpolicy.Input `json:"request"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REPLAY", l10n.M("api.request.read_failed"), locale, fallback)
		return
	}
	source := "input"
	if input.CaptureID != "" {
		if h.captures == nil {
			h.writeError(writer, http.StatusServiceUnavailable, "CAPTURE_UNAVAILABLE", l10n.M("api.monitoring.unavailable"), locale, fallback)
			return
		}
		record, found := h.captures.Get(input.CaptureID)
		if !found {
			h.writeError(writer, http.StatusNotFound, "CAPTURE_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
			return
		}
		input.Request = trafficpolicy.Input{Method: record.Method, Path: record.Path, Model: "unknown", RequestID: record.RequestID, BodyBytes: record.Request.OriginalBytes, SLOHealthy: true, ErrorBudgetRemaining: 1}
		source = "capture_metadata"
	}
	decision := h.policySimulate(input.Request)
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "traffic_policy.replay_dry_run", Outcome: "succeeded", Details: map[string]any{"source": source, "captureId": input.CaptureID}})
	writeJSON(writer, http.StatusOK, map[string]any{"dryRun": true, "executed": false, "source": source, "containsRawBody": false, "decision": decision})
}

func decodePolicyInput(writer http.ResponseWriter, request *http.Request, h *Handler, locale, fallback string) (trafficpolicy.Input, bool) {
	var input trafficpolicy.Input
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Method == "" || input.Path == "" || !strings.HasPrefix(input.Path, "/") || len(input.Path) > 2048 || len(input.Model) > 256 || len(input.Principal) > 256 {
		h.writeError(writer, http.StatusBadRequest, "INVALID_POLICY_INPUT", l10n.M("api.request.read_failed"), locale, fallback)
		return trafficpolicy.Input{}, false
	}
	input.SLOHealthy = true
	if input.ErrorBudgetRemaining == 0 {
		input.ErrorBudgetRemaining = 1
	}
	return input, true
}

func policyLimit(request *http.Request) int {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		return 50
	}
	return limit
}
