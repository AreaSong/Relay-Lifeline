package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

type RequestInfo struct {
	ID                  string           `json:"id"`
	ClientID            string           `json:"clientId,omitempty"`
	TaskID              string           `json:"taskId,omitempty"`
	Method              string           `json:"method"`
	Path                string           `json:"path"`
	State               lifecycle.State  `json:"state"`
	Attempt             int              `json:"attempt"`
	StartedAt           time.Time        `json:"startedAt"`
	UpdatedAt           time.Time        `json:"updatedAt"`
	NextRetryAt         time.Time        `json:"nextRetryAt,omitempty"`
	LastError           string           `json:"lastError,omitempty"`
	LastErrorCode       string           `json:"lastErrorCode,omitempty"`
	LastErrorDetails    map[string]any   `json:"lastErrorDetails,omitempty"`
	RetryDeadline       time.Time        `json:"retryDeadline,omitempty"`
	RetryIntervalMs     int64            `json:"retryIntervalMilliseconds,omitempty"`
	RetryPolicy         *RetryPolicyInfo `json:"retryPolicy,omitempty"`
	Actions             RequestActions   `json:"actions"`
	PersistenceDegraded bool             `json:"persistenceDegraded,omitempty"`
	PersistencePending  bool             `json:"persistencePending,omitempty"`
	UncertainSince      time.Time        `json:"uncertainSince,omitempty"`
	UncertainResolution string           `json:"uncertainResolution,omitempty"`
	UncertainResolvedAt time.Time        `json:"uncertainResolvedAt,omitempty"`
}

type RequestIdentity struct {
	ClientID string `json:"clientId,omitempty"`
	TaskID   string `json:"taskId,omitempty"`
}

type trackedRequest struct {
	info           RequestInfo
	cancel         context.CancelFunc
	retryNow       chan struct{}
	policyChanged  chan struct{}
	policy         *RetryPolicy
	notified       bool
	successCounted bool
}

type Registry struct {
	mu         sync.RWMutex
	requests   map[string]*trackedRequest
	total      atomic.Uint64
	success    atomic.Uint64
	failures   atomic.Uint64
	upstream   UpstreamInfo
	timeline   *timeline.Store
	persistErr error
}

type UpstreamInfo struct {
	State            string         `json:"state"`
	LastChecked      time.Time      `json:"lastChecked,omitempty"`
	LastError        string         `json:"lastError,omitempty"`
	LastErrorCode    string         `json:"lastErrorCode,omitempty"`
	LastErrorDetails map[string]any `json:"lastErrorDetails,omitempty"`
}

type Snapshot struct {
	Paused                 bool          `json:"paused"`
	Mode                   string        `json:"mode"`
	Active                 int           `json:"active"`
	Queued                 int           `json:"queued"`
	Waiting                int           `json:"waiting"`
	Requesting             int           `json:"requesting"`
	Uncertain              int           `json:"uncertain"`
	OldestUncertainSeconds float64       `json:"oldestUncertainSeconds,omitempty"`
	TotalRequests          uint64        `json:"totalRequests"`
	Successful             uint64        `json:"successful"`
	FailedAttempts         uint64        `json:"failedAttempts"`
	Upstream               UpstreamInfo  `json:"upstream"`
	Requests               []RequestInfo `json:"requests"`
	PersistenceDegraded    bool          `json:"persistenceDegraded"`
	PersistencePending     int           `json:"persistencePending"`
}

func NewRegistry(stores ...*timeline.Store) *Registry {
	store := timeline.New(func() timeline.Limits { return timeline.Limits{MaxItems: 500, Retention: 24 * time.Hour} })
	if len(stores) > 0 && stores[0] != nil {
		store = stores[0]
	}
	return &Registry{requests: make(map[string]*trackedRequest), timeline: store}
}

func (r *Registry) Add(method, path string, cancel context.CancelFunc) (string, <-chan struct{}) {
	return r.AddWithIdentity(method, path, cancel, RequestIdentity{})
}

func (r *Registry) AddWithIdentity(method, path string, cancel context.CancelFunc, identity RequestIdentity) (string, <-chan struct{}) {
	id, retry, _ := r.AddWithIdentityChecked(method, path, cancel, identity)
	return id, retry
}

// AddWithIdentityChecked persists the timeline anchor before exposing the
// request to the data plane. A failed anchor means no upstream attempt may be
// started for the request.
func (r *Registry) AddWithIdentityChecked(method, path string, cancel context.CancelFunc, identity RequestIdentity) (string, <-chan struct{}, error) {
	now := time.Now()
	id := newID()
	retryNow := make(chan struct{}, 1)
	if err := r.timeline.StartWithIdentity(id, method, path, identity.ClientID, identity.TaskID); err != nil {
		r.recordPersistenceError(err)
		return "", nil, err
	}
	policyChanged := make(chan struct{}, 1)
	r.mu.Lock()
	r.requests[id] = &trackedRequest{
		info: RequestInfo{
			ID: id, ClientID: identity.ClientID, TaskID: identity.TaskID,
			Method: method, Path: path, State: lifecycle.StateQueued, StartedAt: now, UpdatedAt: now,
		},
		cancel: cancel, retryNow: retryNow, policyChanged: policyChanged,
	}
	r.mu.Unlock()
	r.total.Add(1)
	return id, retryNow, nil
}

func (r *Registry) Identity(id string) (RequestIdentity, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	request, ok := r.requests[id]
	if !ok {
		return RequestIdentity{}, false
	}
	return RequestIdentity{ClientID: request.info.ClientID, TaskID: request.info.TaskID}, true
}

func (r *Registry) RequestInfo(id string) (RequestInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	request, ok := r.requests[id]
	if !ok {
		return RequestInfo{}, false
	}
	info := request.info
	info.Actions = actionsForState(info.State)
	if request.policy != nil {
		info.RetryPolicy = request.policy.Info(info.Attempt)
	}
	return info, true
}

func (r *Registry) Update(id string, status lifecycle.State, attempt int, lastError string, nextRetry time.Time) {
	r.UpdateMessage(id, status, attempt, l10n.Message{}, nextRetry)
	if lastError != "" {
		r.mu.Lock()
		if request, ok := r.requests[id]; ok {
			request.info.LastError = lastError
		}
		r.mu.Unlock()
	}
}

func (r *Registry) UpdateMessage(id string, status lifecycle.State, attempt int, message l10n.Message, nextRetry time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.requests[id]
	if !ok {
		return
	}
	if lifecycle.ValidateTransition(request.info.State, status) != nil {
		return
	}
	request.info.State = status
	if status == lifecycle.StateForwarding && request.info.UncertainResolution == UncertainRequestCompensation {
		request.info.UncertainResolution = ""
		request.info.UncertainSince = time.Time{}
		request.info.UncertainResolvedAt = time.Time{}
	}
	request.info.Attempt = attempt
	request.info.LastError = ""
	request.info.LastErrorCode = message.ID
	request.info.LastErrorDetails = cloneDetails(message.Data)
	request.info.NextRetryAt = nextRetry
	request.info.UpdatedAt = time.Now()
	if status == lifecycle.StateUncertain && request.info.UncertainSince.IsZero() {
		request.info.UncertainSince = request.info.UpdatedAt
	}
}

const (
	UncertainAbandon             = "abandon"
	UncertainConfirmSuccess      = "confirm_success"
	UncertainRequestCompensation = "request_compensation"
)

func (r *Registry) ResolveUncertain(id, resolution, reason string) RequestActionResult {
	r.mu.Lock()
	request, ok := r.requests[id]
	if !ok {
		r.mu.Unlock()
		return skippedAction(id, "", RequestReasonNotFound)
	}
	if request.info.State != lifecycle.StateUncertain {
		state := request.info.State
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonStateNotRetryable)
	}
	if request.info.UncertainResolution != "" {
		state := request.info.State
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonAlreadyRequested)
	}
	if resolution != UncertainAbandon && resolution != UncertainConfirmSuccess && resolution != UncertainRequestCompensation {
		state := request.info.State
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonStateNotRetryable)
	}
	event := timeline.Event{Type: "uncertain_" + resolution, Attempt: request.info.Attempt, Category: "operator_decision", MessageCode: "timeline.uncertain_" + resolution}
	if reason != "" {
		event.MessageDetails = map[string]any{"Reason": reason}
	}
	if err := r.timeline.Add(id, event); err != nil {
		r.recordPersistenceErrorLocked(id, err)
		state := request.info.State
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonPersistenceUnavailable)
	}
	now := time.Now()
	request.info.UncertainResolution = resolution
	request.info.UncertainResolvedAt = now
	request.info.UpdatedAt = now
	cancel, retry := request.cancel, request.retryNow
	state := request.info.State
	r.mu.Unlock()
	if resolution == UncertainRequestCompensation {
		select {
		case retry <- struct{}{}:
		default:
		}
	} else if cancel != nil {
		cancel()
	}
	return acceptedAction(id, state)
}

func (r *Registry) UncertainResolution(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if request := r.requests[id]; request != nil {
		return request.info.UncertainResolution
	}
	return ""
}

func (r *Registry) Remove(id string, outcome lifecycle.State) error {
	r.mu.Lock()
	request, ok := r.requests[id]
	if ok && lifecycle.ValidateTransition(request.info.State, outcome) != nil {
		r.mu.Unlock()
		return nil
	}
	if outcome == lifecycle.StateSuccessful && ok && !request.successCounted {
		request.successCounted = true
		r.success.Add(1)
	}
	err := r.timeline.Finish(id, string(outcome))
	if err != nil {
		r.recordPersistenceErrorLocked(id, err)
		if request != nil {
			request.info.PersistencePending = true
		}
		r.mu.Unlock()
		return err
	}
	delete(r.requests, id)
	r.mu.Unlock()
	return nil
}

func (r *Registry) RecordFailure() { r.failures.Add(1) }

func (r *Registry) SetUpstream(healthy bool, reason string) {
	r.SetUpstreamMessage(healthy, l10n.Message{})
	if reason != "" {
		r.mu.Lock()
		r.upstream.LastError = reason
		r.mu.Unlock()
	}
}

func (r *Registry) SetUpstreamMessage(healthy bool, message l10n.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := "healthy"
	if !healthy {
		status = "degraded"
	}
	r.upstream = UpstreamInfo{State: status, LastChecked: time.Now(), LastErrorCode: message.ID, LastErrorDetails: cloneDetails(message.Data)}
}

func (r *Registry) Cancel(id string) bool {
	return r.CancelChecked(id).Outcome == RequestActionAccepted
}

func (r *Registry) CancelChecked(id string) RequestActionResult {
	r.mu.Lock()
	request, ok := r.requests[id]
	if !ok {
		r.mu.Unlock()
		return skippedAction(id, "", RequestReasonNotFound)
	}
	state := request.info.State
	if state == lifecycle.StateUncertain {
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonUncertainResolution)
	}
	if !actionsForState(state).CanCancel {
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonStateNotRetryable)
	}
	if err := r.timeline.Add(id, timeline.Event{Type: "cancel_requested", MessageCode: "timeline.cancel_requested"}); err != nil {
		r.recordPersistenceErrorLocked(id, err)
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonPersistenceUnavailable)
	}
	cancel := request.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return acceptedAction(id, state)
}

func (r *Registry) RetryNow(id string) bool {
	r.mu.RLock()
	request, ok := r.requests[id]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	r.timeline.Add(id, timeline.Event{Type: "retry_requested", MessageCode: "timeline.retry_requested"})
	select {
	case request.retryNow <- struct{}{}:
	default:
	}
	return true
}

func (r *Registry) RetryNowChecked(id string) RequestActionResult {
	r.mu.RLock()
	request, ok := r.requests[id]
	if !ok {
		r.mu.RUnlock()
		return skippedAction(id, "", RequestReasonNotFound)
	}
	state, retry := request.info.State, request.retryNow
	r.mu.RUnlock()
	if state == lifecycle.StateUncertain {
		return skippedAction(id, state, RequestReasonUncertainResolution)
	}
	if state != lifecycle.StateWaiting && state != lifecycle.StateUncertain {
		return skippedAction(id, state, RequestReasonStateNotRetryable)
	}
	select {
	case retry <- struct{}{}:
		if err := r.timeline.Add(id, timeline.Event{Type: "retry_requested", MessageCode: "timeline.retry_requested"}); err != nil {
			r.recordPersistenceError(err)
			return skippedAction(id, state, RequestReasonPersistenceUnavailable)
		}
		return acceptedAction(id, state)
	default:
		return skippedAction(id, state, RequestReasonAlreadyRequested)
	}
}

func (r *Registry) PolicyChanges(id string) <-chan struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if request, ok := r.requests[id]; ok {
		return request.policyChanged
	}
	return nil
}

func (r *Registry) SetRetryPolicy(id string, duration, interval time.Duration) bool {
	result := r.applyRetryPolicy(id, RetryPolicySpec{
		Duration: duration, Schedule: RetrySchedule{Mode: RetryScheduleFixed, Interval: interval},
		HonorRetryAfter: true,
	}, true, false)
	return result.Outcome == RequestActionAccepted
}

func (r *Registry) RetryPolicy(id string) (RetryPolicy, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	request, ok := r.requests[id]
	if !ok || request.policy == nil {
		return RetryPolicy{}, false
	}
	return *request.policy, true
}

func (r *Registry) ApplyRetryPolicy(id string, spec RetryPolicySpec, overwrite bool) RequestActionResult {
	return r.applyRetryPolicy(id, spec, overwrite, true)
}

func (r *Registry) applyRetryPolicy(id string, spec RetryPolicySpec, overwrite, validate bool) RequestActionResult {
	if validate && spec.Validate() != nil {
		return skippedAction(id, "", RequestReasonStateNotPolicyCapable)
	}
	now := time.Now()
	r.mu.Lock()
	request, ok := r.requests[id]
	if !ok {
		r.mu.Unlock()
		return skippedAction(id, "", RequestReasonNotFound)
	}
	state := request.info.State
	if !actionsForState(state).CanSetRetryPolicy {
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonStateNotPolicyCapable)
	}
	if request.policy != nil && !overwrite {
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonPolicyExists)
	}
	policy := NewRetryPolicy(spec, now)
	if state == lifecycle.StateWaiting {
		activatePolicy(&policy, request.info.Attempt, now)
	}
	request.policy = &policy
	updatePolicyInfo(request)
	request.info.UpdatedAt = now
	policyChanged := request.policyChanged
	r.mu.Unlock()
	if err := r.timeline.Add(id, timeline.Event{
		Type: "retry_policy_updated", MessageCode: "timeline.retry_policy_updated",
		WaitMilliseconds: policyPrimaryInterval(policy).Milliseconds(),
		MessageDetails:   map[string]any{"Mode": string(policy.Schedule.Mode)},
	}); err != nil {
		r.recordPersistenceError(err)
		return skippedAction(id, state, RequestReasonPersistenceUnavailable)
	}
	if state == lifecycle.StateWaiting {
		signal(policyChanged)
	}
	return acceptedAction(id, state)
}

func (r *Registry) ClearRetryPolicy(id string) RequestActionResult {
	r.mu.Lock()
	request, ok := r.requests[id]
	if !ok {
		r.mu.Unlock()
		return skippedAction(id, "", RequestReasonNotFound)
	}
	state := request.info.State
	if !actionsForState(state).CanSetRetryPolicy {
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonStateNotPolicyCapable)
	}
	if request.policy == nil {
		r.mu.Unlock()
		return skippedAction(id, state, RequestReasonNoPolicy)
	}
	request.policy = nil
	updatePolicyInfo(request)
	request.info.UpdatedAt = time.Now()
	policyChanged := request.policyChanged
	r.mu.Unlock()
	if err := r.timeline.Add(id, timeline.Event{Type: "retry_policy_reset", MessageCode: "timeline.retry_policy_reset"}); err != nil {
		r.recordPersistenceError(err)
		return skippedAction(id, state, RequestReasonPersistenceUnavailable)
	}
	if state == lifecycle.StateWaiting {
		signal(policyChanged)
	}
	return acceptedAction(id, state)
}

func (r *Registry) ActivateRetryPolicy(id string, attempt int) (RetryPolicy, bool) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.requests[id]
	if !ok || request.policy == nil {
		return RetryPolicy{}, false
	}
	if !request.policy.Active() {
		activatePolicy(request.policy, attempt, now)
		updatePolicyInfo(request)
		request.info.UpdatedAt = now
		if err := r.timeline.Add(id, timeline.Event{Type: "retry_policy_activated", Attempt: attempt, MessageCode: "timeline.retry_policy_activated"}); err != nil {
			if r.persistErr == nil {
				r.persistErr = err
			}
		}
	}
	return *request.policy, true
}

func activatePolicy(policy *RetryPolicy, attempt int, now time.Time) {
	policy.ActivatedAt = now
	policy.Deadline = now.Add(policy.Duration)
	policy.BaselineAttempt = attempt
}

func updatePolicyInfo(request *trackedRequest) {
	request.info.RetryDeadline = time.Time{}
	request.info.RetryIntervalMs = 0
	request.info.RetryPolicy = nil
	if request.policy == nil {
		return
	}
	request.info.RetryDeadline = request.policy.Deadline
	request.info.RetryIntervalMs = policyPrimaryInterval(*request.policy).Milliseconds()
	request.info.RetryPolicy = request.policy.Info(request.info.Attempt)
}

func policyPrimaryInterval(policy RetryPolicy) time.Duration {
	switch policy.Schedule.Mode {
	case RetryScheduleFixed:
		return policy.Schedule.Interval
	case RetryScheduleRandom:
		return policy.Schedule.Minimum
	case RetryScheduleExponential:
		return policy.Schedule.Base
	default:
		return 0
	}
}

func signal(channel chan<- struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func (r *Registry) RetryWaiting() int {
	r.mu.RLock()
	ids := make([]string, 0)
	for id, request := range r.requests {
		if request.info.State == lifecycle.StateWaiting {
			ids = append(ids, id)
		}
	}
	r.mu.RUnlock()
	retried := 0
	for _, id := range ids {
		if r.RetryNowChecked(id).Outcome == RequestActionAccepted {
			retried++
		}
	}
	return retried
}

func (r *Registry) RecordEvent(id string, event timeline.Event) error {
	err := r.timeline.Add(id, event)
	if err != nil {
		r.recordPersistenceErrorFor(id, err)
	}
	return err
}

func (r *Registry) PersistenceError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.persistErr
}

func (r *Registry) recordPersistenceError(err error) {
	r.recordPersistenceErrorFor("", err)
}

func (r *Registry) recordPersistenceErrorFor(id string, err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.recordPersistenceErrorLocked(id, err)
	r.mu.Unlock()
}

func (r *Registry) recordPersistenceErrorLocked(id string, err error) {
	if r.persistErr == nil {
		r.persistErr = err
	}
	if request, ok := r.requests[id]; ok {
		request.info.PersistenceDegraded = true
		request.info.UpdatedAt = time.Now()
	}
}

func (r *Registry) Timeline(id string) (timeline.Record, bool) { return r.timeline.Request(id) }

func (r *Registry) History() []timeline.Record { return r.timeline.History() }

func (r *Registry) MarkNotified(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.requests[id]
	if !ok || request.notified {
		return false
	}
	request.notified = true
	return true
}

func (r *Registry) WasNotified(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	request, ok := r.requests[id]
	return ok && request.notified
}

func (r *Registry) Snapshot(paused bool) Snapshot {
	r.mu.RLock()
	requests := make([]RequestInfo, 0, len(r.requests))
	queued, waiting, requesting, uncertain, persistencePending := 0, 0, 0, 0, 0
	oldestUncertainSeconds := 0.0
	now := time.Now()
	for _, request := range r.requests {
		info := request.info
		info.Actions = actionsForState(info.State)
		if request.policy != nil {
			info.RetryPolicy = request.policy.Info(info.Attempt)
		} else {
			info.RetryPolicy = nil
		}
		requests = append(requests, info)
		if info.PersistencePending {
			persistencePending++
		}
		switch request.info.State {
		case lifecycle.StateQueued:
			queued++
		case lifecycle.StateWaiting:
			waiting++
		case lifecycle.StateForwarding:
			requesting++
		case lifecycle.StateUncertain:
			uncertain++
			since := info.UncertainSince
			if since.IsZero() {
				since = info.UpdatedAt
			}
			age := now.Sub(since).Seconds()
			if age > oldestUncertainSeconds {
				oldestUncertainSeconds = age
			}
		}
	}
	upstream := r.upstream
	persistenceDegraded := r.persistErr != nil
	r.mu.RUnlock()
	if upstream.State == "" {
		upstream.State = "unknown"
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].StartedAt.Before(requests[j].StartedAt) })
	mode := ControlRunning
	if paused {
		mode = ControlPaused
	}
	return Snapshot{
		Paused: paused, Mode: mode, Active: len(requests) - persistencePending, Queued: queued, Waiting: waiting, Requesting: requesting, Uncertain: uncertain, OldestUncertainSeconds: oldestUncertainSeconds,
		TotalRequests: r.total.Load(),
		Successful:    r.success.Load(), FailedAttempts: r.failures.Load(), Upstream: upstream, Requests: requests,
		PersistenceDegraded: persistenceDegraded, PersistencePending: persistencePending,
	}
}

func (r *Registry) LocalizedSnapshot(paused bool, locale, fallback string) Snapshot {
	snapshot := r.Snapshot(paused)
	for index := range snapshot.Requests {
		request := &snapshot.Requests[index]
		if request.LastErrorCode != "" {
			request.LastError = l10n.Default.Text(locale, fallback, l10n.M(request.LastErrorCode, request.LastErrorDetails))
		}
	}
	if snapshot.Upstream.LastErrorCode != "" {
		snapshot.Upstream.LastError = l10n.Default.Text(locale, fallback, l10n.M(snapshot.Upstream.LastErrorCode, snapshot.Upstream.LastErrorDetails))
	}
	return snapshot
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	copy := make(map[string]any, len(details))
	for key, value := range details {
		copy[key] = value
	}
	return copy
}

func newID() string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return time.Now().Format("150405.000000")
	}
	return hex.EncodeToString(buffer)
}

type Controller struct {
	mu       sync.RWMutex
	mode     string
	resumeCh chan struct{}
}

const (
	ControlRunning     = "running"
	ControlPaused      = "paused"
	ControlDraining    = "draining"
	ControlMaintenance = "maintenance"
)

func NewController() *Controller {
	return &Controller{mode: ControlRunning, resumeCh: make(chan struct{})}
}

func (c *Controller) Pause() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mode != ControlRunning {
		return false
	}
	c.mode = ControlPaused
	c.resumeCh = make(chan struct{})
	return true
}

func (c *Controller) Resume() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mode == ControlRunning {
		return false
	}
	wasPaused := c.mode == ControlPaused
	c.mode = ControlRunning
	if wasPaused {
		close(c.resumeCh)
	}
	return true
}

func (c *Controller) IsPaused() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode == ControlPaused
}

func (c *Controller) Mode() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode
}

func (c *Controller) Accepting() bool { return c.Mode() == ControlRunning || c.Mode() == ControlPaused }

func (c *Controller) Drain() bool { return c.setNonAccepting(ControlDraining) }

func (c *Controller) Maintenance() bool { return c.setNonAccepting(ControlMaintenance) }

func (c *Controller) setNonAccepting(mode string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mode == mode {
		return false
	}
	wasPaused := c.mode == ControlPaused
	c.mode = mode
	if wasPaused {
		close(c.resumeCh)
	}
	return true
}

func (c *Controller) Wait(ctx context.Context) error {
	for {
		c.mu.RLock()
		paused, resumeCh := c.mode == ControlPaused, c.resumeCh
		c.mu.RUnlock()
		if !paused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resumeCh:
		}
	}
}
