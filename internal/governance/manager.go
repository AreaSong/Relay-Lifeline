package governance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/telemetry"
)

const (
	UnknownUsageObserve = "observe"
	UnknownUsageDeny    = "deny"
)

var (
	ErrConcurrentLimit      = errors.New("governance concurrent limit exceeded")
	ErrRateLimit            = errors.New("governance request rate limit exceeded")
	ErrTokenLimit           = errors.New("governance token limit exceeded")
	ErrCostLimit            = errors.New("governance cost limit exceeded")
	ErrUnknownUsage         = errors.New("governance usage is unknown")
	ErrTenantContext        = errors.New("governance tenant context is required")
	ErrLedgerUnavailable    = errors.New("governance ledger unavailable")
	ErrReservationClosed    = errors.New("governance reservation is closed")
	ErrDuplicateReservation = errors.New("governance reservation already exists")
)

type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	Known        bool
}

// AdmissionContext carries stable dimensions known before an upstream call.
// Upstream is optional because the first target is selected after admission.
type AdmissionContext struct {
	Principal string
	Tenant    string
	Model     string
	Upstream  string
	// RequestID and Attempt are optional correlation metadata. They are kept
	// out of budget identity, but are persisted so an interrupted admission can
	// be reconciled without guessing which request owned it.
	RequestID string
	Attempt   int
}

type Decision struct {
	Allowed                    bool
	Mode                       string
	Reason                     string
	Principal                  string
	Model                      string
	WindowStarted              time.Time
	Requests                   int
	Active                     int
	Tokens                     int64
	CostMicros                 int64
	ReservedTokens             int64
	ReservedCostMicros         int64
	SoftThreshold              bool
	EstimatedExhaustionMinutes float64
	BudgetScope                string
	PersistenceDegraded        bool
}

type Snapshot struct {
	Mode                       string           `json:"mode"`
	UnknownUsagePolicy         string           `json:"unknownUsagePolicy"`
	Principals                 int              `json:"principals"`
	Reservations               int              `json:"reservations"`
	Entries                    []PrincipalUsage `json:"entries"`
	Counters                   Counters         `json:"counters"`
	Ledger                     LedgerStatus     `json:"ledger"`
	SoftThreshold              bool             `json:"softThreshold"`
	EstimatedExhaustionMinutes float64          `json:"estimatedExhaustionMinutes,omitempty"`
}

type Counters struct {
	Admitted            uint64            `json:"admitted"`
	Rejected            map[string]uint64 `json:"rejected"`
	Settlements         uint64            `json:"settlements"`
	KnownSettlements    uint64            `json:"knownSettlements"`
	UnknownSettlements  uint64            `json:"unknownSettlements"`
	Reconciled          uint64            `json:"reconciled"`
	PersistenceFailures uint64            `json:"persistenceFailures"`
}

type LedgerStatus struct {
	Enabled      bool      `json:"enabled"`
	Healthy      bool      `json:"healthy"`
	State        string    `json:"state,omitempty"`
	FailedAt     time.Time `json:"failedAt,omitempty"`
	FailedStage  string    `json:"failedStage,omitempty"`
	FailureCount uint64    `json:"failureCount,omitempty"`
}

type PrincipalUsage struct {
	Scope              string    `json:"scope"`
	Key                string    `json:"key"`
	Principal          string    `json:"principal"`
	WindowStarted      time.Time `json:"windowStarted"`
	Requests           int       `json:"requests"`
	Active             int       `json:"active"`
	Tokens             int64     `json:"tokens"`
	CostMicros         int64     `json:"costMicros"`
	ReservedTokens     int64     `json:"reservedTokens"`
	ReservedCostMicros int64     `json:"reservedCostMicros"`
	UnknownUsage       int       `json:"unknownUsage"`
}

type price struct {
	inputMicros  int64
	outputMicros int64
}

type usageState struct {
	windowStarted      time.Time
	requests           int
	tokens             int64
	costMicros         int64
	reservedTokens     int64
	reservedCostMicros int64
	unknownUsage       int
}

type reservationEstimate struct {
	tokens     int64
	costMicros int64
	samples    int
}

type attemptState struct {
	id                  string
	windowStarted       time.Time
	reservedTokens      int64
	reservedCostMicros  int64
	reservationReleased bool
	settled             bool
	budgetKeys          []string
}

type reservationState struct {
	id                  string
	principal           string
	tenant              string
	upstream            string
	model               string
	requestID           string
	startedAt           time.Time
	windowStarted       time.Time
	reservedTokens      int64
	reservedCostMicros  int64
	reservationReleased bool
	settlements         map[string]int64
	budgetKeys          []string
	attempts            map[string]*attemptState
}

type Manager struct {
	mu              sync.Mutex
	config          config.GovernanceConfig
	prices          map[string]price
	states          map[string]*usageState
	dimensionStates map[string]*usageState
	// states and dimensionStates retain the current-window pointers for
	// compatibility with the original implementation. The window maps prevent
	// a rollover from overwriting state still referenced by an active attempt.
	principalWindows map[string]map[int64]*usageState
	dimensionWindows map[string]map[int64]*usageState
	estimates        map[string]*reservationEstimate
	reservations     map[string]*reservationState
	ledger           *journal.Store
	counters         Counters
	now              func() time.Time
}

type Reservation struct {
	manager *Manager
	id      string
	ctx     context.Context
}

func New(cfg config.GovernanceConfig) *Manager {
	manager, _ := newManager(cfg, nil)
	return manager
}

func NewPersistent(cfg config.GovernanceConfig, ledger *journal.Store) (*Manager, error) {
	return newManager(cfg, ledger)
}

func newManager(cfg config.GovernanceConfig, ledger *journal.Store) (*Manager, error) {
	manager := &Manager{
		states:           make(map[string]*usageState),
		dimensionStates:  make(map[string]*usageState),
		principalWindows: make(map[string]map[int64]*usageState),
		dimensionWindows: make(map[string]map[int64]*usageState),
		estimates:        make(map[string]*reservationEstimate),
		reservations:     make(map[string]*reservationState),
		ledger:           ledger,
		now:              time.Now,
		counters:         Counters{Rejected: make(map[string]uint64)},
	}
	manager.Apply(cfg)
	if ledger != nil {
		manager.mu.Lock()
		err := manager.replayLocked(ledger.Entries())
		manager.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	return manager, nil
}

func (m *Manager) Apply(cfg config.GovernanceConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.UnknownUsagePolicy == "" {
		cfg.UnknownUsagePolicy = UnknownUsageObserve
	}
	m.config = cfg
	m.prices = make(map[string]price, len(cfg.Prices))
	for _, item := range cfg.Prices {
		m.prices[item.Model] = price{inputMicros: item.InputMicrosPerToken, outputMicros: item.OutputMicrosPerToken}
	}
}

func (m *Manager) Admit(ctx context.Context, principal, model string) (*Reservation, Decision, error) {
	id, err := newReservationID()
	if err != nil {
		return nil, Decision{Allowed: false, Reason: "reservation_id"}, fmt.Errorf("create governance reservation: %w", err)
	}
	return m.AdmitWithContext(ctx, id, AdmissionContext{Principal: principal, Model: model})
}

func (m *Manager) AdmitContext(ctx context.Context, admission AdmissionContext) (*Reservation, Decision, error) {
	id, err := newReservationID()
	if err != nil {
		return nil, Decision{Allowed: false, Reason: "reservation_id"}, fmt.Errorf("create governance reservation: %w", err)
	}
	return m.AdmitWithContext(ctx, id, admission)
}

func (m *Manager) AdmitWithID(ctx context.Context, id, principal, model string) (*Reservation, Decision, error) {
	return m.AdmitWithContext(ctx, id, AdmissionContext{Principal: principal, Model: model})
}

func (m *Manager) AdmitWithContext(ctx context.Context, id string, admission AdmissionContext) (*Reservation, Decision, error) {
	ctx, span := telemetry.Tracer("relay-lifeline/governance").Start(ctx, "relay.governance.admit")
	defer span.End()
	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "context canceled")
		return nil, Decision{Allowed: false, Reason: "canceled"}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 {
		span.SetStatus(codes.Error, "invalid reservation id")
		return nil, Decision{Allowed: false, Reason: "reservation_id"}, errors.New("invalid governance reservation id")
	}
	principal := strings.TrimSpace(admission.Principal)
	if principal == "" {
		principal = "anonymous"
	}
	admission.Principal = principal
	admission.Tenant = strings.TrimSpace(admission.Tenant)
	admission.Model = strings.TrimSpace(admission.Model)
	admission.Upstream = strings.TrimSpace(admission.Upstream)
	admission.RequestID = strings.TrimSpace(admission.RequestID)
	model := strings.TrimSpace(admission.Model)
	m.mu.Lock()
	defer m.mu.Unlock()
	span.SetAttributes(attribute.String("relay.governance.mode", m.config.Mode))
	if m.config.Mode == "enforce" && m.tenantContextRequiredLocked() && admission.Tenant == "" {
		decision := Decision{Allowed: false, Mode: m.config.Mode, Principal: principal, Model: model, Reason: "tenant_required", BudgetScope: "tenant"}
		m.counters.Rejected[decision.Reason]++
		span.SetStatus(codes.Error, decision.Reason)
		telemetry.RecordGovernanceAdmission(ctx, false, decision.Reason)
		return nil, decision, ErrTenantContext
	}
	if _, exists := m.reservations[id]; exists {
		span.SetStatus(codes.Error, "duplicate reservation")
		return nil, Decision{Allowed: false, Reason: "duplicate_reservation"}, ErrDuplicateReservation
	}
	now := m.now().UTC()
	state := m.stateLocked(principal, now)
	keys := m.budgetKeysLocked(admission, principal, model)
	decision := m.decisionLocked(principal, model, state, keys)
	if !decision.Allowed {
		m.counters.Rejected[decision.Reason]++
		span.SetAttributes(attribute.String("relay.governance.reason", decision.Reason))
		span.SetStatus(codes.Error, decision.Reason)
		telemetry.RecordGovernanceAdmission(ctx, false, decision.Reason)
		return nil, decision, reasonError(decision.Reason)
	}
	initialAttemptID := "attempt-1"
	payload := reservedEvent{
		Principal: principal, Tenant: admission.Tenant, Upstream: admission.Upstream, Model: model,
		RequestID: admission.RequestID, AttemptID: initialAttemptID,
		WindowStarted: state.windowStarted, StartedAt: now,
		ReservedTokens: decision.ReservedTokens, ReservedCostMicros: decision.ReservedCostMicros,
		BudgetKeys: append([]string(nil), keys...),
	}
	if err := m.appendLocked(ctx, id, eventReserved, payload); err != nil {
		decision.PersistenceDegraded = true
		if m.config.Mode == "enforce" {
			decision.Allowed, decision.Reason = false, "ledger_unavailable"
			m.counters.Rejected[decision.Reason]++
			span.RecordError(err)
			span.SetStatus(codes.Error, decision.Reason)
			telemetry.RecordGovernanceAdmission(ctx, false, decision.Reason)
			return nil, decision, ErrLedgerUnavailable
		}
	}
	reservation := &reservationState{
		id: id, principal: principal, tenant: admission.Tenant, upstream: admission.Upstream,
		model: model, requestID: admission.RequestID, startedAt: now, windowStarted: state.windowStarted,
		reservedTokens: decision.ReservedTokens, reservedCostMicros: decision.ReservedCostMicros,
		settlements: make(map[string]int64), budgetKeys: append([]string(nil), keys...),
		attempts: make(map[string]*attemptState),
	}
	attempt := &attemptState{id: initialAttemptID, windowStarted: state.windowStarted, reservedTokens: decision.ReservedTokens, reservedCostMicros: decision.ReservedCostMicros, budgetKeys: append([]string(nil), keys...)}
	reservation.attempts[initialAttemptID] = attempt
	m.applyAttemptReservationLocked(reservation, attempt)
	m.reservations[id] = reservation
	m.counters.Admitted++
	decision.Requests, decision.Active = state.requests, m.activeLocked(principal)
	span.SetStatus(codes.Ok, "")
	telemetry.RecordGovernanceAdmission(ctx, true, "")
	telemetry.RecordGovernanceActive(ctx, 1)
	return &Reservation{manager: m, id: id, ctx: ctx}, decision, nil
}

func (r *Reservation) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// BindUpstream attaches the concrete upstream selected after admission. An
// upstream-scoped budget is intentionally not considered bound until this
// method succeeds; policy recommendations are not a substitute for the
// actual lease selected by the gateway.
func (r *Reservation) BindUpstream(upstream string) error {
	_, err := r.BindUpstreamWithDecision(upstream)
	return err
}

// BindUpstreamWithDecision is the diagnostic form of BindUpstream. Existing
// callers may use BindUpstream and ignore the returned decision.
func (r *Reservation) BindUpstreamWithDecision(upstream string) (Decision, error) {
	if r == nil || r.manager == nil {
		return Decision{Allowed: false, Reason: "reservation_closed"}, ErrReservationClosed
	}
	upstream = strings.TrimSpace(upstream)
	if upstream == "" || len(upstream) > 128 {
		return Decision{Allowed: false, Reason: "upstream"}, errors.New("invalid governance upstream")
	}
	m := r.manager
	ctx, span := telemetry.Tracer("relay-lifeline/governance").Start(r.ctx, "relay.governance.bind_upstream")
	defer span.End()
	m.mu.Lock()
	defer m.mu.Unlock()
	reservation := m.reservations[r.id]
	if reservation == nil {
		return Decision{Allowed: false, Reason: "reservation_closed"}, ErrReservationClosed
	}
	if reservation.upstream == upstream {
		return m.reservationDecisionLocked(reservation), nil
	}
	admission := AdmissionContext{Principal: reservation.principal, Tenant: reservation.tenant, Model: reservation.model, Upstream: upstream, RequestID: reservation.requestID}
	newKeys := m.budgetKeysLocked(admission, reservation.principal, reservation.model)
	amounts, failureReason, ok := m.bindingAmountsLocked(reservation, newKeys)
	if !ok {
		decision := m.reservationDecisionLocked(reservation)
		decision.Allowed = false
		if failureReason == "" {
			failureReason = "budget_limit"
		}
		decision.Reason = failureReason
		return decision, reasonError(decision.Reason)
	}
	bound := boundEvent{Principal: reservation.principal, Upstream: upstream, BudgetKeys: append([]string(nil), newKeys...), BoundAt: m.now().UTC(), AttemptReservations: amounts}
	if err := m.appendLocked(ctx, r.id, eventBound, bound); err != nil {
		if m.config.Mode == "enforce" {
			return Decision{Allowed: false, Reason: "ledger_unavailable"}, ErrLedgerUnavailable
		}
	}
	m.rebindReservationLocked(reservation, newKeys, amounts)
	reservation.upstream = upstream
	reservation.budgetKeys = append([]string(nil), newKeys...)
	return m.reservationDecisionLocked(reservation), nil
}

// Bind is a short compatibility alias used by callers that model the
// reservation as a lease.
func (r *Reservation) Bind(upstream string) error { return r.BindUpstream(upstream) }

// BeginAttempt reserves one additional retry attempt. The optional upstream
// argument allows a caller to bind the concrete target and reserve atomically
// at the retry boundary.
func (r *Reservation) BeginAttempt(attemptID string, upstream ...string) (Decision, error) {
	if r == nil || r.manager == nil {
		return Decision{Allowed: false, Reason: "reservation_closed"}, ErrReservationClosed
	}
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" || len(attemptID) > 128 {
		return Decision{Allowed: false, Reason: "attempt_id"}, errors.New("invalid governance attempt id")
	}
	if len(upstream) > 0 && strings.TrimSpace(upstream[0]) != "" {
		if _, err := r.BindUpstreamWithDecision(upstream[0]); err != nil {
			return Decision{Allowed: false, Reason: "upstream"}, err
		}
	}
	m := r.manager
	ctx, span := telemetry.Tracer("relay-lifeline/governance").Start(r.ctx, "relay.governance.begin_attempt")
	defer span.End()
	m.mu.Lock()
	defer m.mu.Unlock()
	reservation := m.reservations[r.id]
	if reservation == nil {
		return Decision{Allowed: false, Reason: "reservation_closed"}, ErrReservationClosed
	}
	if existing := reservation.attempts[attemptID]; existing != nil {
		if !existing.reservationReleased && !existing.settled {
			return m.reservationDecisionLocked(reservation), nil
		}
		return Decision{Allowed: false, Reason: "duplicate_attempt"}, ErrDuplicateReservation
	}
	now := m.now().UTC()
	state := m.stateLocked(reservation.principal, now)
	keys := append([]string(nil), reservation.budgetKeys...)
	decision := m.attemptDecisionLocked(reservation, state, keys)
	if !decision.Allowed {
		m.counters.Rejected[decision.Reason]++
		return decision, reasonError(decision.Reason)
	}
	attempt := &attemptState{id: attemptID, windowStarted: state.windowStarted, reservedTokens: decision.ReservedTokens, reservedCostMicros: decision.ReservedCostMicros, budgetKeys: keys}
	payload := attemptReservedEvent{Principal: reservation.principal, Model: reservation.model, AttemptID: attemptID, WindowStarted: state.windowStarted, ReservedTokens: decision.ReservedTokens, ReservedCostMicros: decision.ReservedCostMicros, BudgetKeys: keys, ReservedAt: now}
	if err := m.appendLocked(ctx, r.id, eventAttemptReserved, payload); err != nil && m.config.Mode == "enforce" {
		return Decision{Allowed: false, Reason: "ledger_unavailable"}, ErrLedgerUnavailable
	}
	reservation.attempts[attemptID] = attempt
	reservation.reservationReleased = false
	reservation.reservedTokens = safeAdd64(reservation.reservedTokens, attempt.reservedTokens)
	reservation.reservedCostMicros = safeAdd64(reservation.reservedCostMicros, attempt.reservedCostMicros)
	m.applyAttemptReservationLocked(reservation, attempt)
	return decision, nil
}

// ReserveAttempt is an explicit alias for BeginAttempt.
func (r *Reservation) ReserveAttempt(attemptID string, upstream ...string) (Decision, error) {
	return r.BeginAttempt(attemptID, upstream...)
}

// BeginAttemptWithUpstream makes the intended retry call shape explicit while
// retaining the variadic BeginAttempt form for older integrations.
func (r *Reservation) BeginAttemptWithUpstream(attemptID, upstream string) (Decision, error) {
	return r.BeginAttempt(attemptID, upstream)
}

func (r *Reservation) Record(settlementID string, usage Usage) (int64, error) {
	if r == nil || r.manager == nil {
		return 0, ErrReservationClosed
	}
	ctx, span := telemetry.Tracer("relay-lifeline/governance").Start(r.ctx, "relay.governance.settle")
	defer span.End()
	settlementID = strings.TrimSpace(settlementID)
	if settlementID == "" || len(settlementID) > 128 {
		span.SetStatus(codes.Error, "invalid settlement id")
		return 0, errors.New("invalid governance settlement id")
	}
	m := r.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	reservation := m.reservations[r.id]
	if reservation == nil {
		span.SetStatus(codes.Error, "reservation closed")
		return 0, ErrReservationClosed
	}
	if cost, exists := reservation.settlements[settlementID]; exists {
		span.SetAttributes(attribute.Bool("relay.governance.idempotent_replay", true))
		span.SetStatus(codes.Ok, "")
		return cost, nil
	}
	now := m.now().UTC()
	attempt := reservation.attempts[settlementID]
	if attempt == nil {
		// Existing gateway integrations use attempt-N IDs but may not call
		// BeginAttempt explicitly. Resolve the initial attempt only for the
		// initial settlement; every later attempt gets its own reservation.
		if settlementID == "attempt-1" {
			if first := reservation.attempts["attempt-1"]; first != nil && !first.settled && !first.reservationReleased {
				attempt = first
			}
		}
		if attempt == nil {
			var reserveErr error
			attempt, reserveErr = m.beginAttemptLocked(ctx, reservation, settlementID, now)
			if reserveErr != nil && m.config.Mode == "enforce" {
				span.RecordError(reserveErr)
				span.SetStatus(codes.Error, "attempt reservation unavailable")
				return 0, reserveErr
			}
		}
	}
	window := attempt.windowStarted
	state := m.stateForPrincipalWindowLocked(reservation.principal, window)
	total, cost := m.usageCostLocked(reservation.model, usage)
	payload := usageRecordedEvent{
		SettlementID: settlementID, Principal: reservation.principal, Model: reservation.model,
		AttemptID: attempt.id, WindowStarted: window, Known: usage.Known, InputTokens: max64(usage.InputTokens, 0),
		OutputTokens: max64(usage.OutputTokens, 0), TotalTokens: total, CostMicros: cost, RecordedAt: now,
	}
	appendErr := m.appendLocked(ctx, r.id, eventUsageRecorded, payload)
	if appendErr != nil && m.config.Mode == "enforce" {
		span.RecordError(appendErr)
		span.SetStatus(codes.Error, "ledger unavailable")
		return 0, ErrLedgerUnavailable
	}
	m.releaseAttemptLocked(reservation, attempt)
	m.applyUsageLocked(state, payload)
	for _, key := range attempt.budgetKeys {
		if budgetKeyScope(key) != "principal" {
			m.applyUsageLocked(m.dimensionStateLocked(key, window), payload)
		}
	}
	if usage.Known {
		m.updateEstimateLocked(reservation.model, total, cost)
	}
	reservation.settlements[settlementID] = cost
	attempt.settled = true
	m.counters.Settlements++
	if usage.Known {
		m.counters.KnownSettlements++
	} else {
		m.counters.UnknownSettlements++
	}
	span.SetAttributes(attribute.Bool("relay.usage.known", usage.Known), attribute.Int64("relay.usage.tokens", total), attribute.Int64("relay.usage.cost_micros", cost))
	if appendErr != nil {
		span.RecordError(appendErr)
		span.SetStatus(codes.Error, "ledger degraded")
	} else {
		span.SetStatus(codes.Ok, "")
	}
	telemetry.RecordGovernanceSettlement(ctx, usage.Known, total, cost)
	return cost, appendErr
}

func (r *Reservation) Release() error {
	if r == nil || r.manager == nil {
		return nil
	}
	m := r.manager
	ctx, span := telemetry.Tracer("relay-lifeline/governance").Start(r.ctx, "relay.governance.release")
	defer span.End()
	m.mu.Lock()
	defer m.mu.Unlock()
	reservation := m.reservations[r.id]
	if reservation == nil {
		span.SetStatus(codes.Ok, "")
		return nil
	}
	err := m.appendLocked(ctx, r.id, eventReleased, releasedEvent{Principal: reservation.principal, ReleasedAt: m.now().UTC()})
	if err != nil && m.config.Mode == "enforce" {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ledger unavailable")
		return ErrLedgerUnavailable
	}
	m.releaseReservationLocked(reservation)
	delete(m.reservations, r.id)
	telemetry.RecordGovernanceActive(ctx, -1)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ledger degraded")
	} else {
		span.SetStatus(codes.Ok, "")
	}
	return err
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	active := make(map[string]int)
	for _, reservation := range m.reservations {
		active[reservation.principal] = m.activeLocked(reservation.principal)
	}
	entries := make([]PrincipalUsage, 0, len(m.states)+len(m.dimensionWindows))
	softThreshold := false
	forecast := 0.0
	principals := make(map[string]struct{}, len(m.states))
	for principal := range m.states {
		principals[principal] = struct{}{}
	}
	for _, reservation := range m.reservations {
		principals[reservation.principal] = struct{}{}
	}
	for principal := range principals {
		m.stateLocked(principal, now)
	}
	for principal, state := range m.states {
		if !sameWindow(state.windowStarted, now) && active[principal] == 0 {
			continue
		}
		entries = append(entries, PrincipalUsage{
			Scope: "principal", Key: principal, Principal: principal, WindowStarted: state.windowStarted, Requests: state.requests, Active: active[principal],
			Tokens: state.tokens, CostMicros: state.costMicros, ReservedTokens: state.reservedTokens, ReservedCostMicros: state.reservedCostMicros, UnknownUsage: state.unknownUsage,
		})
		decision := m.evaluateDecisionLimitsLocked(Decision{Allowed: true}, state.windowStarted, []string{canonicalBudgetKey("principal", principal)}, 0, 0, 0, nil)
		softThreshold = softThreshold || decision.SoftThreshold
		forecast = minForecast(forecast, decision.EstimatedExhaustionMinutes)
	}
	principalCount := len(entries)
	currentWindow := now.Truncate(time.Minute)
	for key, bucket := range m.dimensionWindows {
		state := bucket[currentWindow.UnixNano()]
		if state == nil || budgetKeyScope(key) == "principal" {
			continue
		}
		scope, value := parseBudgetKey(key)
		entries = append(entries, PrincipalUsage{
			Scope: scope, Key: value, WindowStarted: state.windowStarted, Requests: state.requests, Active: m.activeForBudgetLocked(key),
			Tokens: state.tokens, CostMicros: state.costMicros, ReservedTokens: state.reservedTokens, ReservedCostMicros: state.reservedCostMicros, UnknownUsage: state.unknownUsage,
		})
		decision := m.evaluateDecisionLimitsLocked(Decision{Allowed: true}, currentWindow, []string{key}, 0, 0, 0, nil)
		softThreshold = softThreshold || decision.SoftThreshold
		forecast = minForecast(forecast, decision.EstimatedExhaustionMinutes)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Scope != entries[j].Scope {
			return entries[i].Scope < entries[j].Scope
		}
		return entries[i].Key < entries[j].Key
	})
	return Snapshot{
		Mode: m.config.Mode, UnknownUsagePolicy: m.config.UnknownUsagePolicy, Principals: principalCount,
		Reservations: len(m.reservations), Entries: entries, Counters: cloneCounters(m.counters), Ledger: m.ledgerStatusLocked(), SoftThreshold: softThreshold, EstimatedExhaustionMinutes: forecast,
	}
}

// Compact compacts the usage ledger while retaining active reservations. A
// heartbeat is written for each active reservation first, making its entity
// newer than the cutoff; journal compaction can therefore remove settled
// history without dropping an in-flight reservation needed for replay.
func (m *Manager) Compact(ctx context.Context, cutoff time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ledger == nil {
		return 0, nil
	}
	now := m.now().UTC()
	for id, reservation := range m.reservations {
		if reservation == nil || allAttemptsReleased(reservation) {
			continue
		}
		event := heartbeatEvent{Principal: reservation.principal, RecordedAt: now}
		if err := m.appendLocked(ctx, id, eventHeartbeat, event); err != nil {
			if m.config.Mode == "enforce" {
				return 0, ErrLedgerUnavailable
			}
		}
	}
	return m.ledger.Compact(cutoff)
}

func PrincipalFromRequest(request *http.Request) string {
	if request == nil {
		return "anonymous"
	}
	value := request.Header.Get("Authorization")
	if value == "" {
		return "anonymous"
	}
	digest := sha256.Sum256([]byte(value))
	return "bearer:" + hex.EncodeToString(digest[:])[:16]
}

func (m *Manager) decisionLocked(principal, model string, state *usageState, keys []string) Decision {
	reservedTokens, reservedCost := m.reservationAmountsForKeysLocked(model, keys)
	decision := Decision{
		Allowed: true, Mode: m.config.Mode, Principal: principal, Model: model, WindowStarted: state.windowStarted,
		Requests: state.requests, Active: m.activeForBudgetLocked(keys[0]), Tokens: state.tokens, CostMicros: state.costMicros,
		ReservedTokens: reservedTokens, ReservedCostMicros: reservedCost,
	}
	if m.config.Mode == "enforce" && m.ledger != nil && m.ledger.Health() != nil {
		decision.Allowed, decision.Reason = false, "ledger_unavailable"
	}
	decision = m.evaluateDecisionLimitsLocked(decision, state.windowStarted, keys, reservedTokens, reservedCost, 1, nil)
	return decision
}

func (m *Manager) reservationAmountsLocked(model string) (int64, int64) {
	return m.reservationAmountsForKeysLocked(model, []string{canonicalBudgetKey("principal", "*")})
}

func (m *Manager) reservationAmountsForKeysLocked(model string, keys []string) (int64, int64) {
	estimate := m.estimates[model]
	tokens := max64(m.config.TokenReservation, 0)
	adaptive := m.config.ReservationMaxTokens > 0 || m.config.ReservationMinTokens > 0 || m.config.ForecastWindow.Duration > 0
	if adaptive && tokens == 0 && estimate != nil && estimate.tokens > 0 {
		tokens = estimate.tokens
	}
	if tokens == 0 {
		tokens = max64(m.config.ReservationMinTokens, 1)
	}
	if m.config.ReservationMaxTokens > 0 {
		tokens = min64(tokens, m.config.ReservationMaxTokens)
	}
	if m.config.ReservationMinTokens > 0 {
		tokens = max64(tokens, m.config.ReservationMinTokens)
	}
	cost := max64(m.config.CostReservationMicros, 0)
	if adaptive && cost == 0 && estimate != nil && estimate.costMicros > 0 {
		cost = estimate.costMicros
	}
	if cost == 0 {
		p := m.prices[model]
		cost = safeMul64(tokens, max64(p.inputMicros, p.outputMicros))
	}
	if m.config.ReservationMaxCostMicros > 0 {
		cost = min64(cost, m.config.ReservationMaxCostMicros)
	}
	if m.config.ReservationMinCostMicros > 0 {
		cost = max64(cost, m.config.ReservationMinCostMicros)
	}
	// A scoped budget is a complete budget even when the global limit is zero.
	// Only suppress a reservation when no applicable dimension has that kind of
	// limit at all.
	tokenLimit, costLimit := int64(0), int64(0)
	for _, key := range keys {
		budget := m.budgetForKeyLocked(key)
		if budget.TokenLimit > 0 && (tokenLimit == 0 || budget.TokenLimit < tokenLimit) {
			tokenLimit = budget.TokenLimit
		}
		if budget.CostLimitMicros > 0 && (costLimit == 0 || budget.CostLimitMicros < costLimit) {
			costLimit = budget.CostLimitMicros
		}
	}
	if tokenLimit == 0 {
		tokens = 0
	} else {
		tokens = min64(tokens, tokenLimit)
	}
	if costLimit == 0 {
		cost = 0
	} else {
		cost = min64(cost, costLimit)
	}
	return tokens, cost
}

func (m *Manager) evaluateDecisionLimitsLocked(decision Decision, window time.Time, keys []string, reservedTokens, reservedCost int64, extraRequests int, exclude *reservationState) Decision {
	for _, key := range keys {
		budget := m.budgetForKeyLocked(key)
		budgetState := m.dimensionStateLocked(key, window)
		activeForBudget := m.activeForBudgetLockedExcluding(key, exclude)
		if m.config.Mode == "enforce" {
			switch {
			case decision.Allowed && budget.UnknownUsageDeny && budgetState.unknownUsage > 0:
				decision.Allowed, decision.Reason, decision.BudgetScope = false, "unknown_usage", displayBudgetKey(key)
			case decision.Allowed && budget.MaxConcurrent > 0 && activeForBudget+1 > budget.MaxConcurrent:
				decision.Allowed, decision.Reason, decision.BudgetScope = false, "concurrent_limit", displayBudgetKey(key)
			case decision.Allowed && budget.RequestsPerMinute > 0 && budgetState.requests+extraRequests > budget.RequestsPerMinute:
				decision.Allowed, decision.Reason, decision.BudgetScope = false, "rate_limit", displayBudgetKey(key)
			case decision.Allowed && budget.TokenLimit > 0 && budgetState.tokens+budgetState.reservedTokens+reservedTokens > budget.TokenLimit:
				decision.Allowed, decision.Reason, decision.BudgetScope = false, "token_limit", displayBudgetKey(key)
			case decision.Allowed && budget.CostLimitMicros > 0 && budgetState.costMicros+budgetState.reservedCostMicros+reservedCost > budget.CostLimitMicros:
				decision.Allowed, decision.Reason, decision.BudgetScope = false, "cost_limit", displayBudgetKey(key)
			}
		}
		usedTokens := budgetState.tokens + budgetState.reservedTokens + reservedTokens
		usedCost := budgetState.costMicros + budgetState.reservedCostMicros + reservedCost
		if softLimitReached(usedTokens, usedCost, budget.TokenLimit, budget.CostLimitMicros, m.config.SoftThresholdPercent) {
			decision.SoftThreshold = true
		}
		candidate := m.forecastLocked(budgetState, budget, reservedTokens, reservedCost)
		if candidate > 0 && (decision.EstimatedExhaustionMinutes == 0 || candidate < decision.EstimatedExhaustionMinutes) {
			decision.EstimatedExhaustionMinutes = candidate
		}
	}
	return decision
}

func (m *Manager) attemptDecisionLocked(reservation *reservationState, state *usageState, keys []string) Decision {
	tokens, cost := m.reservationAmountsForKeysLocked(reservation.model, keys)
	decision := Decision{Allowed: true, Mode: m.config.Mode, Principal: reservation.principal, Model: reservation.model, WindowStarted: state.windowStarted, Requests: state.requests, Active: m.activeForBudgetLocked(keys[0]), Tokens: state.tokens, CostMicros: state.costMicros, ReservedTokens: tokens, ReservedCostMicros: cost}
	return m.evaluateDecisionLimitsLocked(decision, state.windowStarted, keys, tokens, cost, 1, reservation)
}

func (m *Manager) reservationDecisionLocked(reservation *reservationState) Decision {
	if reservation == nil {
		return Decision{Allowed: false, Reason: "reservation_closed"}
	}
	state := m.stateForPrincipalWindowLocked(reservation.principal, reservation.windowStarted)
	decision := Decision{Allowed: true, Mode: m.config.Mode, Principal: reservation.principal, Model: reservation.model, WindowStarted: state.windowStarted, Requests: state.requests, Active: m.activeLocked(reservation.principal), Tokens: state.tokens, CostMicros: state.costMicros, ReservedTokens: reservation.reservedTokens, ReservedCostMicros: reservation.reservedCostMicros}
	return m.evaluateDecisionLimitsLocked(decision, state.windowStarted, reservation.budgetKeys, 0, 0, 0, reservation)
}

func (m *Manager) beginAttemptLocked(ctx context.Context, reservation *reservationState, attemptID string, now time.Time) (*attemptState, error) {
	state := m.stateLocked(reservation.principal, now)
	keys := append([]string(nil), reservation.budgetKeys...)
	decision := m.attemptDecisionLocked(reservation, state, keys)
	if !decision.Allowed {
		return nil, reasonError(decision.Reason)
	}
	attempt := &attemptState{id: attemptID, windowStarted: state.windowStarted, reservedTokens: decision.ReservedTokens, reservedCostMicros: decision.ReservedCostMicros, budgetKeys: keys}
	payload := attemptReservedEvent{Principal: reservation.principal, Model: reservation.model, AttemptID: attemptID, WindowStarted: state.windowStarted, ReservedTokens: attempt.reservedTokens, ReservedCostMicros: attempt.reservedCostMicros, BudgetKeys: keys, ReservedAt: now}
	err := m.appendLocked(ctx, reservation.id, eventAttemptReserved, payload)
	if err != nil && m.config.Mode == "enforce" {
		return nil, ErrLedgerUnavailable
	}
	reservation.attempts[attemptID] = attempt
	reservation.reservationReleased = false
	reservation.reservedTokens = safeAdd64(reservation.reservedTokens, attempt.reservedTokens)
	reservation.reservedCostMicros = safeAdd64(reservation.reservedCostMicros, attempt.reservedCostMicros)
	m.applyAttemptReservationLocked(reservation, attempt)
	return attempt, err
}

type budgetLimits struct {
	UnknownUsageDeny  bool
	MaxConcurrent     int
	RequestsPerMinute int
	TokenLimit        int64
	CostLimitMicros   int64
}

func (m *Manager) budgetKeysLocked(admission AdmissionContext, principal, model string) []string {
	keys := []string{canonicalBudgetKey("principal", principal)}
	seen := map[string]struct{}{keys[0]: {}}
	for _, budget := range m.config.Budgets {
		value := ""
		switch budget.Scope {
		case "principal":
			value = principal
		case "tenant":
			value = strings.TrimSpace(admission.Tenant)
		case "model":
			value = model
		case "upstream":
			value = strings.TrimSpace(admission.Upstream)
		}
		if value != "" && budgetMatches(budget.Key, value) {
			key := canonicalBudgetKey(budget.Scope, value)
			if _, exists := seen[key]; !exists {
				keys = append(keys, key)
				seen[key] = struct{}{}
			}
		}
	}
	return keys
}

func (m *Manager) tenantContextRequiredLocked() bool {
	for _, budget := range m.config.Budgets {
		if budget.Scope == "tenant" {
			return true
		}
	}
	return false
}

func (m *Manager) budgetForKeyLocked(key string) budgetLimits {
	scope, value := parseBudgetKey(key)
	limits := budgetLimits{UnknownUsageDeny: m.config.UnknownUsagePolicy == UnknownUsageDeny}
	if scope == "principal" {
		limits = mergeBudgetLimits(limits, budgetLimits{MaxConcurrent: m.config.MaxConcurrent, RequestsPerMinute: m.config.RequestsPerMinute, TokenLimit: m.config.TokenLimit, CostLimitMicros: m.config.CostLimitMicros})
	}
	for _, budget := range m.config.Budgets {
		if budget.Scope == scope && budgetMatches(budget.Key, value) {
			limits = mergeBudgetLimits(limits, budgetLimits{UnknownUsageDeny: m.config.UnknownUsagePolicy == UnknownUsageDeny, MaxConcurrent: budget.MaxConcurrent, RequestsPerMinute: budget.RequestsPerMinute, TokenLimit: budget.TokenLimit, CostLimitMicros: budget.CostLimitMicros})
		}
	}
	return limits
}

func (m *Manager) dimensionStateLocked(key string, window time.Time) *usageState {
	scope, value := parseBudgetKey(key)
	if scope == "principal" {
		return m.stateForPrincipalWindowLocked(value, window)
	}
	if m.dimensionWindows == nil {
		m.dimensionWindows = make(map[string]map[int64]*usageState)
	}
	window = window.UTC().Truncate(time.Minute)
	bucket := m.dimensionWindows[key]
	if bucket == nil {
		bucket = make(map[int64]*usageState)
		m.dimensionWindows[key] = bucket
	}
	state := bucket[window.UnixNano()]
	if state == nil {
		state = &usageState{windowStarted: window}
		bucket[window.UnixNano()] = state
	}
	if current, ok := m.dimensionStates[key]; !ok || sameWindow(current.windowStarted, m.now()) || current.windowStarted.Equal(window) {
		m.dimensionStates[key] = state
	}
	return state
}

func (m *Manager) activeForBudgetLocked(key string) int {
	return m.activeForBudgetLockedExcluding(key, nil)
}

func (m *Manager) activeForBudgetLockedExcluding(key string, exclude *reservationState) int {
	active := 0
	for _, reservation := range m.reservations {
		if reservation == exclude {
			continue
		}
		for _, attempt := range reservation.attempts {
			if attempt.reservationReleased || attempt.settled {
				continue
			}
			for _, budgetKey := range attempt.budgetKeys {
				if budgetKey == key {
					active++
					break
				}
			}
		}
	}
	return active
}

func softLimitReached(tokens, cost, tokenLimit, costLimit int64, percent int) bool {
	if percent <= 0 {
		return false
	}
	if tokenLimit > 0 && float64(tokens) >= float64(tokenLimit)*float64(percent)/100 {
		return true
	}
	return costLimit > 0 && float64(cost) >= float64(costLimit)*float64(percent)/100
}

func (m *Manager) forecastLocked(state *usageState, budget budgetLimits, reservedTokens, reservedCost int64) float64 {
	window := m.config.ForecastWindow.Duration
	if window <= 0 || state.requests <= 0 {
		return 0
	}
	elapsed := m.now().UTC().Sub(state.windowStarted)
	if elapsed <= 0 {
		return 0
	}
	minutes := elapsed.Minutes()
	if minutes <= 0 {
		return 0
	}
	tokensPerMinute := float64(state.tokens+state.reservedTokens+reservedTokens) / minutes
	costPerMinute := float64(state.costMicros+state.reservedCostMicros+reservedCost) / minutes
	remaining := 0.0
	if budget.TokenLimit > 0 && tokensPerMinute > 0 {
		remaining = float64(max64(budget.TokenLimit-state.tokens-state.reservedTokens, 0)) / tokensPerMinute
	}
	if budget.CostLimitMicros > 0 && costPerMinute > 0 {
		costRemaining := float64(max64(budget.CostLimitMicros-state.costMicros-state.reservedCostMicros, 0)) / costPerMinute
		if remaining == 0 || costRemaining < remaining {
			remaining = costRemaining
		}
	}
	// ForecastWindow is an explicit horizon, not merely an enable switch. A
	// budget that is not projected to exhaust within the configured horizon has
	// no actionable forecast signal.
	if remaining <= 0 || remaining > window.Minutes() {
		return 0
	}
	return remaining
}

func (m *Manager) releaseReservationLocked(reservation *reservationState) {
	if reservation == nil {
		return
	}
	for _, attempt := range reservation.attempts {
		m.releaseAttemptLocked(reservation, attempt)
	}
	reservation.reservedTokens = 0
	reservation.reservedCostMicros = 0
	reservation.reservationReleased = true
}

func (m *Manager) stateLocked(principal string, now time.Time) *usageState {
	window := now.UTC().Truncate(time.Minute)
	return m.stateForPrincipalWindowLocked(principal, window)
}

func (m *Manager) activeLocked(principal string) int {
	active := 0
	for _, reservation := range m.reservations {
		if reservation.principal == principal {
			for _, attempt := range reservation.attempts {
				if !attempt.reservationReleased && !attempt.settled {
					active++
				}
			}
		}
	}
	return active
}

func (m *Manager) stateForPrincipalWindowLocked(principal string, window time.Time) *usageState {
	window = window.UTC().Truncate(time.Minute)
	if m.principalWindows == nil {
		m.principalWindows = make(map[string]map[int64]*usageState)
	}
	bucket := m.principalWindows[principal]
	if bucket == nil {
		bucket = make(map[int64]*usageState)
		m.principalWindows[principal] = bucket
	}
	key := window.UnixNano()
	state := bucket[key]
	if state == nil {
		state = &usageState{windowStarted: window}
		bucket[key] = state
	}
	current := m.now().UTC().Truncate(time.Minute)
	if window.Equal(current) {
		m.states[principal] = state
	}
	return state
}

func (m *Manager) applyAttemptReservationLocked(reservation *reservationState, attempt *attemptState) {
	if reservation == nil || attempt == nil || attempt.reservationReleased {
		return
	}
	for _, key := range attempt.budgetKeys {
		state := m.dimensionStateLocked(key, attempt.windowStarted)
		state.requests++
		state.reservedTokens = safeAdd64(state.reservedTokens, attempt.reservedTokens)
		state.reservedCostMicros = safeAdd64(state.reservedCostMicros, attempt.reservedCostMicros)
	}
}

func (m *Manager) releaseAttemptLocked(reservation *reservationState, attempt *attemptState) {
	if reservation == nil || attempt == nil || attempt.reservationReleased {
		return
	}
	for _, key := range attempt.budgetKeys {
		state := m.dimensionStateLocked(key, attempt.windowStarted)
		if state == nil || !state.windowStarted.Equal(attempt.windowStarted) {
			continue
		}
		state.reservedTokens = max64(state.reservedTokens-attempt.reservedTokens, 0)
		state.reservedCostMicros = max64(state.reservedCostMicros-attempt.reservedCostMicros, 0)
	}
	reservation.reservedTokens = max64(reservation.reservedTokens-attempt.reservedTokens, 0)
	reservation.reservedCostMicros = max64(reservation.reservedCostMicros-attempt.reservedCostMicros, 0)
	attempt.reservationReleased = true
	if allAttemptsReleased(reservation) {
		reservation.reservationReleased = true
	}
}

func allAttemptsReleased(reservation *reservationState) bool {
	if reservation == nil {
		return true
	}
	for _, attempt := range reservation.attempts {
		if !attempt.reservationReleased && !attempt.settled {
			return false
		}
	}
	return true
}

func (m *Manager) moveReservationKeysLocked(reservation *reservationState, newKeys []string) {
	if reservation == nil {
		return
	}
	for _, attempt := range reservation.attempts {
		if attempt.reservationReleased || attempt.settled {
			continue
		}
		for _, key := range attempt.budgetKeys {
			state := m.dimensionStateLocked(key, attempt.windowStarted)
			state.requests = maxInt(state.requests-1, 0)
			state.reservedTokens = max64(state.reservedTokens-attempt.reservedTokens, 0)
			state.reservedCostMicros = max64(state.reservedCostMicros-attempt.reservedCostMicros, 0)
		}
		attempt.budgetKeys = append([]string(nil), newKeys...)
		m.applyAttemptReservationLocked(reservation, attempt)
	}
}

func (m *Manager) bindingFitsLocked(reservation *reservationState, newKeys []string) bool {
	if m.config.Mode != "enforce" {
		return true
	}
	for _, key := range newKeys {
		budget := m.budgetForKeyLocked(key)
		state := m.dimensionStateLocked(key, m.now().UTC())
		active := m.activeForBudgetLockedExcluding(key, reservation)
		requests, reservedTokens, reservedCost := state.requests, state.reservedTokens, state.reservedCostMicros
		for _, attempt := range reservation.attempts {
			if attempt.reservationReleased || attempt.settled || !attempt.windowStarted.Equal(state.windowStarted) {
				continue
			}
			active++
			requests++
			reservedTokens += attempt.reservedTokens
			reservedCost += attempt.reservedCostMicros
		}
		if budget.UnknownUsageDeny && state.unknownUsage > 0 || budget.MaxConcurrent > 0 && active > budget.MaxConcurrent || budget.RequestsPerMinute > 0 && requests > budget.RequestsPerMinute || budget.TokenLimit > 0 && state.tokens+reservedTokens > budget.TokenLimit || budget.CostLimitMicros > 0 && state.costMicros+reservedCost > budget.CostLimitMicros {
			return false
		}
	}
	return true
}

func (m *Manager) bindingAmountsLocked(reservation *reservationState, newKeys []string) ([]attemptReservationEvent, string, bool) {
	if reservation == nil {
		return nil, "reservation_closed", false
	}
	amounts := make([]attemptReservationEvent, 0, len(reservation.attempts))
	for _, attempt := range reservation.attempts {
		if attempt.reservationReleased || attempt.settled {
			continue
		}
		tokens, cost := m.reservationAmountsForKeysLocked(reservation.model, newKeys)
		amounts = append(amounts, attemptReservationEvent{AttemptID: attempt.id, ReservedTokens: tokens, ReservedCostMicros: cost})
	}
	// Evaluate the target dimensions using the amounts that will be applied,
	// excluding this reservation's old target accounting.
	for _, key := range newKeys {
		budget := m.budgetForKeyLocked(key)
		state := m.dimensionStateLocked(key, m.now().UTC())
		active := m.activeForBudgetLockedExcluding(key, reservation)
		requests := state.requests
		reservedTokens := state.reservedTokens
		reservedCost := state.reservedCostMicros
		for _, attempt := range reservation.attempts {
			if attempt.reservationReleased || attempt.settled || !attempt.windowStarted.Equal(state.windowStarted) {
				continue
			}
			active++
			requests++
			for _, amount := range amounts {
				if amount.AttemptID == attempt.id {
					reservedTokens += amount.ReservedTokens
					reservedCost += amount.ReservedCostMicros
					break
				}
			}
		}
		if m.config.Mode == "enforce" {
			switch {
			case budget.UnknownUsageDeny && state.unknownUsage > 0:
				return nil, "unknown_usage", false
			case budget.MaxConcurrent > 0 && active > budget.MaxConcurrent:
				return nil, "concurrent_limit", false
			case budget.RequestsPerMinute > 0 && requests > budget.RequestsPerMinute:
				return nil, "rate_limit", false
			case budget.TokenLimit > 0 && state.tokens+reservedTokens > budget.TokenLimit:
				return nil, "token_limit", false
			case budget.CostLimitMicros > 0 && state.costMicros+reservedCost > budget.CostLimitMicros:
				return nil, "cost_limit", false
			}
		}
	}
	return amounts, "", true
}

func (m *Manager) rebindReservationLocked(reservation *reservationState, newKeys []string, amounts []attemptReservationEvent) {
	if reservation == nil {
		return
	}
	amountByID := make(map[string]attemptReservationEvent, len(amounts))
	for _, amount := range amounts {
		amountByID[amount.AttemptID] = amount
	}
	for _, attempt := range reservation.attempts {
		if attempt.reservationReleased || attempt.settled {
			continue
		}
		// Remove old accounting without changing the attempt lifecycle.
		for _, key := range attempt.budgetKeys {
			state := m.dimensionStateLocked(key, attempt.windowStarted)
			state.requests = maxInt(state.requests-1, 0)
			state.reservedTokens = max64(state.reservedTokens-attempt.reservedTokens, 0)
			state.reservedCostMicros = max64(state.reservedCostMicros-attempt.reservedCostMicros, 0)
		}
		reservation.reservedTokens = max64(reservation.reservedTokens-attempt.reservedTokens, 0)
		reservation.reservedCostMicros = max64(reservation.reservedCostMicros-attempt.reservedCostMicros, 0)
		attempt.budgetKeys = append([]string(nil), newKeys...)
		if amount, ok := amountByID[attempt.id]; ok {
			attempt.reservedTokens = amount.ReservedTokens
			attempt.reservedCostMicros = amount.ReservedCostMicros
		}
		attempt.reservationReleased = false
		m.applyAttemptReservationLocked(reservation, attempt)
		reservation.reservedTokens = safeAdd64(reservation.reservedTokens, attempt.reservedTokens)
		reservation.reservedCostMicros = safeAdd64(reservation.reservedCostMicros, attempt.reservedCostMicros)
	}
	reservation.reservationReleased = allAttemptsReleased(reservation)
}

func (m *Manager) usageCostLocked(model string, usage Usage) (int64, int64) {
	if !usage.Known {
		return 0, 0
	}
	input := max64(usage.InputTokens, 0)
	output := max64(usage.OutputTokens, 0)
	componentTotal := safeAdd64(input, output)
	total := max64(usage.TotalTokens, componentTotal)
	modelPrice := m.prices[model]
	cost := safeAdd64(safeMul64(input, modelPrice.inputMicros), safeMul64(output, modelPrice.outputMicros))
	// Some upstreams expose only usage.total_tokens. Treat the missing split
	// conservatively by pricing the remainder at the more expensive direction;
	// a total-token event must never silently contribute zero cost.
	if total > componentTotal {
		unit := max64(modelPrice.inputMicros, modelPrice.outputMicros)
		cost = safeAdd64(cost, safeMul64(total-componentTotal, unit))
	}
	return max64(total, 0), cost
}

func (m *Manager) updateEstimateLocked(model string, total, cost int64) {
	estimate := m.estimates[model]
	if estimate == nil {
		estimate = &reservationEstimate{}
		m.estimates[model] = estimate
	}
	estimate.samples++
	if estimate.tokens == 0 {
		estimate.tokens = max64(total, 0)
	} else {
		estimate.tokens = safeAdd64(safeMul64(estimate.tokens, 3), max64(total, 0)) / 4
	}
	if estimate.costMicros == 0 {
		estimate.costMicros = max64(cost, 0)
	} else {
		estimate.costMicros = safeAdd64(safeMul64(estimate.costMicros, 3), max64(cost, 0)) / 4
	}
}

func (m *Manager) applyUsageLocked(state *usageState, event usageRecordedEvent) {
	if event.Known {
		state.tokens = safeAdd64(state.tokens, max64(event.TotalTokens, 0))
		state.costMicros = safeAdd64(state.costMicros, max64(event.CostMicros, 0))
	} else {
		state.unknownUsage++
	}
}

func (m *Manager) appendLocked(ctx context.Context, id, eventType string, payload any) error {
	if m.ledger == nil {
		return nil
	}
	if _, err := m.ledger.AppendContext(ctx, id, eventType, payload); err != nil {
		m.counters.PersistenceFailures++
		return fmt.Errorf("persist governance event %s: %w", eventType, err)
	}
	return nil
}

func (m *Manager) ledgerStatusLocked() LedgerStatus {
	if m.ledger == nil {
		return LedgerStatus{Healthy: true}
	}
	status := m.ledger.Status()
	return LedgerStatus{
		Enabled: true, Healthy: m.ledger.Health() == nil, State: string(status.State), FailedAt: status.FailedAt,
		FailedStage: status.FailedStage, FailureCount: status.FailureCount,
	}
}

func reasonError(reason string) error {
	switch reason {
	case "concurrent_limit":
		return ErrConcurrentLimit
	case "rate_limit":
		return ErrRateLimit
	case "token_limit":
		return ErrTokenLimit
	case "cost_limit":
		return ErrCostLimit
	case "unknown_usage":
		return ErrUnknownUsage
	case "tenant_required":
		return ErrTenantContext
	case "ledger_unavailable":
		return ErrLedgerUnavailable
	default:
		return errors.New("governance admission denied")
	}
}

func newReservationID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func cloneCounters(source Counters) Counters {
	result := source
	result.Rejected = make(map[string]uint64, len(source.Rejected))
	for reason, count := range source.Rejected {
		result.Rejected[reason] = count
	}
	return result
}

func sameWindow(window, now time.Time) bool {
	return window.Equal(now.UTC().Truncate(time.Minute))
}

func max64(value, minimum int64) int64 {
	if value > minimum {
		return value
	}
	return minimum
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func maxInt(value, minimum int) int {
	if value > minimum {
		return value
	}
	return minimum
}

// A NUL separator makes scope/value identities unambiguous even when a value
// itself contains ':' or other delimiters. Legacy colon keys are accepted by
// parseBudgetKey during ledger replay.
func canonicalBudgetKey(scope, value string) string {
	return strings.TrimSpace(scope) + "\x00" + value
}

func parseBudgetKey(key string) (string, string) {
	if parts := strings.SplitN(key, "\x00", 2); len(parts) == 2 {
		return parts[0], parts[1]
	}
	parts := strings.SplitN(key, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return key, ""
}

func normalizeBudgetKey(key string) string {
	scope, value := parseBudgetKey(key)
	if value == "" {
		return canonicalBudgetKey("principal", scope)
	}
	return canonicalBudgetKey(scope, value)
}

func normalizeBudgetKeys(keys []string, principal string) []string {
	if len(keys) == 0 {
		return []string{canonicalBudgetKey("principal", principal)}
	}
	result := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = normalizeBudgetKey(key)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func budgetKeyScope(key string) string {
	scope, _ := parseBudgetKey(key)
	return scope
}

func displayBudgetKey(key string) string {
	scope, value := parseBudgetKey(key)
	if value == "" {
		return scope
	}
	return scope + ":" + value
}

func budgetMatches(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == value {
		return true
	}
	if pattern == "" || value == "" {
		return false
	}
	// Small, deterministic glob matcher. '*' matches any sequence and '?'
	// matches one rune; no regular-expression features are accepted.
	if !strings.ContainsAny(pattern, "*?") {
		return false
	}
	return globMatch(pattern, value)
}

func globMatch(pattern, value string) bool {
	p := []rune(pattern)
	v := []rune(value)
	row := make([]bool, len(v)+1)
	row[0] = true
	for _, token := range p {
		next := make([]bool, len(v)+1)
		switch token {
		case '*':
			for j := 0; j <= len(v); j++ {
				if row[j] {
					for k := j; k <= len(v); k++ {
						next[k] = true
					}
				}
			}
		case '?':
			for j := 1; j <= len(v); j++ {
				if row[j-1] {
					next[j] = true
				}
			}
		default:
			for j := 1; j <= len(v); j++ {
				if row[j-1] && v[j-1] == token {
					next[j] = true
				}
			}
		}
		row = next
	}
	return row[len(v)]
}

func mergeBudgetLimits(left, right budgetLimits) budgetLimits {
	result := left
	result.UnknownUsageDeny = left.UnknownUsageDeny || right.UnknownUsageDeny
	result.MaxConcurrent = stricterInt(left.MaxConcurrent, right.MaxConcurrent)
	result.RequestsPerMinute = stricterInt(left.RequestsPerMinute, right.RequestsPerMinute)
	result.TokenLimit = stricterInt64(left.TokenLimit, right.TokenLimit)
	result.CostLimitMicros = stricterInt64(left.CostLimitMicros, right.CostLimitMicros)
	return result
}

func stricterInt(left, right int) int {
	if left == 0 {
		return right
	}
	if right == 0 || left < right {
		return left
	}
	return right
}

func stricterInt64(left, right int64) int64 {
	if left == 0 {
		return right
	}
	if right == 0 || left < right {
		return left
	}
	return right
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func minForecast(current, candidate float64) float64 {
	if candidate <= 0 {
		return current
	}
	if current == 0 || candidate < current {
		return candidate
	}
	return current
}

const maxInt64Value = int64(^uint64(0) >> 1)

func safeAdd64(left, right int64) int64 {
	if right > 0 && left > maxInt64Value-right {
		return maxInt64Value
	}
	if right < 0 && left < -maxInt64Value-1-right {
		return -maxInt64Value - 1
	}
	return left + right
}

func safeMul64(left, right int64) int64 {
	if left == 0 || right == 0 {
		return 0
	}
	if left == -1 && right == -maxInt64Value-1 || right == -1 && left == -maxInt64Value-1 {
		return maxInt64Value
	}
	negative := (left < 0) != (right < 0)
	a, b := left, right
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > maxInt64Value/b {
		if negative {
			return -maxInt64Value - 1
		}
		return maxInt64Value
	}
	result := a * b
	if negative {
		return -result
	}
	return result
}
