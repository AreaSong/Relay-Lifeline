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
	ClientID   string         `json:"clientId,omitempty"`
	TaskID     string         `json:"taskId,omitempty"`
	Attempt    int            `json:"attempt,omitempty"`
	StatusCode int            `json:"statusCode,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type Page struct {
	Entries     []Entry `json:"entries"`
	NextAfter   uint64  `json:"nextAfter"`
	OldestAfter uint64  `json:"oldestAfter"`
	HasMore     bool    `json:"hasMore"`
	HasGap      bool    `json:"hasGap"`
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

func (s *Store) Page(after uint64, limit int, level, event, requestID string) Page {
	if limit < 1 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	page := Page{Entries: make([]Entry, 0), NextAfter: after}
	if len(s.entries) == 0 {
		page.OldestAfter = s.nextID
		return page
	}
	page.OldestAfter = s.entries[0].ID - 1
	page.HasGap = after < page.OldestAfter
	for _, entry := range s.entries {
		if entry.ID <= after || !matches(entry, level, event, requestID) {
			continue
		}
		if len(page.Entries) == limit {
			page.HasMore = true
			break
		}
		entry.Fields = cloneFields(entry.Fields)
		page.Entries = append(page.Entries, entry)
		page.NextAfter = entry.ID
	}
	return page
}

func (s *Store) Tail(limit int, level, event, requestID string) Page {
	if limit < 1 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	page := Page{Entries: make([]Entry, 0)}
	if len(s.entries) == 0 {
		page.OldestAfter = s.nextID
		page.NextAfter = s.nextID
		return page
	}
	page.OldestAfter = s.entries[0].ID - 1
	matched := make([]Entry, 0, min(limit+1, len(s.entries)))
	for index := len(s.entries) - 1; index >= 0 && len(matched) <= limit; index-- {
		entry := s.entries[index]
		if matches(entry, level, event, requestID) {
			matched = append(matched, entry)
		}
	}
	if len(matched) > limit {
		page.HasMore = true
		matched = matched[:limit]
	}
	for index := len(matched) - 1; index >= 0; index-- {
		entry := matched[index]
		entry.Fields = cloneFields(entry.Fields)
		page.Entries = append(page.Entries, entry)
	}
	if len(page.Entries) > 0 {
		page.NextAfter = page.Entries[len(page.Entries)-1].ID
	} else {
		page.NextAfter = s.nextID
	}
	page.HasGap = page.HasMore || page.OldestAfter > 0
	return page
}

func matches(entry Entry, level, event, requestID string) bool {
	return (level == "" || entry.Level == level) &&
		(event == "" || entry.Event == event) &&
		(requestID == "" || entry.RequestID == requestID)
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
