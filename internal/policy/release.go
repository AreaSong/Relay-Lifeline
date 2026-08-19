package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/journal"
)

var (
	ErrDraftNotFound   = errors.New("policy draft not found")
	ErrReleaseConflict = errors.New("policy release revision conflict")
	ErrReleaseAction   = errors.New("unsupported policy release action")
	ErrReleaseLedger   = errors.New("policy release ledger unavailable")
)

const (
	eventDraftSaved        = "traffic_policy.draft_saved"
	eventReleasePrepared   = "traffic_policy.release_prepared"
	eventReleasePublished  = "traffic_policy.release_published"
	eventReleaseRolledBack = "traffic_policy.release_rolled_back"
	eventReleaseAborted    = "traffic_policy.release_aborted"
)

type ReleaseRecord struct {
	OperationID   string                     `json:"operationId,omitempty"`
	Revision      string                     `json:"revision"`
	Stage         string                     `json:"stage"`
	CanaryPercent int                        `json:"canaryPercent"`
	CreatedAt     time.Time                  `json:"createdAt"`
	Actor         string                     `json:"actor,omitempty"`
	Policy        config.TrafficPolicyConfig `json:"policy"`
}

type ReleaseStatus struct {
	CurrentRevision string                      `json:"currentRevision"`
	CurrentStage    string                      `json:"currentStage"`
	DraftRevision   string                      `json:"draftRevision,omitempty"`
	Draft           *config.TrafficPolicyConfig `json:"draft,omitempty"`
	History         []ReleaseRecord             `json:"history"`
	Pending         int                         `json:"pending"`
}

type releaseIntent struct {
	OperationID string        `json:"operationId"`
	Rollback    bool          `json:"rollback"`
	Record      ReleaseRecord `json:"record"`
	Reason      string        `json:"reason,omitempty"`
}

// ReleaseManager owns the control-plane lifecycle of traffic policies. The
// active policy remains in config.Store; this manager only holds drafts and a
// bounded, auditable release history for the admin API.
type ReleaseManager struct {
	mu      sync.RWMutex
	draft   *config.TrafficPolicyConfig
	history []ReleaseRecord
	pending map[string]releaseIntent
	limit   int
	now     func() time.Time
	journal *journal.Store
}

func NewReleaseManager() *ReleaseManager {
	return &ReleaseManager{limit: 50, now: time.Now, pending: make(map[string]releaseIntent)}
}

func NewPersistentReleaseManager(store *journal.Store) (*ReleaseManager, error) {
	manager := NewReleaseManager()
	manager.journal = store
	if store == nil {
		return manager, nil
	}
	for _, entry := range store.Entries() {
		switch entry.Type {
		case eventDraftSaved:
			var policy config.TrafficPolicyConfig
			if err := json.Unmarshal(entry.Payload, &policy); err != nil {
				return nil, fmt.Errorf("%w: replay draft: %v", ErrReleaseLedger, err)
			}
			copy := clonePolicyConfig(policy)
			manager.draft = &copy
		case eventReleasePrepared:
			var intent releaseIntent
			if err := json.Unmarshal(entry.Payload, &intent); err != nil {
				return nil, fmt.Errorf("%w: replay prepared release: %v", ErrReleaseLedger, err)
			}
			if intent.OperationID == "" {
				intent.OperationID = releaseOperationID(intent.Record)
			}
			manager.pending[intent.OperationID] = intent
		case eventReleasePublished, eventReleaseRolledBack:
			var record ReleaseRecord
			if err := json.Unmarshal(entry.Payload, &record); err != nil {
				return nil, fmt.Errorf("%w: replay release: %v", ErrReleaseLedger, err)
			}
			manager.appendHistoryLocked(record)
			delete(manager.pending, releaseOperationID(record))
			manager.draft = nil
		case eventReleaseAborted:
			var intent releaseIntent
			if err := json.Unmarshal(entry.Payload, &intent); err != nil {
				return nil, fmt.Errorf("%w: replay aborted release: %v", ErrReleaseLedger, err)
			}
			if intent.OperationID == "" {
				intent.OperationID = releaseOperationID(intent.Record)
			}
			delete(manager.pending, intent.OperationID)
		}
	}
	if len(manager.history) > manager.limit {
		manager.history = manager.history[len(manager.history)-manager.limit:]
	}
	return manager, nil
}

func (m *ReleaseManager) Status(current config.TrafficPolicyConfig) ReleaseStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 即使发布账本为空，也保持 history 为数组，稳定管理端 API 契约。
	history := make([]ReleaseRecord, 0, len(m.history))
	history = append(history, m.history...)
	status := ReleaseStatus{CurrentRevision: policyRevision(current), CurrentStage: current.ReleaseStage, History: history, Pending: len(m.pending)}
	if m.draft != nil {
		copy := clonePolicyConfig(*m.draft)
		status.Draft = &copy
		status.DraftRevision = policyRevision(copy)
	}
	return status
}

func (m *ReleaseManager) SaveDraft(candidate config.TrafficPolicyConfig, expectedRevision string) (string, error) {
	if err := validateCandidate(candidate); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if expectedRevision != "" && m.draft != nil && expectedRevision != policyRevision(*m.draft) {
		return "", ErrReleaseConflict
	}
	copy := clonePolicyConfig(candidate)
	copy.ReleaseStage = "draft"
	copy.Revision = policyRevision(copy)
	if err := m.appendLocked(copy.Revision, eventDraftSaved, copy); err != nil {
		return "", err
	}
	m.draft = &copy
	return copy.Revision, nil
}

func (m *ReleaseManager) Candidate(candidate *config.TrafficPolicyConfig) (config.TrafficPolicyConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if candidate != nil {
		return clonePolicyConfig(*candidate), nil
	}
	if m.draft == nil {
		return config.TrafficPolicyConfig{}, ErrDraftNotFound
	}
	return clonePolicyConfig(*m.draft), nil
}

func (m *ReleaseManager) PreparePublish(candidate *config.TrafficPolicyConfig, stage string, canaryPercent int, actor string) (config.TrafficPolicyConfig, ReleaseRecord, error) {
	if stage != "shadow" && stage != "canary" && stage != "full" {
		return config.TrafficPolicyConfig{}, ReleaseRecord{}, ErrReleaseAction
	}
	policy, err := m.Candidate(candidate)
	if err != nil {
		return config.TrafficPolicyConfig{}, ReleaseRecord{}, err
	}
	policy.ReleaseStage, policy.CanaryPercent = stage, canaryPercent
	if stage == "canary" && (canaryPercent < 1 || canaryPercent > 100) {
		return config.TrafficPolicyConfig{}, ReleaseRecord{}, fmt.Errorf("canary percent must be between 1 and 100")
	}
	if stage != "canary" {
		policy.CanaryPercent = 100
	}
	if err := validateCandidate(policy); err != nil {
		return config.TrafficPolicyConfig{}, ReleaseRecord{}, err
	}
	policy.Revision = policyRevision(policy)
	record := ReleaseRecord{Revision: policy.Revision, Stage: stage, CanaryPercent: policy.CanaryPercent, CreatedAt: m.now().UTC(), Actor: actor, Policy: clonePolicyConfig(policy)}
	record.OperationID = releaseOperationID(record)
	return policy, record, nil
}

func (m *ReleaseManager) PrepareRollback(record ReleaseRecord, actor string) (config.TrafficPolicyConfig, ReleaseRecord, error) {
	policy := clonePolicyConfig(record.Policy)
	policy.ReleaseStage, policy.CanaryPercent = "full", 100
	policy.Revision = policyRevision(policy)
	if err := validateCandidate(policy); err != nil {
		return config.TrafficPolicyConfig{}, ReleaseRecord{}, err
	}
	rollback := ReleaseRecord{Revision: policy.Revision, Stage: "full", CanaryPercent: 100, CreatedAt: m.now().UTC(), Actor: actor, Policy: clonePolicyConfig(policy)}
	rollback.OperationID = releaseOperationID(rollback)
	return policy, rollback, nil
}

// PrepareCommit durably records the intended policy transition before config
// storage is changed. Reconciliation can distinguish an interrupted commit
// from a policy that was actually applied.
func (m *ReleaseManager) PrepareCommit(record ReleaseRecord, rollback bool) error {
	if record.OperationID == "" {
		record.OperationID = releaseOperationID(record)
	}
	intent := releaseIntent{OperationID: record.OperationID, Rollback: rollback, Record: record}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.pending[intent.OperationID]; exists {
		return nil
	}
	if err := m.appendLocked(intent.OperationID, eventReleasePrepared, intent); err != nil {
		return err
	}
	m.pending[intent.OperationID] = intent
	return nil
}

// FinalizeCommit writes the applied event after config persistence succeeds.
func (m *ReleaseManager) FinalizeCommit(record ReleaseRecord, rollback bool) error {
	eventType := eventReleasePublished
	if rollback {
		eventType = eventReleaseRolledBack
	}
	if record.OperationID == "" {
		record.OperationID = releaseOperationID(record)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.appendLocked(record.Revision, eventType, record); err != nil {
		return err
	}
	m.appendHistoryLocked(record)
	delete(m.pending, record.OperationID)
	m.draft = nil
	return nil
}

// AbortCommit closes a prepared intent when config persistence did not apply.
// If the ledger is unavailable, the intent remains pending and is reconciled
// on the next startup against the active config revision.
func (m *ReleaseManager) AbortCommit(record ReleaseRecord, rollback bool, reason string) error {
	if record.OperationID == "" {
		record.OperationID = releaseOperationID(record)
	}
	intent := releaseIntent{OperationID: record.OperationID, Rollback: rollback, Record: record, Reason: reason}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.appendLocked(intent.OperationID, eventReleaseAborted, intent); err != nil {
		return err
	}
	delete(m.pending, intent.OperationID)
	return nil
}

// Commit preserves the original unit-test and embedding API by executing the
// two phases back-to-back.
func (m *ReleaseManager) Commit(record ReleaseRecord, rollback bool) error {
	if err := m.PrepareCommit(record, rollback); err != nil {
		return err
	}
	return m.FinalizeCommit(record, rollback)
}

// Reconcile resolves prepared intents left by a crash between config and
// ledger writes. A matching active revision is finalized; otherwise the
// intent is explicitly aborted.
func (m *ReleaseManager) Reconcile(current config.TrafficPolicyConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, intent := range m.pending {
		if policyRevision(current) == intent.Record.Revision {
			eventType := eventReleasePublished
			if intent.Rollback {
				eventType = eventReleaseRolledBack
			}
			if err := m.appendLocked(intent.OperationID, eventType, intent.Record); err != nil {
				return err
			}
			m.appendHistoryLocked(intent.Record)
			m.draft = nil
		} else {
			intent.Reason = "active_revision_mismatch"
			if err := m.appendLocked(intent.OperationID, eventReleaseAborted, intent); err != nil {
				return err
			}
		}
		delete(m.pending, id)
	}
	return nil
}

func (m *ReleaseManager) FindRevision(revision string) (ReleaseRecord, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for index := len(m.history) - 1; index >= 0; index-- {
		if m.history[index].Revision == revision {
			return m.history[index], true
		}
	}
	return ReleaseRecord{}, false
}

func (m *ReleaseManager) appendHistoryLocked(record ReleaseRecord) {
	for _, existing := range m.history {
		if record.OperationID != "" && existing.OperationID == record.OperationID {
			return
		}
	}
	m.history = append(m.history, record)
	if len(m.history) > m.limit {
		m.history = m.history[len(m.history)-m.limit:]
	}
}

func (m *ReleaseManager) appendLocked(entityID, eventType string, payload any) error {
	if m.journal == nil {
		return nil
	}
	if _, err := m.journal.Append(entityID, eventType, payload); err != nil {
		return fmt.Errorf("%w: %v", ErrReleaseLedger, err)
	}
	return nil
}

func validateCandidate(policy config.TrafficPolicyConfig) error {
	ids := make([]string, 0, len(policy.Rules)+1)
	for _, rule := range policy.Rules {
		if rule.TargetID != "" {
			ids = append(ids, rule.TargetID)
		}
	}
	if policy.Shadow.TargetID != "" {
		ids = append(ids, policy.Shadow.TargetID)
	}
	return config.ValidateTrafficPolicyConfig(policy, ids)
}

func policyRevision(policy config.TrafficPolicyConfig) string {
	policy.Revision = ""
	data, _ := json.Marshal(policy)
	digest := sha256.Sum256([]byte(data))
	return hex.EncodeToString(digest[:])[:16]
}

func releaseOperationID(record ReleaseRecord) string {
	data, _ := json.Marshal(struct {
		Revision      string
		Stage         string
		CanaryPercent int
		CreatedAt     time.Time
		Actor         string
	}{record.Revision, record.Stage, record.CanaryPercent, record.CreatedAt, record.Actor})
	digest := sha256.Sum256(data)
	return "release-" + hex.EncodeToString(digest[:])[:20]
}

func clonePolicyConfig(policy config.TrafficPolicyConfig) config.TrafficPolicyConfig {
	policy.Rules = append([]config.TrafficPolicyRule(nil), policy.Rules...)
	return policy
}
