package journal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const SchemaVersion = 1

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

type Store struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	sync     bool
	sequence uint64
	lastHash string
	entries  []Entry
	lastErr  error
}

func Open(path string, syncWrites bool) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("journal path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
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
	store := &Store{path: path, file: file, sync: syncWrites, entries: entries}
	if len(entries) > 0 {
		store.sequence = entries[len(entries)-1].Sequence
		store.lastHash = entries[len(entries)-1].Hash
	}
	return store, nil
}

func (s *Store) Append(entityID, eventType string, payload any) (Entry, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Entry{}, fmt.Errorf("encode journal payload: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := Entry{
		SchemaVersion: SchemaVersion, Sequence: s.sequence + 1, Time: time.Now().UTC(),
		EntityID: entityID, Type: eventType, Payload: data, PreviousHash: s.lastHash,
	}
	entry.Hash = entryHash(entry)
	line, err := json.Marshal(entry)
	if err == nil {
		_, err = s.file.Write(append(line, '\n'))
	}
	if err == nil && s.sync {
		err = s.file.Sync()
	}
	if err != nil {
		s.lastErr = err
		return Entry{}, fmt.Errorf("append journal: %w", err)
	}
	s.sequence = entry.Sequence
	s.lastHash = entry.Hash
	s.entries = append(s.entries, entry)
	return entry, nil
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

// Compact removes entities whose newest event is older than cutoff and
// atomically rebuilds the remaining hash chain.
func (s *Store) Compact(cutoff time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	retainedEntities := make(map[string]struct{})
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
	removed := len(s.entries) - len(retained)
	if removed == 0 {
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
	return err
}

func Verify(path string) ([]Entry, error) { return readAndVerify(path) }

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
