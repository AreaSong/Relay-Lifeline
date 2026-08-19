package timeline

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/l10n"
)

const maxEventsPerRequest = 100

const (
	journalStart  = "timeline.start"
	journalEvent  = "timeline.event"
	journalFinish = "timeline.finish"
)

type Limits struct {
	MaxItems  int
	Retention time.Duration
}

type ErrorDetail struct {
	Message           string `json:"message,omitempty"`
	Type              string `json:"type,omitempty"`
	Code              string `json:"code,omitempty"`
	UpstreamRequestID string `json:"upstreamRequestId,omitempty"`
	RetryAfter        string `json:"retryAfter,omitempty"`
	ResponseBytes     int64  `json:"responseBytes"`
	Parsed            bool   `json:"parsed"`
}

type Event struct {
	Time                time.Time      `json:"time"`
	Type                string         `json:"type"`
	Attempt             int            `json:"attempt,omitempty"`
	StatusCode          int            `json:"statusCode,omitempty"`
	Category            string         `json:"category,omitempty"`
	Message             string         `json:"message"`
	MessageCode         string         `json:"messageCode,omitempty"`
	MessageDetails      map[string]any `json:"messageDetails,omitempty"`
	ErrorDetail         *ErrorDetail   `json:"errorDetail,omitempty"`
	WaitMilliseconds    int64          `json:"waitMilliseconds,omitempty"`
	AttemptPhase        string         `json:"attemptPhase,omitempty"`
	TargetID            string         `json:"targetId,omitempty"`
	TargetDomain        string         `json:"targetDomain,omitempty"`
	WroteRequest        bool           `json:"wroteRequest,omitempty"`
	IdempotencyKeyHash  string         `json:"idempotencyKeyHash,omitempty"`
	RequestBytes        int64          `json:"requestBytes,omitempty"`
	LatencyMilliseconds int64          `json:"latencyMilliseconds,omitempty"`
}

type Record struct {
	ID               string         `json:"id"`
	ClientID         string         `json:"clientId,omitempty"`
	TaskID           string         `json:"taskId,omitempty"`
	Method           string         `json:"method"`
	Path             string         `json:"path"`
	State            string         `json:"state"`
	Attempt          int            `json:"attempt"`
	StartedAt        time.Time      `json:"startedAt"`
	CompletedAt      time.Time      `json:"completedAt,omitempty"`
	LastError        string         `json:"lastError,omitempty"`
	LastErrorCode    string         `json:"lastErrorCode,omitempty"`
	LastErrorDetails map[string]any `json:"lastErrorDetails,omitempty"`
	LastErrorDetail  *ErrorDetail   `json:"lastErrorDetail,omitempty"`
	Events           []Event        `json:"events"`
	EventsTruncated  bool           `json:"eventsTruncated,omitempty"`
	DroppedEvents    int            `json:"droppedEvents,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	active  map[string]*Record
	history []Record
	limits  func() Limits
	now     func() time.Time
	journal *journal.Store
}

func New(limits func() Limits) *Store {
	return &Store{active: make(map[string]*Record), limits: limits, now: time.Now}
}

// NewPersistent replays the verified journal and marks requests interrupted by
// the previous process as orphaned. Orphaned requests are never retried.
func NewPersistent(limits func() Limits, eventJournal *journal.Store) (*Store, error) {
	store := New(limits)
	store.journal = eventJournal
	if eventJournal == nil {
		return store, nil
	}
	if err := store.replay(eventJournal.Entries()); err != nil {
		return nil, err
	}
	if err := store.orphanInterrupted(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Start(id, method, path string) error {
	return s.StartWithIdentity(id, method, path, "", "")
}

func (s *Store) StartWithIdentity(id, method, path, clientID, taskID string) error {
	now := s.now()
	record := &Record{ID: id, ClientID: clientID, TaskID: taskID, Method: method, Path: path, State: "queued", StartedAt: now}
	record.Events = []Event{{Time: now, Type: "received", MessageCode: "timeline.received"}}
	s.mu.Lock()
	if err := s.appendJournal(id, journalStart, record); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("persist timeline start: %w", err)
	}
	s.active[id] = record
	s.mu.Unlock()
	return nil
}

func (s *Store) Add(id string, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.active[id]
	if !ok {
		return nil
	}
	if event.Time.IsZero() {
		event.Time = s.now()
	}
	if err := s.appendJournal(id, journalEvent, event); err != nil {
		return fmt.Errorf("persist timeline event: %w", err)
	}
	applyEvent(record, event)
	return nil
}

func (s *Store) Finish(id, outcome string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.active[id]
	if !ok {
		return nil
	}
	completedAt := s.now()
	if err := s.appendJournal(id, journalFinish, finishPayload{Outcome: outcome, CompletedAt: completedAt}); err != nil {
		return fmt.Errorf("persist timeline finish: %w", err)
	}
	delete(s.active, id)
	record.State = outcome
	record.CompletedAt = completedAt
	s.history = append([]Record{cloneRecord(*record)}, s.history...)
	s.pruneLocked()
	return nil
}

func (s *Store) replay(entries []journal.Entry) error {
	for _, entry := range entries {
		switch entry.Type {
		case journalStart:
			var record Record
			if err := json.Unmarshal(entry.Payload, &record); err != nil {
				return replayError(entry, err)
			}
			if record.ID == "" || record.ID != entry.EntityID {
				return replayError(entry, fmt.Errorf("request ID mismatch"))
			}
			s.active[record.ID] = &record
		case journalEvent:
			record, ok := s.active[entry.EntityID]
			if !ok {
				return replayError(entry, fmt.Errorf("request is not active"))
			}
			var event Event
			if err := json.Unmarshal(entry.Payload, &event); err != nil {
				return replayError(entry, err)
			}
			applyEvent(record, event)
		case journalFinish:
			record, ok := s.active[entry.EntityID]
			if !ok {
				return replayError(entry, fmt.Errorf("request is not active"))
			}
			var payload finishPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return replayError(entry, err)
			}
			delete(s.active, entry.EntityID)
			record.State = payload.Outcome
			record.CompletedAt = payload.CompletedAt
			s.history = append([]Record{cloneRecord(*record)}, s.history...)
		default:
			return replayError(entry, fmt.Errorf("unsupported event type %q", entry.Type))
		}
	}
	s.pruneLocked()
	return nil
}

func (s *Store) orphanInterrupted() error {
	now := s.now()
	for id, record := range s.active {
		payload := finishPayload{Outcome: "orphaned", CompletedAt: now}
		if _, err := s.journal.Append(id, journalFinish, payload); err != nil {
			return fmt.Errorf("mark interrupted request %q as orphaned: %w", id, err)
		}
		delete(s.active, id)
		record.State = payload.Outcome
		record.CompletedAt = payload.CompletedAt
		s.history = append([]Record{cloneRecord(*record)}, s.history...)
	}
	s.pruneLocked()
	return nil
}

func (s *Store) appendJournal(id, eventType string, payload any) error {
	if s.journal != nil {
		_, err := s.journal.Append(id, eventType, payload)
		return err
	}
	return nil
}

func applyEvent(record *Record, event Event) {
	record.Events = append(record.Events, event)
	if len(record.Events) > maxEventsPerRequest {
		// Preserve the anchor and operator decisions; discard the oldest routine
		// detail event so an uncertain-delivery audit cannot be aged out.
		drop := 1
		for index := 1; index < len(record.Events)-1; index++ {
			if !criticalEvent(record.Events[index].Type) {
				drop = index
				break
			}
		}
		record.Events = append(record.Events[:drop], record.Events[drop+1:]...)
		record.EventsTruncated = true
		record.DroppedEvents++
	}
	if event.Attempt > record.Attempt {
		record.Attempt = event.Attempt
	}
	if event.Type == "attempt_failed" {
		record.LastError = event.Message
		record.LastErrorCode = event.MessageCode
		record.LastErrorDetails = cloneDetails(event.MessageDetails)
		record.LastErrorDetail = cloneErrorDetail(event.ErrorDetail)
	}
}

func criticalEvent(eventType string) bool {
	return eventType == "uncertain" || strings.HasPrefix(eventType, "uncertain_") || eventType == "completed"
}

type finishPayload struct {
	Outcome     string    `json:"outcome"`
	CompletedAt time.Time `json:"completedAt"`
}

func replayError(entry journal.Entry, err error) error {
	return fmt.Errorf("replay timeline journal sequence %d: %w", entry.Sequence, err)
}

func (s *Store) Request(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if record, ok := s.active[id]; ok {
		return cloneRecord(*record), true
	}
	for _, record := range s.history {
		if record.ID == id {
			return cloneRecord(record), true
		}
	}
	return Record{}, false
}

// ActiveIDs returns a snapshot of requests that still need their journal
// history for in-flight recovery. The returned map is independent from the
// store and may be safely used after releasing the timeline lock.
func (s *Store) ActiveIDs() map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make(map[string]struct{}, len(s.active))
	for id := range s.active {
		ids[id] = struct{}{}
	}
	return ids
}

func (s *Store) History() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	result := make([]Record, len(s.history))
	for index, record := range s.history {
		result[index] = cloneRecord(record)
	}
	return result
}

func (s *Store) pruneLocked() {
	limits := s.limits()
	cutoff := s.now().Add(-limits.Retention)
	kept := s.history[:0]
	for _, record := range s.history {
		if !record.CompletedAt.Before(cutoff) {
			kept = append(kept, record)
		}
	}
	if len(kept) > limits.MaxItems {
		kept = kept[:limits.MaxItems]
	}
	s.history = kept
}

func cloneRecord(record Record) Record {
	record.Events = append([]Event(nil), record.Events...)
	for index := range record.Events {
		record.Events[index].MessageDetails = cloneDetails(record.Events[index].MessageDetails)
		record.Events[index].ErrorDetail = cloneErrorDetail(record.Events[index].ErrorDetail)
	}
	record.LastErrorDetails = cloneDetails(record.LastErrorDetails)
	record.LastErrorDetail = cloneErrorDetail(record.LastErrorDetail)
	return record
}

func WithoutErrorDetails(records []Record) []Record {
	result := make([]Record, len(records))
	for index, record := range records {
		result[index] = cloneRecord(record)
		result[index].LastErrorDetail = nil
		for eventIndex := range result[index].Events {
			result[index].Events[eventIndex].ErrorDetail = nil
		}
	}
	return result
}

func LocalizeRecord(record Record, locale, fallback string) Record {
	record = cloneRecord(record)
	for index := range record.Events {
		event := &record.Events[index]
		if event.MessageCode != "" {
			event.Message = l10n.Default.Text(locale, fallback, l10n.M(event.MessageCode, event.MessageDetails))
		}
	}
	if record.LastErrorCode != "" {
		record.LastError = l10n.Default.Text(locale, fallback, l10n.M(record.LastErrorCode, record.LastErrorDetails))
	}
	return record
}

func LocalizeRecords(records []Record, locale, fallback string) []Record {
	result := make([]Record, len(records))
	for index, record := range records {
		result[index] = LocalizeRecord(record, locale, fallback)
	}
	return result
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	result := make(map[string]any, len(details))
	for key, value := range details {
		result[key] = value
	}
	return result
}

func cloneErrorDetail(detail *ErrorDetail) *ErrorDetail {
	if detail == nil {
		return nil
	}
	copy := *detail
	return &copy
}
