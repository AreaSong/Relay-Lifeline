package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type RequestInfo struct {
	ID          string    `json:"id"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	State       string    `json:"state"`
	Attempt     int       `json:"attempt"`
	StartedAt   time.Time `json:"startedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	NextRetryAt time.Time `json:"nextRetryAt,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
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
}

type UpstreamInfo struct {
	State       string    `json:"state"`
	LastChecked time.Time `json:"lastChecked,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
}

type Snapshot struct {
	Paused         bool          `json:"paused"`
	Active         int           `json:"active"`
	TotalRequests  uint64        `json:"totalRequests"`
	Successful     uint64        `json:"successful"`
	FailedAttempts uint64        `json:"failedAttempts"`
	Upstream       UpstreamInfo  `json:"upstream"`
	Requests       []RequestInfo `json:"requests"`
}

func NewRegistry() *Registry { return &Registry{requests: make(map[string]*trackedRequest)} }

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
	return id, retryNow
}

func (r *Registry) Update(id, status string, attempt int, lastError string, nextRetry time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.requests[id]
	if !ok {
		return
	}
	request.info.State = status
	request.info.Attempt = attempt
	request.info.LastError = lastError
	request.info.NextRetryAt = nextRetry
	request.info.UpdatedAt = time.Now()
}

func (r *Registry) Remove(id string, succeeded bool) {
	r.mu.Lock()
	delete(r.requests, id)
	r.mu.Unlock()
	if succeeded {
		r.success.Add(1)
	}
}

func (r *Registry) RecordFailure() { r.failures.Add(1) }

func (r *Registry) SetUpstream(healthy bool, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := "healthy"
	if !healthy {
		status = "degraded"
	}
	r.upstream = UpstreamInfo{State: status, LastChecked: time.Now(), LastError: reason}
}

func (r *Registry) Cancel(id string) bool {
	r.mu.RLock()
	request, ok := r.requests[id]
	r.mu.RUnlock()
	if ok {
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
	select {
	case request.retryNow <- struct{}{}:
	default:
	}
	return true
}

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
	for _, request := range r.requests {
		requests = append(requests, request.info)
	}
	upstream := r.upstream
	r.mu.RUnlock()
	if upstream.State == "" {
		upstream.State = "unknown"
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].StartedAt.Before(requests[j].StartedAt) })
	return Snapshot{
		Paused: paused, Active: len(requests), TotalRequests: r.total.Load(),
		Successful: r.success.Load(), FailedAttempts: r.failures.Load(), Upstream: upstream, Requests: requests,
	}
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
