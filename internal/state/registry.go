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
	"github.com/areasong/relay-lifeline/internal/timeline"
)

type RequestInfo struct {
	ID               string         `json:"id"`
	Method           string         `json:"method"`
	Path             string         `json:"path"`
	State            string         `json:"state"`
	Attempt          int            `json:"attempt"`
	StartedAt        time.Time      `json:"startedAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	NextRetryAt      time.Time      `json:"nextRetryAt,omitempty"`
	LastError        string         `json:"lastError,omitempty"`
	LastErrorCode    string         `json:"lastErrorCode,omitempty"`
	LastErrorDetails map[string]any `json:"lastErrorDetails,omitempty"`
}

type trackedRequest struct {
	info     RequestInfo
	cancel   context.CancelFunc
	retryNow chan struct{}
	notified bool
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
	now := time.Now()
	id := newID()
	retryNow := make(chan struct{}, 1)
	r.mu.Lock()
	r.requests[id] = &trackedRequest{
		info:   RequestInfo{ID: id, Method: method, Path: path, State: "queued", StartedAt: now, UpdatedAt: now},
		cancel: cancel, retryNow: retryNow,
	}
	r.mu.Unlock()
	r.total.Add(1)
	r.timeline.Start(id, method, path)
	return id, retryNow
}

func (r *Registry) Update(id, status string, attempt int, lastError string, nextRetry time.Time) {
	r.UpdateMessage(id, status, attempt, l10n.Message{}, nextRetry)
	if lastError != "" {
		r.mu.Lock()
		if request, ok := r.requests[id]; ok {
			request.info.LastError = lastError
		}
		r.mu.Unlock()
	}
}

func (r *Registry) UpdateMessage(id, status string, attempt int, message l10n.Message, nextRetry time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.requests[id]
	if !ok {
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

func (r *Registry) Remove(id, outcome string) {
	r.mu.Lock()
	delete(r.requests, id)
	r.mu.Unlock()
	r.timeline.Finish(id, outcome)
	if outcome == "successful" {
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

func (r *Registry) RetryWaiting() int {
	r.mu.RLock()
	ids := make([]string, 0)
	for id, request := range r.requests {
		if request.info.State == "waiting" {
			ids = append(ids, id)
		}
	}
	r.mu.RUnlock()
	retried := 0
	for _, id := range ids {
		if r.RetryNow(id) {
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
		requests = append(requests, request.info)
		switch request.info.State {
		case "queued":
			queued++
		case "waiting":
			waiting++
		case "requesting":
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
