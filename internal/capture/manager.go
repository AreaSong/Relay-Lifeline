package capture

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

type Manager struct {
	mu          sync.Mutex
	config      func() config.CaptureConfig
	masterKey   []byte
	unavailable string
	active      bool
	remaining   int
	deadline    time.Time
	records     map[string]Record
	byRequest   map[string]string
	now         func() time.Time
}

func New(provider func() config.CaptureConfig, encodedMasterKey string) *Manager {
	manager := &Manager{config: provider, records: make(map[string]Record), byRequest: make(map[string]string), now: time.Now}
	key, err := parseMasterKey(encodedMasterKey)
	if err != nil {
		manager.unavailable = err.Error()
		return manager
	}
	manager.masterKey = key
	if err := manager.initialize(); err != nil {
		manager.unavailable = err.Error()
		return manager
	}
	if provider().Enabled {
		manager.active = true
		manager.remaining = provider().DefaultRequestLimit
		manager.deadline = manager.now().Add(provider().ActivationTimeout.Duration)
	}
	return manager
}

func (m *Manager) StartCleaner(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = m.DeleteExpired()
		}
	}
}

func (m *Manager) Activate(requestLimit int, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unavailable != "" {
		return errors.New(m.unavailable)
	}
	cfg := m.config()
	if requestLimit == 0 {
		requestLimit = cfg.DefaultRequestLimit
	}
	if timeout == 0 {
		timeout = cfg.ActivationTimeout.Duration
	}
	if requestLimit < 1 || requestLimit > 100 || timeout <= 0 || timeout > time.Hour {
		return errors.New("capture activation limits are invalid")
	}
	if err := m.capacityAvailableLocked(0); err != nil {
		return err
	}
	m.active = true
	m.remaining = requestLimit
	m.deadline = m.now().Add(timeout)
	return nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	m.active = false
	m.remaining = 0
	m.deadline = time.Time{}
	m.mu.Unlock()
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireActivationLocked()
	return Status{
		Available: m.unavailable == "", UnavailableReason: m.unavailable, Active: m.active,
		RemainingRequests: m.remaining, Deadline: m.deadline, StorageBytes: m.storageBytesLocked(),
		MaxTotalBytes: int64(m.config().MaxTotalSize), CaptureCount: len(m.records),
	}
}

func (m *Manager) BeginRequest(requestID, method, path string, headers http.Header, body []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireActivationLocked()
	if m.unavailable != "" || !m.active || m.remaining == 0 {
		return "", nil
	}
	if err := m.capacityAvailableLocked(min(int64(len(body)), int64(m.config().MaxBodySize))); err != nil {
		m.active = false
		return "", err
	}
	m.remaining--
	if m.remaining == 0 {
		m.active = false
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	dataKey, err := randomKey()
	if err != nil {
		return "", err
	}
	wrapped, err := wrapKey(m.masterKey, dataKey)
	if err != nil {
		return "", err
	}
	now := m.now()
	record := Record{
		ID: id, RequestID: requestID, Method: method, Path: path, State: "active",
		StartedAt: now, ExpiresAt: now.Add(m.config().Retention.Duration), WrappedKey: wrapped,
		Request: BodyPart{Headers: safeHeaders(headers), ContentType: headers.Get("Content-Type")},
	}
	if err := os.MkdirAll(m.recordDir(id), 0o700); err != nil {
		return "", err
	}
	part, err := m.writeObjectLocked(id, dataKey, "request.body.enc", bytes.NewReader(body), &record.Request)
	if err != nil {
		_ = os.RemoveAll(m.recordDir(id))
		return "", err
	}
	record.Request = part
	record.CapturedBytes += part.StoredBytes
	m.records[id] = record
	m.byRequest[requestID] = id
	if err := m.persistLocked(record); err != nil {
		delete(m.records, id)
		delete(m.byRequest, requestID)
		_ = os.RemoveAll(m.recordDir(id))
		return "", err
	}
	return id, nil
}

func (m *Manager) RecordAttempt(requestID string, number, statusCode int, headers http.Header, body io.Reader, attemptErr error, started time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byRequest[requestID]
	if !ok {
		return nil
	}
	record := m.records[id]
	attempt := Attempt{Number: number, StartedAt: started, FinishedAt: m.now(), StatusCode: statusCode}
	if attemptErr != nil {
		attempt.Error = attemptErr.Error()
	}
	if body != nil && number <= m.config().MaxAttemptsPerRequest {
		key, err := unwrapKey(m.masterKey, record.WrappedKey)
		if err != nil {
			return err
		}
		base := &BodyPart{Headers: safeHeaders(headers), ContentType: headers.Get("Content-Type")}
		part, err := m.writeObjectLocked(id, key, fmt.Sprintf("attempt-%03d.body.enc", number), body, base)
		if err != nil {
			record.Warnings = appendUnique(record.Warnings, err.Error())
		} else {
			attempt.Response = &part
			record.CapturedBytes += part.StoredBytes
		}
	} else if body != nil {
		record.Warnings = appendUnique(record.Warnings, "max attempts per request reached; later bodies were not captured")
	}
	record.Attempts = append(record.Attempts, attempt)
	m.records[id] = record
	return m.persistLocked(record)
}

func (m *Manager) Finish(requestID, state string, finalAttempt int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byRequest[requestID]
	if !ok {
		return nil
	}
	record := m.records[id]
	record.State = state
	record.CompletedAt = m.now()
	for i := range record.Attempts {
		if record.Attempts[i].Number == finalAttempt && record.Attempts[i].Response != nil {
			part := *record.Attempts[i].Response
			record.Final = &part
		}
	}
	delete(m.byRequest, requestID)
	m.records[id] = record
	return m.persistLocked(record)
}

func (m *Manager) List() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Record, 0, len(m.records))
	for _, record := range m.records {
		result = append(result, publicRecord(record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	return result
}

func (m *Manager) Get(id string) (Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	return publicRecord(record), ok
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return os.ErrNotExist
	}
	delete(m.byRequest, record.RequestID)
	delete(m.records, id)
	return os.RemoveAll(m.recordDir(id))
}

func (m *Manager) DeleteExpired() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	deleted := 0
	var errs []error
	for id, record := range m.records {
		if record.ExpiresAt.After(now) {
			continue
		}
		if err := os.RemoveAll(m.recordDir(id)); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(m.records, id)
		delete(m.byRequest, record.RequestID)
		deleted++
	}
	return deleted, errors.Join(errs...)
}

func (m *Manager) initialize() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	root := m.config().StorageDir
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(root, entry.Name(), "metadata.json"))
		if readErr != nil {
			continue
		}
		var record Record
		if json.Unmarshal(data, &record) == nil && record.ID == entry.Name() {
			m.records[record.ID] = record
		}
	}
	return nil
}

func (m *Manager) writeObjectLocked(id string, key []byte, name string, source io.Reader, base *BodyPart) (BodyPart, error) {
	part := *base
	if err := m.capacityAvailableLocked(0); err != nil {
		return part, err
	}
	directory := m.recordDir(id)
	temporary, err := os.CreateTemp(directory, ".capture-*")
	if err != nil {
		return part, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return part, err
	}
	original, stored, truncated, err := encryptChunks(key, temporary, source, int64(m.config().MaxBodySize))
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return part, err
	}
	if err := m.capacityAvailableLocked(stored); err != nil {
		return part, err
	}
	destination := filepath.Join(directory, name)
	if err := os.Rename(temporaryName, destination); err != nil {
		return part, err
	}
	part.Object = name
	part.OriginalBytes = original
	part.StoredBytes = stored
	part.Truncated = truncated
	return part, nil
}

func (m *Manager) persistLocked(record Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.recordDir(record.ID), "metadata.json")
	temporary, err := os.CreateTemp(m.recordDir(record.ID), ".metadata-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (m *Manager) capacityAvailableLocked(additional int64) error {
	cfg := m.config()
	if m.storageBytesLocked()+additional > int64(cfg.MaxTotalSize) {
		return errors.New("capture storage capacity reached")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(cfg.StorageDir, &stat); err != nil {
		return err
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	if available-additional < int64(cfg.MinimumFreeDisk) {
		return errors.New("capture stopped because available disk is below the configured minimum")
	}
	return nil
}

func (m *Manager) storageBytesLocked() int64 {
	var total int64
	_ = filepath.WalkDir(m.config().StorageDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func (m *Manager) expireActivationLocked() {
	if m.active && !m.deadline.IsZero() && !m.now().Before(m.deadline) {
		m.active = false
		m.remaining = 0
		m.deadline = time.Time{}
	}
}

func (m *Manager) recordDir(id string) string { return filepath.Join(m.config().StorageDir, id) }

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func safeHeaders(source http.Header) http.Header {
	result := make(http.Header)
	for key, values := range source {
		lower := strings.ToLower(key)
		if lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" || lower == "set-cookie" || strings.Contains(lower, "api-key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
			continue
		}
		result[key] = append([]string(nil), values...)
	}
	return result
}

func cloneRecord(record Record) Record {
	record.Request.Headers = record.Request.Headers.Clone()
	record.Attempts = append([]Attempt(nil), record.Attempts...)
	for i := range record.Attempts {
		if record.Attempts[i].Response != nil {
			part := *record.Attempts[i].Response
			part.Headers = part.Headers.Clone()
			record.Attempts[i].Response = &part
		}
	}
	if record.Final != nil {
		part := *record.Final
		part.Headers = part.Headers.Clone()
		record.Final = &part
	}
	record.Warnings = append([]string(nil), record.Warnings...)
	return record
}

func publicRecord(record Record) Record {
	record = cloneRecord(record)
	record.WrappedKey = ""
	record.Request.Object = ""
	for index := range record.Attempts {
		if record.Attempts[index].Response != nil {
			record.Attempts[index].Response.Object = ""
		}
	}
	if record.Final != nil {
		record.Final.Object = ""
	}
	return record
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
