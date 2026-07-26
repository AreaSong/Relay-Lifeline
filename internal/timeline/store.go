package timeline

import (
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
)

const maxEventsPerRequest = 100

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
	Time             time.Time      `json:"time"`
	Type             string         `json:"type"`
	Attempt          int            `json:"attempt,omitempty"`
	StatusCode       int            `json:"statusCode,omitempty"`
	Category         string         `json:"category,omitempty"`
	Message          string         `json:"message"`
	MessageCode      string         `json:"messageCode,omitempty"`
	MessageDetails   map[string]any `json:"messageDetails,omitempty"`
	ErrorDetail      *ErrorDetail   `json:"errorDetail,omitempty"`
	WaitMilliseconds int64          `json:"waitMilliseconds,omitempty"`
}

type Record struct {
	ID               string         `json:"id"`
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
}

type Store struct {
	mu      sync.RWMutex
	active  map[string]*Record
	history []Record
	limits  func() Limits
	now     func() time.Time
}

func New(limits func() Limits) *Store {
	return &Store{active: make(map[string]*Record), limits: limits, now: time.Now}
}

func (s *Store) Start(id, method, path string) {
	now := s.now()
	record := &Record{ID: id, Method: method, Path: path, State: "queued", StartedAt: now}
	record.Events = []Event{{Time: now, Type: "received", MessageCode: "timeline.received"}}
	s.mu.Lock()
	s.active[id] = record
	s.mu.Unlock()
}

func (s *Store) Add(id string, event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.active[id]
	if !ok {
		return
	}
	if event.Time.IsZero() {
		event.Time = s.now()
	}
	record.Events = append(record.Events, event)
	if len(record.Events) > maxEventsPerRequest {
		record.Events = append([]Event(nil), record.Events[len(record.Events)-maxEventsPerRequest:]...)
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

func (s *Store) Finish(id, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.active[id]
	if !ok {
		return
	}
	delete(s.active, id)
	record.State = outcome
	record.CompletedAt = s.now()
	s.history = append([]Record{cloneRecord(*record)}, s.history...)
	s.pruneLocked()
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
