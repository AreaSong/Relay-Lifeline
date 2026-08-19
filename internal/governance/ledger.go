package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/areasong/relay-lifeline/internal/journal"
)

const (
	eventReserved        = "governance.reserved"
	eventAttemptReserved = "governance.attempt_reserved"
	eventBound           = "governance.bound"
	eventHeartbeat       = "governance.heartbeat"
	eventUsageRecorded   = "governance.usage_recorded"
	eventReleased        = "governance.released"
	eventReconciled      = "governance.reconciled"
)

type reservedEvent struct {
	Principal          string    `json:"principal"`
	Tenant             string    `json:"tenant,omitempty"`
	Upstream           string    `json:"upstream,omitempty"`
	Model              string    `json:"model"`
	RequestID          string    `json:"requestId,omitempty"`
	AttemptID          string    `json:"attemptId,omitempty"`
	WindowStarted      time.Time `json:"windowStarted"`
	StartedAt          time.Time `json:"startedAt"`
	ReservedTokens     int64     `json:"reservedTokens,omitempty"`
	ReservedCostMicros int64     `json:"reservedCostMicros,omitempty"`
	BudgetKeys         []string  `json:"budgetKeys,omitempty"`
}

type attemptReservedEvent struct {
	Principal          string    `json:"principal"`
	Model              string    `json:"model"`
	AttemptID          string    `json:"attemptId"`
	WindowStarted      time.Time `json:"windowStarted"`
	ReservedTokens     int64     `json:"reservedTokens,omitempty"`
	ReservedCostMicros int64     `json:"reservedCostMicros,omitempty"`
	BudgetKeys         []string  `json:"budgetKeys,omitempty"`
	ReservedAt         time.Time `json:"reservedAt"`
}

type boundEvent struct {
	Principal           string                    `json:"principal"`
	Upstream            string                    `json:"upstream"`
	BudgetKeys          []string                  `json:"budgetKeys,omitempty"`
	AttemptReservations []attemptReservationEvent `json:"attemptReservations,omitempty"`
	BoundAt             time.Time                 `json:"boundAt"`
}

type attemptReservationEvent struct {
	AttemptID          string `json:"attemptId"`
	ReservedTokens     int64  `json:"reservedTokens,omitempty"`
	ReservedCostMicros int64  `json:"reservedCostMicros,omitempty"`
}

type usageRecordedEvent struct {
	SettlementID  string    `json:"settlementId"`
	Principal     string    `json:"principal"`
	Model         string    `json:"model"`
	AttemptID     string    `json:"attemptId,omitempty"`
	WindowStarted time.Time `json:"windowStarted"`
	Known         bool      `json:"known"`
	InputTokens   int64     `json:"inputTokens,omitempty"`
	OutputTokens  int64     `json:"outputTokens,omitempty"`
	TotalTokens   int64     `json:"totalTokens,omitempty"`
	CostMicros    int64     `json:"costMicros,omitempty"`
	RecordedAt    time.Time `json:"recordedAt"`
}

type releasedEvent struct {
	Principal  string    `json:"principal"`
	ReleasedAt time.Time `json:"releasedAt"`
}

type reconciledEvent struct {
	Principal  string    `json:"principal"`
	Reason     string    `json:"reason"`
	RecordedAt time.Time `json:"recordedAt"`
}

type heartbeatEvent struct {
	Principal  string    `json:"principal"`
	RecordedAt time.Time `json:"recordedAt"`
}

func (m *Manager) replayLocked(entries []journal.Entry) error {
	active := make(map[string]*reservationState)
	settlements := make(map[string]map[string]struct{})
	for _, entry := range entries {
		switch entry.Type {
		case eventReserved:
			var event reservedEvent
			if err := decodeLedgerEvent(entry, &event); err != nil {
				return err
			}
			if event.Principal == "" || event.WindowStarted.IsZero() {
				return fmt.Errorf("invalid governance reservation event %q", entry.EntityID)
			}
			budgetKeys := append([]string(nil), event.BudgetKeys...)
			if len(budgetKeys) == 0 {
				budgetKeys = []string{canonicalBudgetKey("principal", event.Principal)}
			}
			for index := range budgetKeys {
				budgetKeys[index] = normalizeBudgetKey(budgetKeys[index])
			}
			attemptID := event.AttemptID
			if attemptID == "" {
				attemptID = "attempt-1"
			}
			reservation := &reservationState{
				id: entry.EntityID, principal: event.Principal, tenant: event.Tenant, upstream: event.Upstream, model: event.Model,
				requestID: event.RequestID, startedAt: event.StartedAt, windowStarted: event.WindowStarted,
				reservedTokens: event.ReservedTokens, reservedCostMicros: event.ReservedCostMicros,
				settlements: make(map[string]int64), budgetKeys: budgetKeys, attempts: make(map[string]*attemptState),
			}
			attempt := &attemptState{id: attemptID, windowStarted: event.WindowStarted, reservedTokens: event.ReservedTokens, reservedCostMicros: event.ReservedCostMicros, budgetKeys: append([]string(nil), budgetKeys...)}
			reservation.attempts[attemptID] = attempt
			active[entry.EntityID] = reservation
			settlements[entry.EntityID] = make(map[string]struct{})
			m.applyAttemptReservationLocked(reservation, attempt)
			m.counters.Admitted++
		case eventAttemptReserved:
			var event attemptReservedEvent
			if err := decodeLedgerEvent(entry, &event); err != nil {
				return err
			}
			reservation := active[entry.EntityID]
			if reservation == nil || event.AttemptID == "" || event.WindowStarted.IsZero() {
				return fmt.Errorf("orphan governance attempt reservation %q", entry.EntityID)
			}
			if _, exists := reservation.attempts[event.AttemptID]; exists {
				continue
			}
			keys := normalizeBudgetKeys(event.BudgetKeys, reservation.principal)
			attempt := &attemptState{id: event.AttemptID, windowStarted: event.WindowStarted, reservedTokens: event.ReservedTokens, reservedCostMicros: event.ReservedCostMicros, budgetKeys: keys}
			reservation.attempts[event.AttemptID] = attempt
			reservation.reservedTokens = safeAdd64(reservation.reservedTokens, max64(event.ReservedTokens, 0))
			reservation.reservedCostMicros = safeAdd64(reservation.reservedCostMicros, max64(event.ReservedCostMicros, 0))
			m.applyAttemptReservationLocked(reservation, attempt)
		case eventBound:
			var event boundEvent
			if err := decodeLedgerEvent(entry, &event); err != nil {
				return err
			}
			reservation := active[entry.EntityID]
			if reservation == nil || event.Upstream == "" {
				return fmt.Errorf("orphan governance binding %q", entry.EntityID)
			}
			keys := normalizeBudgetKeys(event.BudgetKeys, reservation.principal)
			if len(event.AttemptReservations) > 0 {
				m.rebindReservationLocked(reservation, keys, event.AttemptReservations)
			} else {
				m.moveReservationKeysLocked(reservation, keys)
			}
			reservation.upstream = event.Upstream
			reservation.budgetKeys = keys
			for _, attempt := range reservation.attempts {
				attempt.budgetKeys = append([]string(nil), keys...)
			}
		case eventHeartbeat:
			var event heartbeatEvent
			if err := decodeLedgerEvent(entry, &event); err != nil {
				return err
			}
			if active[entry.EntityID] == nil || event.Principal == "" {
				return fmt.Errorf("orphan governance heartbeat %q", entry.EntityID)
			}
		case eventUsageRecorded:
			var event usageRecordedEvent
			if err := decodeLedgerEvent(entry, &event); err != nil {
				return err
			}
			reservation := active[entry.EntityID]
			if reservation == nil || event.SettlementID == "" || event.Principal != reservation.principal {
				return fmt.Errorf("orphan governance settlement %q", entry.EntityID)
			}
			seen := settlements[entry.EntityID]
			if _, duplicate := seen[event.SettlementID]; duplicate {
				continue
			}
			seen[event.SettlementID] = struct{}{}
			reservation.settlements[event.SettlementID] = event.CostMicros
			attemptID := event.AttemptID
			if attemptID == "" {
				attemptID = "attempt-1"
			}
			attempt := reservation.attempts[attemptID]
			if attempt == nil {
				// Older ledgers did not persist attempt IDs. Resolve the only
				// outstanding attempt, preserving compatibility with those entries.
				for _, candidate := range reservation.attempts {
					if !candidate.reservationReleased && !candidate.settled {
						attempt = candidate
						break
					}
				}
			}
			if attempt == nil {
				return fmt.Errorf("orphan governance attempt settlement %q", entry.EntityID)
			}
			m.releaseAttemptLocked(reservation, attempt)
			attempt.settled = true
			window := event.WindowStarted
			if window.IsZero() {
				window = attempt.windowStarted
			}
			state := m.stateForPrincipalWindowLocked(event.Principal, window)
			m.applyUsageLocked(state, event)
			for _, key := range attempt.budgetKeys {
				if budgetKeyScope(key) != "principal" {
					m.applyUsageLocked(m.dimensionStateLocked(key, window), event)
				}
			}
			if event.Known {
				m.updateEstimateLocked(event.Model, event.TotalTokens, event.CostMicros)
			}
			m.counters.Settlements++
			if event.Known {
				m.counters.KnownSettlements++
			} else {
				m.counters.UnknownSettlements++
			}
		case eventReleased, eventReconciled:
			if reservation := active[entry.EntityID]; reservation != nil {
				m.releaseReservationLocked(reservation)
			}
			delete(active, entry.EntityID)
			delete(settlements, entry.EntityID)
		case "":
			return fmt.Errorf("governance ledger entry %d has no event type", entry.Sequence)
		default:
			return fmt.Errorf("unsupported governance ledger event %q", entry.Type)
		}
	}
	for id, reservation := range active {
		event := reconciledEvent{Principal: reservation.principal, Reason: "process_restart", RecordedAt: m.now().UTC()}
		if err := m.appendLocked(context.Background(), id, eventReconciled, event); err != nil && m.config.Mode == "enforce" {
			return ErrLedgerUnavailable
		}
		// The process that wrote the reservation is gone. Release the replayed
		// accounting before exposing the manager so an interrupted attempt cannot
		// consume concurrent or token capacity after reconciliation.
		m.releaseReservationLocked(reservation)
		m.counters.Reconciled++
	}
	return nil
}

func decodeLedgerEvent(entry journal.Entry, target any) error {
	if err := json.Unmarshal(entry.Payload, target); err != nil {
		return fmt.Errorf("decode governance event %d (%s): %w", entry.Sequence, entry.Type, err)
	}
	return nil
}

// replayStateLocked is retained for in-package integrations that used the
// original replay helper. It now delegates to the window-indexed state store.
func (m *Manager) replayStateLocked(principal string, window time.Time) *usageState {
	return m.stateForPrincipalWindowLocked(principal, window)
}
