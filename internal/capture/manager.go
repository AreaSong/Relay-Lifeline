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
	"net/url"
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
	keyring     Keyring
	root        string
	unavailable string
	active      bool
	remaining   int
	deadline    time.Time
	records     map[string]Record
	byRequest   map[string]string
	disabled    map[string]bool
	now         func() time.Time
	onEvent     func(event, message string, fields map[string]any)
}

func New(provider func() config.CaptureConfig, encodedMasterKey string) *Manager {
	keyring, err := ParseKeyring("", encodedMasterKey, "")
	return NewWithKeyring(provider, keyring, err)
}

func NewFromEnvironment(provider func() config.CaptureConfig) *Manager {
	keyring, err := KeyringFromEnvironment()
	return NewWithKeyring(provider, keyring, err)
}

func NewWithKeyring(provider func() config.CaptureConfig, keyring Keyring, keyringErr error) *Manager {
	manager := &Manager{config: provider, root: provider().StorageDir, records: make(map[string]Record), byRequest: make(map[string]string), disabled: make(map[string]bool), now: time.Now}
	if keyringErr != nil {
		manager.unavailable = keyringErr.Error()
		return manager
	}
	manager.keyring = keyring
	if err := manager.initialize(); err != nil {
		manager.unavailable = err.Error()
		return manager
	}
	_, _ = manager.DeleteExpired()
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

func (m *Manager) SetEventSink(sink func(event, message string, fields map[string]any)) {
	m.mu.Lock()
	m.onEvent = sink
	m.mu.Unlock()
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
		m.remaining = 0
		m.deadline = time.Time{}
		m.emitLocked("capture.capacity_stopped", "捕获因容量或磁盘限制停止", map[string]any{"reason": err.Error()})
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
	wrapped, err := wrapKey(m.keyring.Keys[m.keyring.ActiveID], dataKey)
	if err != nil {
		return "", err
	}
	now := m.now()
	record := Record{
		ID: id, RequestID: requestID, Method: method, Path: sanitizePath(path), State: "active",
		StartedAt: now, ExpiresAt: now.Add(m.config().Retention.Duration), WrappedKey: wrapped, KeyID: m.keyring.ActiveID,
		Request:  BodyPart{Headers: safeHeaders(headers), ContentType: headers.Get("Content-Type")},
		Attempts: make([]Attempt, 0),
	}
	if err := os.MkdirAll(m.recordDir(id), 0o700); err != nil {
		return "", err
	}
	part, err := m.writeObjectLocked(id, dataKey, "request.body.enc", bytes.NewReader(body), &record.Request)
	if err != nil {
		m.active = false
		m.remaining = 0
		m.deadline = time.Time{}
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
		attempt.Error = string(redactText([]byte(attemptErr.Error())))
	}
	if body != nil && !m.disabled[requestID] && number <= m.config().MaxAttemptsPerRequest {
		key, _, err := m.dataKeyLocked(record)
		if err != nil {
			return err
		}
		base := &BodyPart{Headers: safeHeaders(headers), ContentType: headers.Get("Content-Type")}
		part, err := m.writeObjectLocked(id, key, fmt.Sprintf("attempt-%03d.body.enc", number), body, base)
		if err != nil {
			record.Warnings = appendUnique(record.Warnings, err.Error())
			m.disabled[requestID] = true
		} else {
			attempt.Response = &part
			record.CapturedBytes += part.StoredBytes
		}
	} else if body != nil && !m.disabled[requestID] {
		record.Warnings = appendUnique(record.Warnings, "max attempts per request reached; later bodies were not captured")
		m.disabled[requestID] = true
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
	delete(m.disabled, requestID)
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

func (m *Manager) KeyStatus() KeyStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := KeyStatus{ActiveID: m.keyring.ActiveID, Configured: m.keyring.IDs(), RecordsByID: make(map[string]int)}
	for _, record := range m.records {
		if record.KeyID == "" {
			status.Unresolved++
			continue
		}
		status.RecordsByID[record.KeyID]++
	}
	return status
}

func (m *Manager) RewrapAll() (RewrapResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := RewrapResult{ActiveID: m.keyring.ActiveID}
	updated := make(map[string]Record)
	originals := make(map[string]Record)
	for id, record := range m.records {
		if record.KeyID == m.keyring.ActiveID {
			result.Unchanged++
			continue
		}
		dataKey, _, err := m.dataKeyLocked(record)
		if err != nil {
			return RewrapResult{}, fmt.Errorf("capture %s cannot be unwrapped: %w", id, err)
		}
		wrapped, err := wrapKey(m.keyring.Keys[m.keyring.ActiveID], dataKey)
		if err != nil {
			return RewrapResult{}, err
		}
		originals[id] = record
		record.WrappedKey = wrapped
		record.KeyID = m.keyring.ActiveID
		updated[id] = record
	}
	persisted := make([]string, 0, len(updated))
	for id, record := range updated {
		if err := m.persistLocked(record); err != nil {
			var rollbackErrors []error
			for _, persistedID := range persisted {
				if rollbackErr := m.persistLocked(originals[persistedID]); rollbackErr != nil {
					rollbackErrors = append(rollbackErrors, rollbackErr)
				}
			}
			return RewrapResult{}, errors.Join(append([]error{err}, rollbackErrors...)...)
		}
		persisted = append(persisted, id)
	}
	for id, record := range updated {
		m.records[id] = record
	}
	result.Updated = len(updated)
	return result, nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return os.ErrNotExist
	}
	delete(m.byRequest, record.RequestID)
	delete(m.disabled, record.RequestID)
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
		delete(m.disabled, record.RequestID)
		deleted++
	}
	if deleted > 0 {
		m.emitLocked("capture.expired_deleted", "已删除到期捕获", map[string]any{"count": deleted})
	}
	return deleted, errors.Join(errs...)
}

func (m *Manager) initialize() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	root := m.root
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
			if record.KeyID == "" {
				if _, resolvedID, resolveErr := m.dataKeyLocked(record); resolveErr == nil {
					record.KeyID = resolvedID
					if persistErr := m.persistLocked(record); persistErr != nil {
						return persistErr
					}
				}
			}
			if record.State == "active" {
				record.State = "interrupted"
				record.CompletedAt = m.now()
				record.Warnings = appendUnique(record.Warnings, "service restarted before request completed")
				if record.Final == nil {
					for index := len(record.Attempts) - 1; index >= 0; index-- {
						if record.Attempts[index].Response != nil {
							part := *record.Attempts[index].Response
							record.Final = &part
							break
						}
					}
				}
				if persistErr := m.persistLocked(record); persistErr != nil {
					return persistErr
				}
			}
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
	currentSize := m.storageBytesLocked()
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
	if currentSize+stored > int64(m.config().MaxTotalSize) {
		return part, errors.New("capture storage capacity reached")
	}
	if err := m.capacityAvailableLocked(0); err != nil {
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
	if err := syscall.Statfs(m.root, &stat); err != nil {
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
	_ = filepath.WalkDir(m.root, func(path string, entry os.DirEntry, err error) error {
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
		m.emitLocked("capture.activation_expired", "捕获等待窗口已到期", nil)
	}
}

func (m *Manager) emitLocked(event, message string, fields map[string]any) {
	if m.onEvent != nil {
		m.onEvent(event, message, fields)
	}
}

func (m *Manager) recordDir(id string) string { return filepath.Join(m.root, id) }

func (m *Manager) dataKeyLocked(record Record) ([]byte, string, error) {
	if record.KeyID != "" {
		key, exists := m.keyring.Keys[record.KeyID]
		if !exists {
			return nil, "", fmt.Errorf("capture key %q is not configured", record.KeyID)
		}
		dataKey, err := unwrapKey(key, record.WrappedKey)
		return dataKey, record.KeyID, err
	}
	for _, id := range m.keyring.IDs() {
		if dataKey, err := unwrapKey(m.keyring.Keys[id], record.WrappedKey); err == nil {
			return dataKey, id, nil
		}
	}
	return nil, "", errors.New("no configured capture key can unwrap the record")
}

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
		if lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" || lower == "set-cookie" || lower == "key" || strings.Contains(lower, "auth") || strings.Contains(lower, "api-key") || strings.HasSuffix(lower, "-key") || strings.HasSuffix(lower, "_key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
			continue
		}
		filtered := make([]string, len(values))
		for index, value := range values {
			filtered[index] = string(redactText([]byte(value)))
		}
		result[key] = filtered
	}
	return result
}

func sanitizePath(value string) string {
	path, rawQuery, found := strings.Cut(value, "?")
	if !found {
		return path
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return path
	}
	for key := range values {
		if sensitiveName(key) {
			values.Set(key, "[REDACTED]")
		}
	}
	return path + "?" + values.Encode()
}

func cloneRecord(record Record) Record {
	record.Request.Headers = record.Request.Headers.Clone()
	record.Attempts = append([]Attempt{}, record.Attempts...)
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
