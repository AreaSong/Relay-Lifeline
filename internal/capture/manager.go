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
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/disk"
)

type Manager struct {
	mu                 sync.Mutex
	config             func() config.CaptureConfig
	keyring            Keyring
	root               string
	unavailable        string
	active             bool
	remaining          int
	deadline           time.Time
	records            map[string]Record
	byRequest          map[string]string
	disabled           map[string]bool
	now                func() time.Time
	onEvent            func(event, message string, fields map[string]any)
	hooks              Hooks
	persistenceHealthy bool
	failureCount       uint64
	failedStage        string
	lastFailureAt      time.Time
	storageBytes       int64
}

// Hooks provide deterministic filesystem fault injection for durability tests.
// Nil functions use the operating system implementation.
type Hooks struct {
	Write   func(file *os.File, data []byte) (int, error)
	Sync    func(file *os.File) error
	Rename  func(oldPath, newPath string) error
	SyncDir func(path string) error
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
	manager := &Manager{config: provider, root: provider().StorageDir, records: make(map[string]Record), byRequest: make(map[string]string), disabled: make(map[string]bool), now: time.Now, persistenceHealthy: true}
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
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = m.DeleteExpired()
			ticks++
			if ticks >= 60 {
				m.ReconcileStorageUsage()
				ticks = 0
			}
		}
	}
}

// ReconcileStorageUsage performs the low-frequency full scan used to correct
// accounting after an external cleanup or an interrupted filesystem update.
func (m *Manager) ReconcileStorageUsage() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storageBytes = m.scanStorageBytesLocked()
	return m.storageBytes
}

func (m *Manager) SetEventSink(sink func(event, message string, fields map[string]any)) {
	m.mu.Lock()
	m.onEvent = sink
	m.mu.Unlock()
}

func (m *Manager) SetHooks(hooks Hooks) {
	m.mu.Lock()
	m.hooks = hooks
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
		PersistenceHealthy: m.persistenceHealthy, FailureCount: m.failureCount, FailedStage: m.failedStage, LastFailureAt: m.lastFailureAt,
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
		return "", m.failLocked("mkdir", err)
	}
	if err := m.syncDirLocked(m.root, "directory_sync"); err != nil {
		_ = os.RemoveAll(m.recordDir(id))
		return "", err
	}
	part, err := m.writeObjectLocked(id, dataKey, "request.body.enc", bytes.NewReader(body), &record.Request)
	if err != nil {
		m.active = false
		m.remaining = 0
		m.deadline = time.Time{}
		m.removeRecordDirLocked(id)
		return "", err
	}
	record.Request = part
	record.CapturedBytes += part.StoredBytes
	committed, persistErr := m.persistLocked(record)
	if !committed {
		m.removeRecordDirLocked(id)
		return "", persistErr
	}
	m.records[id] = record
	m.byRequest[requestID] = id
	m.remaining--
	if m.remaining == 0 {
		m.active = false
	}
	if persistErr == nil {
		m.persistenceHealthy = true
	}
	return id, persistErr
}

func (m *Manager) RecordAttempt(requestID string, number, statusCode int, headers http.Header, body io.Reader, attemptErr error, started time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byRequest[requestID]
	if !ok {
		return nil
	}
	record := m.records[id]
	disabled := m.disabled[requestID]
	attempt := Attempt{Number: number, StartedAt: started, FinishedAt: m.now(), StatusCode: statusCode}
	if attemptErr != nil {
		attempt.Error = string(redactText([]byte(attemptErr.Error())))
	}
	var newObject string
	var bodyFailed bool
	if body != nil && !disabled && number <= m.config().MaxAttemptsPerRequest {
		key, _, err := m.dataKeyLocked(record)
		if err != nil {
			return err
		}
		base := &BodyPart{Headers: safeHeaders(headers), ContentType: headers.Get("Content-Type")}
		part, err := m.writeObjectLocked(id, key, fmt.Sprintf("attempt-%03d.body.enc", number), body, base)
		if err != nil {
			record.Warnings = appendUnique(record.Warnings, err.Error())
			disabled = true
			bodyFailed = true
		} else {
			attempt.Response = &part
			record.CapturedBytes += part.StoredBytes
			newObject = part.Object
		}
	} else if body != nil && !disabled {
		record.Warnings = appendUnique(record.Warnings, "max attempts per request reached; later bodies were not captured")
		disabled = true
	}
	record.Attempts = append(record.Attempts, attempt)
	committed, persistErr := m.persistLocked(record)
	if !committed {
		if newObject != "" {
			m.removeObjectLocked(id, newObject)
		}
		return persistErr
	}
	m.records[id] = record
	if disabled {
		m.disabled[requestID] = true
	} else {
		delete(m.disabled, requestID)
	}
	if persistErr == nil && !bodyFailed {
		m.persistenceHealthy = true
	}
	return persistErr
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
	committed, persistErr := m.persistLocked(record)
	if !committed {
		return persistErr
	}
	delete(m.byRequest, requestID)
	delete(m.disabled, requestID)
	m.records[id] = record
	if persistErr == nil {
		m.persistenceHealthy = true
	}
	return persistErr
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
		committed, err := m.persistLocked(record)
		if committed {
			persisted = append(persisted, id)
		}
		if err != nil {
			var rollbackErrors []error
			for _, persistedID := range persisted {
				_, rollbackErr := m.persistLocked(originals[persistedID])
				if rollbackErr != nil {
					rollbackErrors = append(rollbackErrors, rollbackErr)
				}
			}
			return RewrapResult{}, errors.Join(append([]error{err}, rollbackErrors...)...)
		}
		if !committed {
			return RewrapResult{}, errors.New("capture metadata was not committed")
		}
	}
	for id, record := range updated {
		m.records[id] = record
	}
	result.Updated = len(updated)
	m.persistenceHealthy = true
	return result, nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return os.ErrNotExist
	}
	removedSize := directorySize(m.recordDir(id))
	if err := os.RemoveAll(m.recordDir(id)); err != nil {
		return m.failLocked("remove", err)
	}
	m.storageBytes = maxInt64(m.storageBytes-removedSize, 0)
	if err := m.syncDirLocked(m.root, "directory_sync"); err != nil {
		return err
	}
	delete(m.byRequest, record.RequestID)
	delete(m.disabled, record.RequestID)
	delete(m.records, id)
	return nil
}

func (m *Manager) DeleteExpired() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	removed := make([]string, 0)
	var errs []error
	for id, record := range m.records {
		if record.ExpiresAt.After(now) {
			continue
		}
		removedSize := directorySize(m.recordDir(id))
		if err := os.RemoveAll(m.recordDir(id)); err != nil {
			errs = append(errs, m.failLocked("remove", err))
			continue
		}
		m.storageBytes = maxInt64(m.storageBytes-removedSize, 0)
		removed = append(removed, id)
	}
	if len(removed) > 0 {
		if syncErr := m.syncDirLocked(m.root, "directory_sync"); syncErr != nil {
			errs = append(errs, syncErr)
			return 0, errors.Join(errs...)
		}
		for _, id := range removed {
			record := m.records[id]
			delete(m.records, id)
			delete(m.byRequest, record.RequestID)
			delete(m.disabled, record.RequestID)
		}
		m.emitLocked("capture.expired_deleted", "已删除到期捕获", map[string]any{"count": len(removed)})
	}
	return len(removed), errors.Join(errs...)
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
					if _, persistErr := m.persistLocked(record); persistErr != nil {
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
				if _, persistErr := m.persistLocked(record); persistErr != nil {
					return persistErr
				}
			}
			m.records[record.ID] = record
		}
	}
	m.storageBytes = m.scanStorageBytesLocked()
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
		return part, m.failLocked("create", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return part, m.failLocked("chmod", err)
	}
	original, stored, truncated, err := encryptChunks(key, &hookedWriter{manager: m, file: temporary, stage: "body_write"}, source, int64(m.config().MaxBodySize))
	if err != nil {
		m.recordFailureLocked("body_write", err)
	} else {
		err = m.syncFileLocked(temporary, "body_sync")
	}
	if closeErr := temporary.Close(); err == nil && closeErr != nil {
		err = m.failLocked("body_close", closeErr)
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
	if err := m.renameLocked(temporaryName, destination, "body_rename"); err != nil {
		return part, err
	}
	if err := m.syncDirLocked(directory, "directory_sync"); err != nil {
		_ = os.Remove(destination)
		_ = m.syncDirectoryOS(directory)
		return part, err
	}
	if info, statErr := os.Stat(destination); statErr == nil {
		m.storageBytes += info.Size()
	}
	part.Object = name
	part.OriginalBytes = original
	part.StoredBytes = stored
	part.Truncated = truncated
	return part, nil
}

// persistLocked returns committed=true once metadata.json has been atomically
// replaced. A later directory Sync failure is reported, but callers must still
// commit the corresponding in-memory state so it cannot move behind visible disk state.
func (m *Manager) persistLocked(record Record) (committed bool, resultErr error) {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return false, m.failLocked("metadata_encode", err)
	}
	path := filepath.Join(m.recordDir(record.ID), "metadata.json")
	previousSize := int64(0)
	if info, statErr := os.Stat(path); statErr == nil {
		previousSize = info.Size()
	}
	temporary, err := os.CreateTemp(m.recordDir(record.ID), ".metadata-*")
	if err != nil {
		return false, m.failLocked("metadata_create", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return false, m.failLocked("metadata_chmod", err)
	}
	if err := m.writeAllLocked(temporary, data, "metadata_write"); err != nil {
		temporary.Close()
		return false, err
	}
	if err := m.syncFileLocked(temporary, "metadata_sync"); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, m.failLocked("metadata_close", err)
	}
	if err := m.renameLocked(name, path, "metadata_rename"); err != nil {
		return false, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		m.storageBytes += info.Size() - previousSize
	}
	if err := m.syncDirLocked(m.recordDir(record.ID), "directory_sync"); err != nil {
		return true, err
	}
	return true, nil
}

type hookedWriter struct {
	manager *Manager
	file    *os.File
	stage   string
}

func (w *hookedWriter) Write(data []byte) (int, error) {
	write := w.manager.hooks.Write
	if write == nil {
		write = func(file *os.File, value []byte) (int, error) { return file.Write(value) }
	}
	written, err := write(w.file, data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return written, fmt.Errorf("capture %s: %w", w.stage, err)
	}
	return written, nil
}

func (m *Manager) writeAllLocked(file *os.File, data []byte, stage string) error {
	_, err := (&hookedWriter{manager: m, file: file, stage: stage}).Write(data)
	if err != nil {
		m.recordFailureLocked(stage, err)
	}
	return err
}

func (m *Manager) syncFileLocked(file *os.File, stage string) error {
	syncFile := m.hooks.Sync
	if syncFile == nil {
		syncFile = func(value *os.File) error { return value.Sync() }
	}
	if err := syncFile(file); err != nil {
		return m.failLocked(stage, err)
	}
	return nil
}

func (m *Manager) renameLocked(oldPath, newPath, stage string) error {
	rename := m.hooks.Rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(oldPath, newPath); err != nil {
		return m.failLocked(stage, err)
	}
	return nil
}

func (m *Manager) syncDirLocked(path, stage string) error {
	syncDir := m.hooks.SyncDir
	if syncDir == nil {
		syncDir = m.syncDirectoryOS
	}
	if err := syncDir(path); err != nil {
		return m.failLocked(stage, err)
	}
	return nil
}

func (m *Manager) syncDirectoryOS(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (m *Manager) removeRecordDirLocked(id string) {
	removedSize := directorySize(m.recordDir(id))
	_ = os.RemoveAll(m.recordDir(id))
	m.storageBytes = maxInt64(m.storageBytes-removedSize, 0)
	_ = m.syncDirectoryOS(m.root)
}

func (m *Manager) removeObjectLocked(id, name string) {
	path := filepath.Join(m.recordDir(id), name)
	removedSize := int64(0)
	if info, err := os.Stat(path); err == nil {
		removedSize = info.Size()
	}
	_ = os.Remove(path)
	m.storageBytes = maxInt64(m.storageBytes-removedSize, 0)
	_ = m.syncDirectoryOS(m.recordDir(id))
}

func (m *Manager) failLocked(stage string, err error) error {
	if err == nil {
		return nil
	}
	m.recordFailureLocked(stage, err)
	return fmt.Errorf("capture %s: %w", stage, err)
}

func (m *Manager) recordFailureLocked(stage string, err error) {
	m.persistenceHealthy = false
	m.failureCount++
	m.failedStage = stage
	m.lastFailureAt = m.now().UTC()
	m.emitLocked("capture.persistence_failed", "捕获持久化失败", map[string]any{"stage": stage, "reason": err.Error()})
}

func (m *Manager) capacityAvailableLocked(additional int64) error {
	cfg := m.config()
	if m.storageBytesLocked()+additional > int64(cfg.MaxTotalSize) {
		return errors.New("capture storage capacity reached")
	}
	available, err := disk.AvailableBytes(m.root)
	if err != nil {
		return err
	}
	if available-additional < int64(cfg.MinimumFreeDisk) {
		return errors.New("capture stopped because available disk is below the configured minimum")
	}
	return nil
}

func (m *Manager) storageBytesLocked() int64 {
	return m.storageBytes
}

func (m *Manager) scanStorageBytesLocked() int64 {
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

func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func maxInt64(value, minimum int64) int64 {
	if value > minimum {
		return value
	}
	return minimum
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
