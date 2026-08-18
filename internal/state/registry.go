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
	ID               string           `json:"id"`
	ClientID         string           `json:"clientId,omitempty"`
	TaskID           string           `json:"taskId,omitempty"`
	Method           string           `json:"method"`
	Path             string           `json:"path"`
	State            lifecycle.State  `json:"state"`
	Attempt          int              `json:"attempt"`
	StartedAt        time.Time        `json:"startedAt"`
	UpdatedAt        time.Time        `json:"updatedAt"`
	NextRetryAt      time.Time        `json:"nextRetryAt,omitempty"`
	LastError        string           `json:"lastError,omitempty"`
	LastErrorCode    string           `json:"lastErrorCode,omitempty"`
	LastErrorDetails map[string]any   `json:"lastErrorDetails,omitempty"`
	RetryDeadline    time.Time        `json:"retryDeadline,omitempty"`
	RetryIntervalMs  int64            `json:"retryIntervalMilliseconds,omitempty"`
	RetryPolicy      *RetryPolicyInfo `json:"retryPolicy,omitempty"`
	Actions          RequestActions   `json:"actions"`
}

type RequestIdentity struct {
	ClientID string `json:"clientId,omitempty"`
	TaskID   string `json:"taskId,omitempty"`
}

type trackedRequest struct {
	info          RequestInfo
	cancel        context.CancelFunc
	retryNow      chan struct{}
	policyChanged chan struct{}
	policy        *RetryPolicy
	notified      bool
}

type Registry struct {
	mu       sync.RWMutex
	requests map[string]*trackedRequest
	total    atomic.Uint64
	success  atomic.Uint64
	failures atomic.Uint64
	upstream UpstreamInfo
	timeline *timeline.Store
}

type UpstreamInfo struct {
	State            string         `json:"state"`
	LastChecked      time.Time      `json:"lastChecked,omitempty"`
	LastError        string         `json:"lastError,omitempty"`
	LastErrorCode    string         `json:"lastErrorCode,omitempty"`
	LastErrorDetails map[string]any `json:"lastErrorDetails,omitempty"`
}

type Snapshot struct {
	Paused         bool          `json:"paused"`
	Active         int           `json:"active"`
	Queued         int           `json:"queued"`
	Waiting        int           `json:"waiting"`
	Requesting     int           `json:"requesting"`
	TotalRequests  uint64        `json:"totalRequests"`
	Successful     uint64        `json:"successful"`
	FailedAttempts uint64        `json:"failedAttempts"`
	Upstream       UpstreamInfo  `json:"upstream"`
	Requests       []RequestInfo `json:"requests"`
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
	now := time.Now()
	id := newID()
	retryNow := make(chan struct{}, 1)
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
	r.timeline.StartWithIdentity(id, method, path, identity.ClientID, identity.TaskID)
	return id, retryNow
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
	request.info.Attempt = attempt
	request.info.LastError = ""
	request.info.LastErrorCode = message.ID
	request.info.LastErrorDetails = cloneDetails(message.Data)
	request.info.NextRetryAt = nextRetry
	request.info.UpdatedAt = time.Now()
}

func (r *Registry) Remove(id string, outcome lifecycle.State) {
	r.mu.Lock()
	request, ok := r.requests[id]
	if ok && lifecycle.ValidateTransition(request.info.State, outcome) != nil {
		r.mu.Unlock()
		return
	}
	delete(r.requests, id)
	r.mu.Unlock()
	r.timeline.Finish(id, string(outcome))
	if outcome == lifecycle.StateSuccessful {
		r.success.Add(1)
	}
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
	r.mu.RLock()
	request, ok := r.requests[id]
	r.mu.RUnlock()
	if ok {
		r.timeline.Add(id, timeline.Event{Type: "cancel_requested", MessageCode: "timeline.cancel_requested"})
		request.cancel()
	}
	return ok
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

func (r *Registry) RetryNowChecked(id string, allowUncertain bool) RequestActionResult {
	r.mu.RLock()
	request, ok := r.requests[id]
	if !ok {
		r.mu.RUnlock()
		return skippedAction(id, "", RequestReasonNotFound)
	}
	state, retry := request.info.State, request.retryNow
	r.mu.RUnlock()
	if state == lifecycle.StateUncertain && !allowUncertain {
		return skippedAction(id, state, RequestReasonConfirmationRequired)
	}
	if state != lifecycle.StateWaiting && state != lifecycle.StateUncertain {
		return skippedAction(id, state, RequestReasonStateNotRetryable)
	}
	select {
	case retry <- struct{}{}:
		r.timeline.Add(id, timeline.Event{Type: "retry_requested", MessageCode: "timeline.retry_requested"})
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
	r.timeline.Add(id, timeline.Event{
		Type: "retry_policy_updated", MessageCode: "timeline.retry_policy_updated",
		WaitMilliseconds: policyPrimaryInterval(policy).Milliseconds(),
		MessageDetails:   map[string]any{"Mode": string(policy.Schedule.Mode)},
	})
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
	r.timeline.Add(id, timeline.Event{Type: "retry_policy_reset", MessageCode: "timeline.retry_policy_reset"})
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
		r.timeline.Add(id, timeline.Event{Type: "retry_policy_activated", Attempt: attempt, MessageCode: "timeline.retry_policy_activated"})
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
		if r.RetryNowChecked(id, false).Outcome == RequestActionAccepted {
			retried++
		}
	}
	return retried
}

func (r *Registry) RecordEvent(id string, event timeline.Event) { r.timeline.Add(id, event) }

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
	queued, waiting, requesting := 0, 0, 0
	for _, request := range r.requests {
		info := request.info
		info.Actions = actionsForState(info.State)
		if request.policy != nil {
			info.RetryPolicy = request.policy.Info(info.Attempt)
		} else {
			info.RetryPolicy = nil
		}
		requests = append(requests, info)
		switch request.info.State {
		case lifecycle.StateQueued:
			queued++
		case lifecycle.StateWaiting:
			waiting++
		case lifecycle.StateForwarding:
			requesting++
		}
	}
	upstream := r.upstream
	r.mu.RUnlock()
	if upstream.State == "" {
		upstream.State = "unknown"
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].StartedAt.Before(requests[j].StartedAt) })
	return Snapshot{
		Paused: paused, Active: len(requests), Queued: queued, Waiting: waiting, Requesting: requesting,
		TotalRequests: r.total.Load(),
		Successful:    r.success.Load(), FailedAttempts: r.failures.Load(), Upstream: upstream, Requests: requests,
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
	paused   bool
	resumeCh chan struct{}
}

func NewController() *Controller { return &Controller{resumeCh: make(chan struct{})} }

func (c *Controller) Pause() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused {
		return false
	}
	c.paused = true
	c.resumeCh = make(chan struct{})
	return true
}

func (c *Controller) Resume() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.paused {
		return false
	}
	c.paused = false
	close(c.resumeCh)
	return true
}

func (c *Controller) IsPaused() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.paused
}

func (c *Controller) Wait(ctx context.Context) error {
	for {
		c.mu.RLock()
		paused, resumeCh := c.paused, c.resumeCh
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
