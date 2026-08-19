package incident

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/journal"
)

const journalSnapshot = "incident.snapshot"

type Incident struct {
	ID               string         `json:"id"`
	State            string         `json:"state"`
	StartedAt        time.Time      `json:"startedAt"`
	LastFailureAt    time.Time      `json:"lastFailureAt"`
	RecoveryStarted  *time.Time     `json:"recoveryStartedAt,omitempty"`
	ResolvedAt       *time.Time     `json:"resolvedAt,omitempty"`
	AffectedRequests []string       `json:"affectedRequests"`
	FailedAttempts   int            `json:"failedAttempts"`
	Categories       map[string]int `json:"categories"`
	StatusCodes      map[int]int    `json:"statusCodes"`
}

type Store struct {
	mu      sync.Mutex
	config  func() config.IncidentConfig
	journal *journal.Store
	items   map[string]*Incident
	current string
	timer   *time.Timer
	now     func() time.Time
}

func New(configProvider func() config.IncidentConfig, eventJournal *journal.Store) (*Store, error) {
	store := &Store{config: configProvider, journal: eventJournal, items: make(map[string]*Incident), now: time.Now}
	if eventJournal != nil {
		for _, entry := range eventJournal.Entries() {
			if entry.Type != journalSnapshot {
				continue
			}
			var incident Incident
			if err := json.Unmarshal(entry.Payload, &incident); err != nil {
				return nil, err
			}
			store.items[incident.ID] = clone(incident)
		}
	}
	store.restoreCurrentLocked()
	store.pruneLocked()
	store.scheduleRecoveryLocked()
	return store, nil
}

func (s *Store) RecordFailure(requestID, category string, statusCode int) error {
	cfg := s.config()
	if !cfg.Enabled {
		return nil
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	incident := s.candidateLocked(now, cfg)
	incident.State = "open"
	incident.LastFailureAt = now
	incident.RecoveryStarted = nil
	incident.ResolvedAt = nil
	incident.FailedAttempts++
	incident.Categories[category]++
	if statusCode > 0 {
		incident.StatusCodes[statusCode]++
	}
	if requestID != "" && !contains(incident.AffectedRequests, requestID) && len(incident.AffectedRequests) < 1000 {
		incident.AffectedRequests = append(incident.AffectedRequests, requestID)
	}
	if err := s.persistLocked(incident); err != nil {
		return fmt.Errorf("persist incident failure: %w", err)
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.items[incident.ID] = incident
	s.current = incident.ID
	s.pruneLocked()
	return nil
}

func (s *Store) RecordSuccess() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == "" {
		return nil
	}
	current, ok := s.items[s.current]
	if !ok {
		return nil
	}
	incident := clone(*current)
	now := s.now().UTC()
	incident.State = "recovering"
	incident.RecoveryStarted = &now
	if err := s.persistLocked(incident); err != nil {
		return fmt.Errorf("persist incident recovery: %w", err)
	}
	s.items[incident.ID] = incident
	s.scheduleRecoveryLocked()
	return nil
}

func (s *Store) List() []Incident {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	result := make([]Incident, 0, len(s.items))
	for _, incident := range s.items {
		result = append(result, *clone(*incident))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	return result
}

func (s *Store) Get(id string) (Incident, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	incident, ok := s.items[id]
	if !ok {
		return Incident{}, false
	}
	return *clone(*incident), true
}

func (s *Store) Close() {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
}

func (s *Store) candidateLocked(now time.Time, cfg config.IncidentConfig) *Incident {
	if s.current != "" {
		if current, ok := s.items[s.current]; ok {
			return clone(*current)
		}
	}
	for _, candidate := range s.items {
		if candidate.State == "resolved" && candidate.ResolvedAt != nil && now.Sub(*candidate.ResolvedAt) <= cfg.CorrelationWindow.Duration {
			result := clone(*candidate)
			result.State = "open"
			result.ResolvedAt = nil
			return result
		}
	}
	id := newID()
	incident := &Incident{ID: id, State: "open", StartedAt: now, LastFailureAt: now, Categories: make(map[string]int), StatusCodes: make(map[int]int)}
	return incident
}

func (s *Store) scheduleRecoveryLocked() {
	if s.current == "" {
		return
	}
	incident := s.items[s.current]
	if incident.State != "recovering" || incident.RecoveryStarted == nil {
		return
	}
	delay := s.config().RecoveryStableWindow.Duration - s.now().UTC().Sub(*incident.RecoveryStarted)
	if delay < 0 {
		delay = 0
	}
	id, recoveryStarted := incident.ID, *incident.RecoveryStarted
	s.timer = time.AfterFunc(delay, func() { s.resolve(id, recoveryStarted) })
}

func (s *Store) resolve(id string, recoveryStarted time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	incident, ok := s.items[id]
	if !ok || incident.State != "recovering" || incident.RecoveryStarted == nil || !incident.RecoveryStarted.Equal(recoveryStarted) {
		return
	}
	now := s.now().UTC()
	candidate := clone(*incident)
	candidate.State = "resolved"
	candidate.ResolvedAt = &now
	if err := s.persistLocked(candidate); err != nil {
		return
	}
	s.items[id] = candidate
	s.current = ""
	s.timer = nil
	s.pruneLocked()
}

func (s *Store) restoreCurrentLocked() {
	for _, incident := range s.items {
		if incident.State == "open" || incident.State == "recovering" {
			if s.current == "" || incident.StartedAt.After(s.items[s.current].StartedAt) {
				s.current = incident.ID
			}
		}
	}
}

func (s *Store) persistLocked(incident *Incident) error {
	if s.journal != nil {
		_, err := s.journal.Append(incident.ID, journalSnapshot, incident)
		return err
	}
	return nil
}

func (s *Store) pruneLocked() {
	cfg := s.config()
	cutoff := s.now().UTC().Add(-cfg.Retention.Duration)
	resolved := make([]*Incident, 0)
	for id, incident := range s.items {
		if incident.State == "resolved" && incident.ResolvedAt != nil && incident.ResolvedAt.Before(cutoff) {
			delete(s.items, id)
			continue
		}
		if incident.State == "resolved" {
			resolved = append(resolved, incident)
		}
	}
	if len(s.items) <= cfg.MaxItems {
		return
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].ResolvedAt.Before(*resolved[j].ResolvedAt) })
	for _, incident := range resolved {
		if len(s.items) <= cfg.MaxItems {
			break
		}
		delete(s.items, incident.ID)
	}
}

func clone(source Incident) *Incident {
	copy := source
	copy.AffectedRequests = append([]string(nil), source.AffectedRequests...)
	copy.Categories = make(map[string]int, len(source.Categories))
	for key, value := range source.Categories {
		copy.Categories[key] = value
	}
	copy.StatusCodes = make(map[int]int, len(source.StatusCodes))
	for key, value := range source.StatusCodes {
		copy.StatusCodes[key] = value
	}
	return &copy
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func newID() string {
	buffer := make([]byte, 8)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}
