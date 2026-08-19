package journal

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/areasong/relay-lifeline/internal/telemetry"
)

const SchemaVersion = 1

type State string

const (
	StateHealthy  State = "healthy"
	StateDegraded State = "degraded"
	StateClosed   State = "closed"
)

// Status is a point-in-time persistence health snapshot. A degraded journal
// remains degraded until the process reopens and verifies it again.
type Status struct {
	State        State     `json:"state"`
	FailedAt     time.Time `json:"failedAt,omitempty"`
	FailedStage  string    `json:"failedStage,omitempty"`
	FailureCount uint64    `json:"failureCount,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
}

// Hooks make short-write and fsync failures deterministic in tests without
// replacing the journal's file ownership or durability behavior in production.
type Hooks struct {
	Write func(*os.File, []byte) (int, error)
	Sync  func(*os.File) error
}

type Entry struct {
	SchemaVersion int             `json:"schemaVersion"`
	Sequence      uint64          `json:"sequence"`
	Time          time.Time       `json:"time"`
	EntityID      string          `json:"entityId"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	PreviousHash  string          `json:"previousHash,omitempty"`
	Hash          string          `json:"hash"`
}

type integrityAnchor struct {
	SchemaVersion int    `json:"schemaVersion"`
	Sequence      uint64 `json:"sequence"`
	Hash          string `json:"hash,omitempty"`
	MAC           string `json:"mac"`
}

type Store struct {
	mu                     sync.Mutex
	path                   string
	file                   *os.File
	sync                   bool
	sequence               uint64
	lastHash               string
	entries                []Entry
	lastErr                error
	state                  State
	failedAt               time.Time
	failedStage            string
	failureCount           uint64
	hooks                  Hooks
	replayDuration         time.Duration
	lastCompactionAt       time.Time
	lastCompactionDuration time.Duration
	lastCompactionRemoved  int
	lastCompactionErr      error
	integrityKey           []byte
	anchorPath             string
}

type Stats struct {
	Entries                int
	SizeBytes              int64
	ReplayDuration         time.Duration
	LastCompactionAt       time.Time
	LastCompactionDuration time.Duration
	LastCompactionRemoved  int
	CompactionHealthy      bool
	Status                 Status
}

func Open(path string, syncWrites bool) (*Store, error) {
	return OpenWithIntegrity(path, syncWrites, nil)
}

// OpenWithIntegrity enables an HMAC-protected external anchor when key is
// configured. The journal hash chain remains the source of event ordering;
// the anchor detects edits that rewrite a valid-looking chain.
func OpenWithIntegrity(path string, syncWrites bool, key []byte) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("journal path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	replayStarted := time.Now()
	entries, err := readAndVerify(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect journal: %w", err)
	}
	store := &Store{path: path, file: file, sync: syncWrites, entries: entries, replayDuration: time.Since(replayStarted), state: StateHealthy, anchorPath: path + ".anchor"}
	if len(entries) > 0 {
		store.sequence = entries[len(entries)-1].Sequence
		store.lastHash = entries[len(entries)-1].Hash
	}
	if len(key) > 0 {
		store.integrityKey = append([]byte(nil), key...)
		if err := store.verifyOrCreateAnchor(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("verify journal integrity anchor: %w", err)
		}
	}
	return store, nil
}

func (s *Store) Append(entityID, eventType string, payload any) (Entry, error) {
	return s.append(context.Background(), entityID, eventType, payload)
}

func (s *Store) AppendContext(ctx context.Context, entityID, eventType string, payload any) (Entry, error) {
	return s.append(ctx, entityID, eventType, payload)
}

func (s *Store) append(ctx context.Context, entityID, eventType string, payload any) (result Entry, resultErr error) {
	started := time.Now()
	var span oteltrace.Span
	if oteltrace.SpanContextFromContext(ctx).IsValid() {
		ctx, span = telemetry.Tracer("relay-lifeline/journal").Start(ctx, "relay.journal.append")
		_ = ctx
		defer span.End()
	}
	defer func() {
		outcome := "success"
		if resultErr != nil {
			outcome = "error"
			if span != nil {
				span.RecordError(resultErr)
				span.SetStatus(codes.Error, "journal append failed")
			}
		} else if span != nil {
			span.SetStatus(codes.Ok, "")
		}
		telemetry.RecordJournalAppend(ctx, outcome, time.Since(started))
	}()
	data, err := json.Marshal(payload)
	if err != nil {
		s.recordFailure("encode", err)
		return Entry{}, fmt.Errorf("encode journal payload: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.state == StateClosed {
		err := errors.New("journal is closed")
		s.recordFailureLocked("write", err)
		return Entry{}, err
	}
	if s.state == StateDegraded {
		return Entry{}, fmt.Errorf("journal is degraded: %w", s.lastErr)
	}
	entry := Entry{
		SchemaVersion: SchemaVersion, Sequence: s.sequence + 1, Time: time.Now().UTC(),
		EntityID: entityID, Type: eventType, Payload: data, PreviousHash: s.lastHash,
	}
	entry.Hash = entryHash(entry)
	line, err := json.Marshal(entry)
	line = append(line, '\n')
	if err != nil {
		s.recordFailureLocked("encode", err)
		return Entry{}, fmt.Errorf("encode journal entry: %w", err)
	}
	write := s.hooks.Write
	if write == nil {
		write = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	}
	written, err := write(s.file, line)
	if err == nil && written != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		s.recordFailureLocked("write", err)
		return Entry{}, fmt.Errorf("append journal: %w", err)
	}
	if s.sync {
		syncFile := s.hooks.Sync
		if syncFile == nil {
			syncFile = func(file *os.File) error { return file.Sync() }
		}
		err = syncFile(s.file)
		if err != nil {
			s.recordFailureLocked("sync", err)
			return Entry{}, fmt.Errorf("append journal: %w", err)
		}
	}
	s.sequence = entry.Sequence
	s.lastHash = entry.Hash
	if err := s.updateAnchorLocked(entry.Sequence, entry.Hash); err != nil {
		s.recordFailureLocked("anchor", err)
		return Entry{}, fmt.Errorf("update journal integrity anchor: %w", err)
	}
	s.entries = append(s.entries, entry)
	return entry, nil
}

func (s *Store) SetHooks(hooks Hooks) {
	s.mu.Lock()
	s.hooks = hooks
	s.mu.Unlock()
}

func (s *Store) recordFailure(stage string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordFailureLocked(stage, err)
}

func (s *Store) recordFailureLocked(stage string, err error) {
	s.lastErr = err
	if s.state != StateClosed {
		s.state = StateDegraded
	}
	s.failedAt = time.Now().UTC()
	s.failedStage = stage
	s.failureCount++
}

func (s *Store) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneEntries(s.entries)
}

func (s *Store) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := Stats{
		Entries: len(s.entries), ReplayDuration: s.replayDuration,
		LastCompactionAt: s.lastCompactionAt, LastCompactionDuration: s.lastCompactionDuration,
		LastCompactionRemoved: s.lastCompactionRemoved, CompactionHealthy: s.lastCompactionErr == nil,
		Status: s.statusLocked(),
	}
	if s.file != nil {
		if info, err := s.file.Stat(); err == nil {
			stats.SizeBytes = info.Size()
		}
	}
	return stats
}

func (s *Store) Health() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateDegraded || s.lastErr != nil {
		return fmt.Errorf("last journal write failed: %w", s.lastErr)
	}
	if s.file == nil || s.state == StateClosed {
		return errors.New("journal is closed")
	}
	if _, err := s.file.Stat(); err != nil {
		return fmt.Errorf("stat journal: %w", err)
	}
	probe, err := os.OpenFile(s.path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("journal is not writable: %w", err)
	}
	return probe.Close()
}

func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Store) statusLocked() Status {
	state := s.state
	if state == "" {
		state = StateHealthy
	}
	status := Status{State: state, FailedAt: s.failedAt, FailedStage: s.failedStage, FailureCount: s.failureCount}
	if s.lastErr != nil {
		status.LastError = s.lastErr.Error()
	}
	return status
}

// Compact removes entities whose newest event is older than cutoff and
// atomically rebuilds the remaining hash chain.
func (s *Store) Compact(cutoff time.Time) (removed int, err error) {
	return s.CompactWithProtection(cutoff, nil)
}

// CompactWithProtection removes expired entities while retaining every entity
// in protectedEntities. Callers use this for entities that are still active in
// memory but have not emitted a recent journal event yet.
func (s *Store) CompactWithProtection(cutoff time.Time, protectedEntities map[string]struct{}) (removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	startedAt := time.Now()
	defer func() {
		s.lastCompactionDuration = time.Since(startedAt)
		s.lastCompactionErr = err
	}()

	retainedEntities := make(map[string]struct{}, len(protectedEntities))
	for entityID := range protectedEntities {
		retainedEntities[entityID] = struct{}{}
	}
	for _, entry := range s.entries {
		if !entry.Time.Before(cutoff) {
			retainedEntities[entry.EntityID] = struct{}{}
		}
	}
	retained := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if _, ok := retainedEntities[entry.EntityID]; ok {
			retained = append(retained, entry)
		}
	}
	removed = len(s.entries) - len(retained)
	if removed == 0 {
		s.lastCompactionAt = time.Now().UTC()
		s.lastCompactionRemoved = 0
		return 0, nil
	}

	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, ".journal-compact-*")
	if err != nil {
		return 0, fmt.Errorf("create compacted journal: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return 0, fmt.Errorf("protect compacted journal: %w", err)
	}
	retained = rebuildChain(retained)
	for _, entry := range retained {
		line, marshalErr := json.Marshal(entry)
		if marshalErr != nil {
			return 0, fmt.Errorf("encode compacted journal: %w", marshalErr)
		}
		if _, err := temporary.Write(append(line, '\n')); err != nil {
			return 0, fmt.Errorf("write compacted journal: %w", err)
		}
	}
	if err := temporary.Sync(); err != nil {
		return 0, fmt.Errorf("sync compacted journal: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return 0, fmt.Errorf("replace journal: %w", err)
	}
	committed = true
	oldFile := s.file
	s.file = temporary
	if oldFile != nil {
		_ = oldFile.Close()
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	s.entries = retained
	s.sequence = 0
	s.lastHash = ""
	if len(retained) > 0 {
		s.sequence = retained[len(retained)-1].Sequence
		s.lastHash = retained[len(retained)-1].Hash
	}
	if err := s.updateAnchorLocked(s.sequence, s.lastHash); err != nil {
		s.recordFailureLocked("anchor", err)
		return 0, fmt.Errorf("update compacted journal integrity anchor: %w", err)
	}
	s.lastCompactionAt = time.Now().UTC()
	s.lastCompactionRemoved = removed
	return removed, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	s.state = StateClosed
	return err
}

func Verify(path string) ([]Entry, error) { return readAndVerify(path) }

func VerifyWithIntegrity(path string, key []byte) ([]Entry, error) {
	entries, err := readAndVerify(path)
	if err != nil || len(key) == 0 {
		return entries, err
	}
	sequence := uint64(0)
	hash := ""
	if len(entries) > 0 {
		sequence = entries[len(entries)-1].Sequence
		hash = entries[len(entries)-1].Hash
	}
	if err := verifyAnchor(path+".anchor", key, sequence, hash); err != nil {
		return nil, err
	}
	return entries, nil
}

func readAndVerify(path string) ([]Entry, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	entries := make([]Entry, 0)
	var sequence uint64
	previousHash := ""
	for scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf("decode journal entry %d: %w", sequence+1, err)
		}
		if entry.SchemaVersion != SchemaVersion || entry.Sequence != sequence+1 {
			return nil, fmt.Errorf("invalid journal sequence %d", entry.Sequence)
		}
		if entry.PreviousHash != previousHash || entry.Hash != entryHash(entry) {
			return nil, fmt.Errorf("journal integrity check failed at sequence %d", entry.Sequence)
		}
		entries = append(entries, entry)
		sequence = entry.Sequence
		previousHash = entry.Hash
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan journal: %w", err)
	}
	return entries, nil
}

func entryHash(entry Entry) string {
	hashable := struct {
		SchemaVersion int             `json:"schemaVersion"`
		Sequence      uint64          `json:"sequence"`
		Time          time.Time       `json:"time"`
		EntityID      string          `json:"entityId"`
		Type          string          `json:"type"`
		Payload       json.RawMessage `json:"payload,omitempty"`
		PreviousHash  string          `json:"previousHash,omitempty"`
	}{entry.SchemaVersion, entry.Sequence, entry.Time, entry.EntityID, entry.Type, entry.Payload, entry.PreviousHash}
	data, _ := json.Marshal(hashable)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *Store) verifyOrCreateAnchor() error {
	if len(s.integrityKey) == 0 {
		return nil
	}
	data, err := os.ReadFile(s.anchorPath)
	if errors.Is(err, os.ErrNotExist) {
		return s.writeAnchor(s.sequence, s.lastHash)
	}
	if err != nil {
		return err
	}
	return verifyAnchorData(data, s.integrityKey, s.sequence, s.lastHash)
}

func verifyAnchor(anchorPath string, key []byte, sequence uint64, hash string) error {
	data, err := os.ReadFile(anchorPath)
	if err != nil {
		return fmt.Errorf("read journal integrity anchor: %w", err)
	}
	return verifyAnchorData(data, key, sequence, hash)
}

func verifyAnchorData(data, key []byte, sequence uint64, hash string) error {
	var anchor integrityAnchor
	if err := json.Unmarshal(data, &anchor); err != nil {
		return fmt.Errorf("decode anchor: %w", err)
	}
	if anchor.SchemaVersion != SchemaVersion || anchor.Sequence != sequence || anchor.Hash != hash || !hmac.Equal([]byte(anchor.MAC), []byte(anchorMAC(key, anchor.SchemaVersion, anchor.Sequence, anchor.Hash))) {
		return errors.New("journal integrity anchor does not match journal")
	}
	return nil
}

func (s *Store) updateAnchorLocked(sequence uint64, hash string) error {
	if len(s.integrityKey) == 0 {
		return nil
	}
	return s.writeAnchor(sequence, hash)
}

func (s *Store) writeAnchor(sequence uint64, hash string) error {
	anchor := integrityAnchor{SchemaVersion: SchemaVersion, Sequence: sequence, Hash: hash, MAC: anchorMAC(s.integrityKey, SchemaVersion, sequence, hash)}
	data, err := json.Marshal(anchor)
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.anchorPath)
	temporary, err := os.CreateTemp(directory, ".journal-anchor-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, s.anchorPath); err != nil {
		return err
	}
	committed = true
	if directoryHandle, err := os.Open(directory); err == nil {
		defer directoryHandle.Close()
		if err := directoryHandle.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func anchorMAC(key []byte, schema int, sequence uint64, hash string) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%d:%d:%s", schema, sequence, hash)
	return hex.EncodeToString(mac.Sum(nil))
}

func cloneEntries(entries []Entry) []Entry {
	result := make([]Entry, len(entries))
	copy(result, entries)
	for index := range result {
		result[index].Payload = append(json.RawMessage(nil), result[index].Payload...)
	}
	return result
}

func rebuildChain(entries []Entry) []Entry {
	result := cloneEntries(entries)
	previousHash := ""
	for index := range result {
		result[index].Sequence = uint64(index + 1)
		result[index].PreviousHash = previousHash
		result[index].Hash = entryHash(result[index])
		previousHash = result[index].Hash
	}
	return result
}
