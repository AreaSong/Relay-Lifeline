package runlog

import (
	"sync"
	"time"
)

type Limits struct {
	MaxItems  int
	Retention time.Duration
}

type Entry struct {
	ID         uint64         `json:"id"`
	Time       time.Time      `json:"time"`
	Level      string         `json:"level"`
	Event      string         `json:"event"`
	Message    string         `json:"message"`
	RequestID  string         `json:"requestId,omitempty"`
	Attempt    int            `json:"attempt,omitempty"`
	StatusCode int            `json:"statusCode,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type Store struct {
	mu      sync.Mutex
	entries []Entry
	nextID  uint64
	limits  func() Limits
	now     func() time.Time
}

func New(limits func() Limits) *Store {
	return &Store{limits: limits, now: time.Now}
}

func (s *Store) Add(entry Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Time.IsZero() {
		entry.Time = s.now()
	}
	s.nextID++
	entry.ID = s.nextID
	entry.Fields = cloneFields(entry.Fields)
	s.entries = append(s.entries, entry)
	s.pruneLocked()
}

func (s *Store) List(after uint64, level, event, requestID string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	result := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.ID <= after || level != "" && entry.Level != level || event != "" && entry.Event != event || requestID != "" && entry.RequestID != requestID {
			continue
		}
		entry.Fields = cloneFields(entry.Fields)
		result = append(result, entry)
	}
	return result
}

func (s *Store) pruneLocked() {
	limits := s.limits()
	cutoff := s.now().Add(-limits.Retention)
	first := 0
	for first < len(s.entries) && s.entries[first].Time.Before(cutoff) {
		first++
	}
	if first > 0 {
		s.entries = append([]Entry(nil), s.entries[first:]...)
	}
	if len(s.entries) > limits.MaxItems {
		s.entries = append([]Entry(nil), s.entries[len(s.entries)-limits.MaxItems:]...)
	}
}

func cloneFields(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	result := make(map[string]any, len(fields))
	for key, value := range fields {
		result[key] = value
	}
	return result
}
