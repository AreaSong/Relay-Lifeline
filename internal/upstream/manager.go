package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/egress"
)

var (
	ErrNoHealthyTarget       = errors.New("no healthy upstream target")
	ErrFailoverNotSafe       = errors.New("automatic failover is not safe for this attempt")
	ErrTargetConcurrencyFull = errors.New("all upstream target concurrency limits are full")
)

type Target struct {
	ID                string  `json:"id"`
	BaseURL           string  `json:"baseUrl"`
	Priority          int     `json:"priority"`
	Weight            int     `json:"weight"`
	MaxActive         int     `json:"maxActive"`
	IdempotencyDomain string  `json:"idempotencyDomain"`
	CostMicrosPer1K   int64   `json:"costMicrosPer1K"`
	CapabilityScore   float64 `json:"capabilityScore"`
}

type SelectionContext struct {
	PreferredTargetID      string
	RequirePreferredTarget bool
	PreviousTargetID       string
	PreviousDomain         string
	WroteRequest           bool
	IdempotencyKey         string
	AllowCrossDomain       bool
}

type Observation struct {
	Success      bool
	WroteRequest bool
	StatusCode   int
	Category     string
	Latency      time.Duration
}

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half-open"
)

type TargetStatus struct {
	Target         Target       `json:"target"`
	State          CircuitState `json:"circuitState"`
	Active         int          `json:"active"`
	FailureCount   int          `json:"failureCount"`
	SuccessCount   int          `json:"successCount"`
	LastFailureAt  time.Time    `json:"lastFailureAt,omitempty"`
	LastSuccessAt  time.Time    `json:"lastSuccessAt,omitempty"`
	LastLatencyMs  int64        `json:"lastLatencyMilliseconds,omitempty"`
	LastErrorClass string       `json:"lastErrorClass,omitempty"`
	HalfOpenLeases int          `json:"halfOpenLeases,omitempty"`
	ErrorRate      float64      `json:"errorRate"`
	RateLimitRate  float64      `json:"rateLimitRate"`
}

type PoolStatus struct {
	Strategy string         `json:"strategy"`
	Targets  []TargetStatus `json:"targets"`
}

type targetState struct {
	cfg               Target
	active            int
	failures          int
	rateLimitFailures int
	successes         int
	lastFailureAt     time.Time
	lastSuccessAt     time.Time
	lastLatency       time.Duration
	lastErrorClass    string
	circuit           CircuitState
	openedAt          time.Time
	halfOpenLeases    int
	client            *http.Client
	transport         *http.Transport
	transportCfg      config.UpstreamConfig
}

type Manager struct {
	mu       sync.Mutex
	strategy string
	health   config.UpstreamHealthConfig
	circuit  config.UpstreamCircuitConfig
	targets  map[string]*targetState
	order    []string
	cursor   uint64
	now      func() time.Time
	egress   egress.Policy
}

type Lease struct {
	manager *Manager
	state   *targetState
	target  Target
	once    sync.Once
}

func New(pool config.UpstreamPoolConfig, legacy config.UpstreamConfig) (*Manager, error) {
	return NewWithEgress(pool, legacy, egress.Policy{})
}

func NewWithEgress(pool config.UpstreamPoolConfig, legacy config.UpstreamConfig, policy egress.Policy) (*Manager, error) {
	manager := &Manager{targets: make(map[string]*targetState), now: time.Now, egress: policy.Normalize()}
	if err := manager.ApplyWithEgress(pool, legacy, policy); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Apply(pool config.UpstreamPoolConfig, legacy config.UpstreamConfig) error {
	return m.ApplyWithEgress(pool, legacy, m.egress)
}

func (m *Manager) ApplyWithEgress(pool config.UpstreamPoolConfig, legacy config.UpstreamConfig, policy egress.Policy) error {
	m.mu.Lock()
	policy = policy.Normalize()
	strategy := pool.Strategy
	if strategy == "" {
		strategy = "primary-only"
	}
	targets := pool.Targets
	if len(targets) == 0 {
		targets = []config.UpstreamTargetConfig{{ID: "primary", BaseURL: legacy.BaseURL, Priority: 0, Weight: 1, IdempotencyDomain: "legacy"}}
	}
	next := make(map[string]*targetState, len(targets))
	for _, target := range targets {
		if target.ID == "" || target.BaseURL == "" {
			m.mu.Unlock()
			return fmt.Errorf("invalid upstream target %q", target.ID)
		}
		if target.Weight <= 0 {
			target.Weight = 1
		}
		previous := m.targets[target.ID]
		targetConfig := Target{ID: target.ID, BaseURL: target.BaseURL, Priority: target.Priority, Weight: target.Weight, MaxActive: target.MaxActive, IdempotencyDomain: target.IdempotencyDomain, CostMicrosPer1K: target.CostMicrosPer1K, CapabilityScore: target.CapabilityScore}
		if previous != nil && previous.cfg.BaseURL == target.BaseURL && previous.transportCfg == legacy {
			previous.cfg = targetConfig
			next[target.ID] = previous
			continue
		}
		state := &targetState{cfg: targetConfig, circuit: CircuitClosed}
		if previous != nil && previous.cfg.BaseURL == target.BaseURL {
			state.failures, state.successes, state.rateLimitFailures = previous.failures, previous.successes, previous.rateLimitFailures
			state.lastFailureAt, state.lastSuccessAt, state.lastLatency = previous.lastFailureAt, previous.lastSuccessAt, previous.lastLatency
			state.lastErrorClass, state.circuit, state.openedAt = previous.lastErrorClass, previous.circuit, previous.openedAt
		}
		state.client, state.transport = newTargetHTTPClient(legacy, policy)
		state.transportCfg = legacy
		next[target.ID] = state
	}
	previousTargets := m.targets
	m.strategy, m.health, m.circuit, m.targets, m.egress = strategy, pool.Health, pool.Circuit, next, policy
	if m.circuit.OpenDuration.Duration <= 0 {
		m.circuit.OpenDuration.Duration = 30 * time.Second
	}
	if m.circuit.HalfOpenMax <= 0 {
		m.circuit.HalfOpenMax = 1
	}
	if m.circuit.MinimumRequests <= 0 {
		m.circuit.MinimumRequests = 5
	}
	if m.circuit.FailurePercent <= 0 {
		m.circuit.FailurePercent = 60
	}
	m.rebuildOrderLocked()
	m.mu.Unlock()
	for id, previous := range previousTargets {
		if current := next[id]; previous.transport != nil && (current == nil || current.transport != previous.transport) {
			previous.transport.CloseIdleConnections()
		}
	}
	return nil
}

func (m *Manager) Select(ctx context.Context, selection SelectionContext) (*Lease, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		candidates := m.candidatesLocked(selection)
		if len(candidates) == 0 {
			unsafe := m.safetyBlockedLocked(selection)
			m.mu.Unlock()
			if unsafe {
				return nil, ErrFailoverNotSafe
			}
			return nil, ErrNoHealthyTarget
		}
		chosen := m.chooseLocked(candidates)
		if chosen != nil && (chosen.cfg.MaxActive <= 0 || chosen.active < chosen.cfg.MaxActive) {
			if chosen.circuit == CircuitHalfOpen {
				chosen.halfOpenLeases++
			}
			chosen.active++
			lease := &Lease{manager: m, state: chosen, target: chosen.cfg}
			m.mu.Unlock()
			return lease, nil
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		return nil, ErrTargetConcurrencyFull
	}
}

func (l *Lease) Target() Target { return l.target }

func (l *Lease) Client() *http.Client {
	if l == nil || l.state == nil {
		return nil
	}
	return l.state.client
}

func (l *Lease) Release() {
	l.finish(nil)
}

func (l *Lease) Complete(observation Observation) {
	l.finish(&observation)
}

func (l *Lease) finish(observation *Observation) {
	if l == nil || l.manager == nil || l.state == nil {
		return
	}
	l.once.Do(func() {
		l.manager.mu.Lock()
		defer l.manager.mu.Unlock()
		state := l.state
		if state.active > 0 {
			state.active--
		}
		if state.circuit == CircuitHalfOpen && state.halfOpenLeases > 0 {
			state.halfOpenLeases--
		}
		if observation != nil {
			l.manager.observeLocked(state, *observation)
		}
	})
}

func (m *Manager) Observe(targetID string, observation Observation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.targets[targetID]
	if state == nil {
		return
	}
	m.observeLocked(state, observation)
}

func (m *Manager) observeLocked(state *targetState, observation Observation) {
	now := m.now()
	if observation.Latency > 0 {
		state.lastLatency = observation.Latency
	}
	if observation.Success {
		state.successes++
		state.lastSuccessAt = now
		state.lastErrorClass = ""
		if state.circuit == CircuitHalfOpen || state.circuit == CircuitOpen {
			state.circuit = CircuitClosed
			state.failures = 0
		}
		return
	}
	if !countsForCircuit(observation) {
		state.lastFailureAt = now
		state.lastErrorClass = observation.Category
		return
	}
	state.failures++
	if observation.Category == "rate_limit" {
		state.rateLimitFailures++
	}
	state.lastFailureAt = now
	state.lastErrorClass = observation.Category
	if !m.circuit.Enabled {
		return
	}
	requests := state.failures + state.successes
	if state.circuit == CircuitHalfOpen {
		state.circuit = CircuitOpen
		state.openedAt = now
		return
	}
	if requests >= m.circuit.MinimumRequests && state.failures*100 >= requests*m.circuit.FailurePercent {
		state.circuit = CircuitOpen
		state.openedAt = now
	}
}

func countsForCircuit(observation Observation) bool {
	if observation.Success {
		return false
	}
	switch observation.Category {
	case "auth", "client", "duplicate_risk":
		return false
	default:
		return true
	}
}

func (m *Manager) Snapshot() PoolStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := PoolStatus{Strategy: m.strategy, Targets: make([]TargetStatus, 0, len(m.order))}
	for _, id := range m.order {
		state := m.targets[id]
		if state == nil {
			continue
		}
		observations := state.failures + state.successes
		errorRate := 0.0
		if observations > 0 {
			errorRate = float64(state.failures) / float64(observations)
		}
		rateLimitRate := 0.0
		if observations > 0 {
			rateLimitRate = float64(state.rateLimitFailures) / float64(observations)
		}
		result.Targets = append(result.Targets, TargetStatus{Target: state.cfg, State: state.circuit, Active: state.active, FailureCount: state.failures, SuccessCount: state.successes, LastFailureAt: state.lastFailureAt, LastSuccessAt: state.lastSuccessAt, LastLatencyMs: state.lastLatency.Milliseconds(), LastErrorClass: state.lastErrorClass, HalfOpenLeases: state.halfOpenLeases, ErrorRate: errorRate, RateLimitRate: rateLimitRate})
	}
	return result
}

func (m *Manager) candidatesLocked(selection SelectionContext) []*targetState {
	now := m.now()
	result := make([]*targetState, 0, len(m.targets))
	for _, id := range m.order {
		if m.strategy == "primary-only" && id != m.order[0] {
			continue
		}
		state := m.targets[id]
		if state == nil {
			continue
		}
		if state.circuit == CircuitOpen {
			if now.Sub(state.openedAt) < m.circuit.OpenDuration.Duration {
				continue
			}
			if m.circuit.Enabled && state.halfOpenLeases >= m.circuit.HalfOpenMax {
				continue
			}
			state.circuit = CircuitHalfOpen
		}
		if state.circuit == CircuitHalfOpen && state.halfOpenLeases >= m.circuit.HalfOpenMax {
			continue
		}
		if selection.PreviousTargetID != "" && selection.WroteRequest && selection.IdempotencyKey == "" && state.cfg.ID != selection.PreviousTargetID {
			continue
		}
		if selection.PreviousDomain != "" && selection.WroteRequest && !selection.AllowCrossDomain && state.cfg.IdempotencyDomain != selection.PreviousDomain {
			continue
		}
		result = append(result, state)
	}
	if selection.PreferredTargetID != "" {
		for _, candidate := range result {
			if candidate.cfg.ID == selection.PreferredTargetID {
				return []*targetState{candidate}
			}
		}
		if selection.RequirePreferredTarget {
			return nil
		}
	}
	if m.strategy == "weighted-priority" && selection.PreviousTargetID != "" && len(result) > 1 && canChangeTarget(selection) {
		alternatives := result[:0]
		for _, candidate := range result {
			if candidate.cfg.ID != selection.PreviousTargetID {
				alternatives = append(alternatives, candidate)
			}
		}
		if len(alternatives) > 0 {
			result = alternatives
		}
	}
	return result
}

func canChangeTarget(selection SelectionContext) bool {
	if !selection.WroteRequest {
		return true
	}
	return selection.IdempotencyKey != ""
}

func (m *Manager) safetyBlockedLocked(selection SelectionContext) bool {
	if !selection.WroteRequest || len(m.targets) == 0 {
		return false
	}
	for _, id := range m.order {
		state := m.targets[id]
		if state == nil {
			continue
		}
		if selection.IdempotencyKey == "" && state.cfg.ID != selection.PreviousTargetID {
			return true
		}
		if !selection.AllowCrossDomain && selection.PreviousDomain != "" && state.cfg.IdempotencyDomain != selection.PreviousDomain {
			return true
		}
	}
	return false
}

func (m *Manager) chooseLocked(candidates []*targetState) *targetState {
	if len(candidates) == 0 {
		return nil
	}
	available := make([]*targetState, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.cfg.MaxActive <= 0 || candidate.active < candidate.cfg.MaxActive {
			available = append(available, candidate)
		}
	}
	if len(available) == 0 {
		return nil
	}
	minPriority := available[0].cfg.Priority
	for _, candidate := range available[1:] {
		if candidate.cfg.Priority < minPriority {
			minPriority = candidate.cfg.Priority
		}
	}
	preferred := make([]*targetState, 0, len(candidates))
	for _, candidate := range available {
		if candidate.cfg.Priority == minPriority {
			preferred = append(preferred, candidate)
		}
	}
	weight := 0
	for _, candidate := range preferred {
		weight += max(candidate.cfg.Weight, 1)
	}
	if weight <= 0 {
		return preferred[0]
	}
	point := int(m.cursor % uint64(weight))
	m.cursor++
	for _, candidate := range preferred {
		point -= max(candidate.cfg.Weight, 1)
		if point < 0 {
			return candidate
		}
	}
	return preferred[len(preferred)-1]
}

func (m *Manager) rebuildOrderLocked() {
	m.order = m.order[:0]
	for id := range m.targets {
		m.order = append(m.order, id)
	}
	sort.Slice(m.order, func(i, j int) bool {
		left, right := m.targets[m.order[i]], m.targets[m.order[j]]
		if left.cfg.Priority != right.cfg.Priority {
			return left.cfg.Priority < right.cfg.Priority
		}
		return left.cfg.ID < right.cfg.ID
	})
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func newTargetHTTPClient(cfg config.UpstreamConfig, policy egress.Policy) (*http.Client, *http.Transport) {
	dialer := &net.Dialer{Timeout: cfg.ConnectTimeout.Duration, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment, DialContext: dialer.DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 32, MaxIdleConnsPerHost: 16,
		IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: cfg.ResponseHeaderTimeout.Duration,
	}
	transport = policy.Transport(transport)
	return &http.Client{Transport: transport}, transport
}
