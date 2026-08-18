package monitoring

import "time"

const securityEventCapacity = 1000

type Event struct {
	ID              uint64         `json:"id"`
	Time            time.Time      `json:"time"`
	Code            string         `json:"code"`
	Category        string         `json:"category,omitempty"`
	RequestID       string         `json:"requestId,omitempty"`
	StatusCode      int            `json:"statusCode,omitempty"`
	Attempt         int            `json:"attempt,omitempty"`
	Outcome         string         `json:"outcome,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
	Changed         *bool          `json:"changed,omitempty"`
	RestartRequired *bool          `json:"restartRequired,omitempty"`
}

type SecurityEvent = Event

type EventPage struct {
	Events      []Event `json:"events"`
	NextAfter   uint64  `json:"nextAfter"`
	OldestAfter uint64  `json:"oldestAfter"`
	HasMore     bool    `json:"hasMore"`
	HasGap      bool    `json:"hasGap"`
}

type eventRing struct {
	items  [securityEventCapacity]Event
	start  int
	length int
	nextID uint64
}

func newEventRing() eventRing { return eventRing{} }

func (s *Store) RecordEvent(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = s.now().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	s.events.nextID++
	event.ID = s.events.nextID
	if s.events.length < securityEventCapacity {
		index := (s.events.start + s.events.length) % securityEventCapacity
		s.events.items[index] = event
		s.events.length++
		return
	}
	s.events.items[s.events.start] = event
	s.events.start = (s.events.start + 1) % securityEventCapacity
}

func (s *Store) RecordSecurityEvent(event SecurityEvent) { s.RecordEvent(event) }

func (s *Store) Events(after uint64, limit int) EventPage {
	if limit < 1 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	page := EventPage{Events: make([]Event, 0), NextAfter: after}
	if s.events.length == 0 {
		page.OldestAfter = s.events.nextID
		return page
	}
	oldest := s.events.items[s.events.start].ID
	page.OldestAfter = oldest - 1
	page.HasGap = after < page.OldestAfter
	for offset := 0; offset < s.events.length; offset++ {
		event := s.events.items[(s.events.start+offset)%securityEventCapacity]
		if event.ID <= after {
			continue
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, event)
		page.NextAfter = event.ID
	}
	return page
}

func Bool(value bool) *bool { return &value }
