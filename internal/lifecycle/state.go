package lifecycle

import "fmt"

type State string

const (
	StateReceived   State = "received"
	StateQueued     State = "queued"
	StateForwarding State = "requesting"
	StateUncertain  State = "uncertain"
	StateWaiting    State = "waiting"
	StateBuffering  State = "buffering"
	StateDelivering State = "delivering"
	StateCompleted  State = "completed"
	StateSuccessful State = "successful"
	StateFailed     State = "failed"
	StateCanceled   State = "canceled"
	StateRejected   State = "rejected"
	StateExpired    State = "expired"
	StateOrphaned   State = "orphaned"
)

type AttemptPhase string

const (
	PhasePrepare         AttemptPhase = "prepare"
	PhaseConnect         AttemptPhase = "connect"
	PhaseRequestWrite    AttemptPhase = "request_write"
	PhaseResponseHeaders AttemptPhase = "response_headers"
	PhaseResponseBody    AttemptPhase = "response_body"
	PhaseProtocol        AttemptPhase = "protocol"
	PhaseDelivery        AttemptPhase = "delivery"
)

var transitions = map[State]map[State]bool{
	StateReceived:   allow(StateQueued, StateRejected, StateCanceled, StateExpired),
	StateQueued:     allow(StateForwarding, StateRejected, StateCanceled, StateExpired),
	StateForwarding: allow(StateBuffering, StateUncertain, StateWaiting, StateCanceled, StateFailed, StateExpired),
	StateUncertain:  allow(StateWaiting, StateCanceled, StateFailed, StateExpired),
	StateWaiting:    allow(StateForwarding, StateCanceled, StateFailed, StateExpired),
	StateBuffering:  allow(StateDelivering, StateWaiting, StateCanceled, StateFailed, StateExpired),
	StateDelivering: allow(StateCompleted, StateCanceled, StateFailed, StateExpired),
	StateCompleted:  allow(StateSuccessful),
}

func allow(states ...State) map[State]bool {
	result := make(map[State]bool, len(states))
	for _, state := range states {
		result[state] = true
	}
	return result
}

func ValidateTransition(from, to State) error {
	if from == to {
		return nil
	}
	if transitions[from][to] {
		return nil
	}
	return fmt.Errorf("invalid request state transition %q -> %q", from, to)
}

func IsTerminal(state State) bool {
	switch state {
	case StateSuccessful, StateFailed, StateCanceled, StateRejected, StateExpired, StateOrphaned:
		return true
	default:
		return false
	}
}
