package state

import (
	"errors"
	"time"

	"github.com/areasong/relay-lifeline/internal/lifecycle"
)

const (
	MinimumRetryPolicyDuration = 5 * time.Second
	MaximumRetryPolicyDuration = 24 * time.Hour
	MaximumPolicyAttempts      = 1000
	MaximumImmediateAttempts   = 20
)

var ErrInvalidRetryPolicy = errors.New("invalid retry policy")

type RetryScheduleMode string

const (
	RetryScheduleInherit     RetryScheduleMode = "inherit"
	RetryScheduleImmediate   RetryScheduleMode = "immediate"
	RetryScheduleFixed       RetryScheduleMode = "fixed"
	RetryScheduleRandom      RetryScheduleMode = "random"
	RetryScheduleExponential RetryScheduleMode = "exponential"
)

type RetrySchedule struct {
	Mode     RetryScheduleMode
	Interval time.Duration
	Minimum  time.Duration
	Maximum  time.Duration
	Base     time.Duration
}

type RetryPolicySpec struct {
	Duration              time.Duration
	Schedule              RetrySchedule
	MaxAdditionalAttempts int
	HonorRetryAfter       bool
}

func (s RetryPolicySpec) Validate() error {
	if s.Duration < MinimumRetryPolicyDuration || s.Duration > MaximumRetryPolicyDuration {
		return ErrInvalidRetryPolicy
	}
	if s.MaxAdditionalAttempts < 0 || s.MaxAdditionalAttempts > MaximumPolicyAttempts {
		return ErrInvalidRetryPolicy
	}
	switch s.Schedule.Mode {
	case RetryScheduleInherit:
		return nil
	case RetryScheduleImmediate:
		if s.MaxAdditionalAttempts < 1 || s.MaxAdditionalAttempts > MaximumImmediateAttempts {
			return ErrInvalidRetryPolicy
		}
	case RetryScheduleFixed:
		if !validPolicyInterval(s.Schedule.Interval, s.Duration) {
			return ErrInvalidRetryPolicy
		}
	case RetryScheduleRandom:
		if !validPolicyInterval(s.Schedule.Minimum, s.Duration) ||
			s.Schedule.Maximum < s.Schedule.Minimum || s.Schedule.Maximum >= s.Duration || s.Schedule.Maximum > MaximumRetryPolicyDuration {
			return ErrInvalidRetryPolicy
		}
	case RetryScheduleExponential:
		if !validPolicyInterval(s.Schedule.Base, s.Duration) ||
			s.Schedule.Maximum < s.Schedule.Base || s.Schedule.Maximum >= s.Duration || s.Schedule.Maximum > MaximumRetryPolicyDuration {
			return ErrInvalidRetryPolicy
		}
	default:
		return ErrInvalidRetryPolicy
	}
	return nil
}

func validPolicyInterval(interval, duration time.Duration) bool {
	return interval >= MinimumRetryPolicyDuration && interval <= MaximumRetryPolicyDuration && interval < duration
}

type RetryPolicy struct {
	Duration              time.Duration
	Schedule              RetrySchedule
	MaxAdditionalAttempts int
	HonorRetryAfter       bool
	AppliedAt             time.Time
	ActivatedAt           time.Time
	Deadline              time.Time
	BaselineAttempt       int

	// Interval keeps the former fixed-policy field available to internal callers.
	Interval time.Duration
}

func NewRetryPolicy(spec RetryPolicySpec, appliedAt time.Time) RetryPolicy {
	return RetryPolicy{
		Duration: spec.Duration, Schedule: spec.Schedule,
		MaxAdditionalAttempts: spec.MaxAdditionalAttempts,
		HonorRetryAfter:       spec.HonorRetryAfter, AppliedAt: appliedAt,
		Interval: spec.Schedule.Interval,
	}
}

func (p RetryPolicy) Active() bool { return !p.ActivatedAt.IsZero() }

func (p RetryPolicy) Exhausted(attempt int) bool {
	return p.MaxAdditionalAttempts > 0 && attempt-p.BaselineAttempt >= p.MaxAdditionalAttempts
}

type RetryScheduleInfo struct {
	Mode                        RetryScheduleMode `json:"mode"`
	IntervalMilliseconds        int64             `json:"intervalMilliseconds,omitempty"`
	MinimumIntervalMilliseconds int64             `json:"minimumIntervalMilliseconds,omitempty"`
	MaximumIntervalMilliseconds int64             `json:"maximumIntervalMilliseconds,omitempty"`
	BaseIntervalMilliseconds    int64             `json:"baseIntervalMilliseconds,omitempty"`
}

type RetryPolicyInfo struct {
	State                       string            `json:"state"`
	DurationMilliseconds        int64             `json:"durationMilliseconds"`
	Schedule                    RetryScheduleInfo `json:"schedule"`
	MaxAdditionalAttempts       int               `json:"maxAdditionalAttempts,omitempty"`
	AdditionalAttemptsUsed      int               `json:"additionalAttemptsUsed"`
	RemainingAdditionalAttempts int               `json:"remainingAdditionalAttempts,omitempty"`
	HonorRetryAfter             bool              `json:"honorRetryAfter"`
	AppliedAt                   time.Time         `json:"appliedAt"`
	ActivatedAt                 *time.Time        `json:"activatedAt,omitempty"`
	Deadline                    *time.Time        `json:"deadline,omitempty"`
}

func (p RetryPolicy) Info(attempt int) *RetryPolicyInfo {
	used := 0
	state := "pending"
	var activatedAt, deadline *time.Time
	if p.Active() {
		state = "active"
		used = max(attempt-p.BaselineAttempt, 0)
		activated, ends := p.ActivatedAt, p.Deadline
		activatedAt, deadline = &activated, &ends
	}
	remaining := 0
	if p.MaxAdditionalAttempts > 0 {
		remaining = max(p.MaxAdditionalAttempts-used, 0)
	}
	return &RetryPolicyInfo{
		State: state, DurationMilliseconds: p.Duration.Milliseconds(),
		Schedule: RetryScheduleInfo{
			Mode: p.Schedule.Mode, IntervalMilliseconds: p.Schedule.Interval.Milliseconds(),
			MinimumIntervalMilliseconds: p.Schedule.Minimum.Milliseconds(),
			MaximumIntervalMilliseconds: p.Schedule.Maximum.Milliseconds(),
			BaseIntervalMilliseconds:    p.Schedule.Base.Milliseconds(),
		},
		MaxAdditionalAttempts: p.MaxAdditionalAttempts, AdditionalAttemptsUsed: used,
		RemainingAdditionalAttempts: remaining, HonorRetryAfter: p.HonorRetryAfter,
		AppliedAt: p.AppliedAt, ActivatedAt: activatedAt, Deadline: deadline,
	}
}

type RequestActions struct {
	CanRetryNow               bool `json:"canRetryNow"`
	CanSetRetryPolicy         bool `json:"canSetRetryPolicy"`
	RetryRequiresConfirmation bool `json:"retryRequiresConfirmation"`
	CanCancel                 bool `json:"canCancel"`
	CanRepeat                 bool `json:"canRepeat"`
}

func actionsForState(state lifecycle.State) RequestActions {
	actions := RequestActions{CanCancel: !lifecycle.IsTerminal(state), CanRepeat: !lifecycle.IsTerminal(state)}
	switch state {
	case lifecycle.StateWaiting:
		actions.CanRetryNow, actions.CanSetRetryPolicy = true, true
	case lifecycle.StateUncertain:
		actions.CanRetryNow, actions.CanSetRetryPolicy, actions.CanCancel, actions.CanRepeat = false, false, false, false
		actions.RetryRequiresConfirmation = true
	case lifecycle.StateQueued, lifecycle.StateForwarding:
		actions.CanSetRetryPolicy = true
	}
	return actions
}

type RequestActionOutcome string

const (
	RequestActionAccepted RequestActionOutcome = "accepted"
	RequestActionSkipped  RequestActionOutcome = "skipped"
)

type RequestActionReason string

const (
	RequestReasonNotFound               RequestActionReason = "not_found"
	RequestReasonStateNotRetryable      RequestActionReason = "state_not_retryable"
	RequestReasonStateNotPolicyCapable  RequestActionReason = "state_not_policy_capable"
	RequestReasonConfirmationRequired   RequestActionReason = "confirmation_required"
	RequestReasonAlreadyRequested       RequestActionReason = "already_requested"
	RequestReasonPolicyExists           RequestActionReason = "policy_exists"
	RequestReasonNoPolicy               RequestActionReason = "no_policy"
	RequestReasonPersistenceUnavailable RequestActionReason = "persistence_unavailable"
	RequestReasonUncertainResolution    RequestActionReason = "uncertain_resolution_required"
)

type RequestActionResult struct {
	ID      string               `json:"id"`
	Outcome RequestActionOutcome `json:"outcome"`
	Reason  RequestActionReason  `json:"reason,omitempty"`
	State   lifecycle.State      `json:"state,omitempty"`
}

func acceptedAction(id string, state lifecycle.State) RequestActionResult {
	return RequestActionResult{ID: id, Outcome: RequestActionAccepted, State: state}
}

func skippedAction(id string, state lifecycle.State, reason RequestActionReason) RequestActionResult {
	return RequestActionResult{ID: id, Outcome: RequestActionSkipped, Reason: reason, State: state}
}
