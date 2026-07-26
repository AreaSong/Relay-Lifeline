package risk

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
)

const maxAlerts = 200

type Alert struct {
	ID                  string         `json:"id"`
	Type                string         `json:"type"`
	Severity            string         `json:"severity"`
	RequestID           string         `json:"requestId,omitempty"`
	Message             string         `json:"message"`
	MessageCode         string         `json:"messageCode,omitempty"`
	MessageDetails      map[string]any `json:"messageDetails,omitempty"`
	Attempts            int            `json:"attempts,omitempty"`
	ElapsedMilliseconds int64          `json:"elapsedMilliseconds,omitempty"`
	CreatedAt           time.Time      `json:"createdAt"`
	ResolvedAt          *time.Time     `json:"resolvedAt,omitempty"`
}

type Manager struct {
	mu         sync.Mutex
	alerts     []Alert
	open       map[string]string
	authCounts map[string]int
	now        func() time.Time
}

func New() *Manager {
	return &Manager{open: make(map[string]string), authCounts: make(map[string]int), now: time.Now}
}

func (m *Manager) EvaluateAttempt(requestID string, attempt int, started time.Time, statusCode int, cfg config.RiskConfig) []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	var created []Alert
	elapsed := m.now().Sub(started)
	if attempt >= cfg.WarningAttempts {
		created = appendCreated(created, m.emitLocked(requestID+"|many_attempts", Alert{
			Type: "many_attempts", Severity: "warning", RequestID: requestID,
			MessageCode: "risk.many_attempts", MessageDetails: map[string]any{"Attempts": attempt}, Attempts: attempt,
			ElapsedMilliseconds: elapsed.Milliseconds(),
		}))
	}
	if elapsed >= cfg.WarningAfter.Duration {
		created = appendCreated(created, m.emitLocked(requestID+"|long_running", Alert{
			Type: "long_running", Severity: "warning", RequestID: requestID,
			MessageCode: "risk.long_running", Attempts: attempt,
			ElapsedMilliseconds: elapsed.Milliseconds(),
		}))
	}
	created = append(created, m.evaluateAuthLocked(requestID, attempt, statusCode, elapsed, cfg.AuthErrorAttempts)...)
	return created
}

func (m *Manager) EvaluateLongRunning(requestID string, attempt int, started time.Time, cfg config.RiskConfig) []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	elapsed := m.now().Sub(started)
	if elapsed < cfg.WarningAfter.Duration {
		return nil
	}
	alert := m.emitLocked(requestID+"|long_running", Alert{
		Type: "long_running", Severity: "warning", RequestID: requestID,
		MessageCode: "risk.long_running", Attempts: attempt,
		ElapsedMilliseconds: elapsed.Milliseconds(),
	})
	return appendCreated(nil, alert)
}

func (m *Manager) EvaluateQueue(active, waiting, maxActive, maxWaiting int, cfg config.RiskConfig) []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	capacity := maxActive + maxWaiting
	if capacity < 1 {
		return nil
	}
	percent := (active + waiting) * 100 / capacity
	if percent < cfg.QueueWarningPercent {
		m.resolveLocked("global|queue_pressure")
		return nil
	}
	alert := m.emitLocked("global|queue_pressure", Alert{
		Type: "queue_pressure", Severity: "warning",
		MessageCode: "risk.queue_pressure", MessageDetails: map[string]any{"Percent": percent},
	})
	return appendCreated(nil, alert)
}

func (m *Manager) RecordGlobal(alertType, severity, message string) []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	alert := m.emitLocked("global|"+alertType, Alert{Type: alertType, Severity: severity, Message: message})
	return appendCreated(nil, alert)
}

func (m *Manager) RecordGlobalMessage(alertType, severity string, message l10n.Message) []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	alert := m.emitLocked("global|"+alertType, Alert{Type: alertType, Severity: severity, MessageCode: message.ID, MessageDetails: message.Data})
	return appendCreated(nil, alert)
}

func (m *Manager) ResolveRequest(requestID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := requestID + "|"
	for key := range m.open {
		if strings.HasPrefix(key, prefix) {
			m.resolveLocked(key)
		}
	}
	delete(m.authCounts, requestID)
}

func (m *Manager) ResolveGlobal(alertType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolveLocked("global|" + alertType)
}

func (m *Manager) HasOpenRequestAlerts(requestID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := requestID + "|"
	for key := range m.open {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (m *Manager) Recent(limit int) []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > len(m.alerts) {
		limit = len(m.alerts)
	}
	result := make([]Alert, limit)
	copy(result, m.alerts[:limit])
	return result
}

func (m *Manager) evaluateAuthLocked(requestID string, attempt, statusCode int, elapsed time.Duration, threshold int) []Alert {
	if statusCode != 401 && statusCode != 403 {
		m.authCounts[requestID] = 0
		return nil
	}
	m.authCounts[requestID]++
	if m.authCounts[requestID] < threshold {
		return nil
	}
	alert := m.emitLocked(requestID+"|auth_errors", Alert{
		Type: "auth_errors", Severity: "warning", RequestID: requestID,
		MessageCode: "risk.auth_errors", MessageDetails: map[string]any{"Count": m.authCounts[requestID], "Status": statusCode},
		Attempts: attempt, ElapsedMilliseconds: elapsed.Milliseconds(),
	})
	return appendCreated(nil, alert)
}

func (m *Manager) emitLocked(key string, alert Alert) *Alert {
	if _, exists := m.open[key]; exists {
		return nil
	}
	alert.ID = newID()
	alert.CreatedAt = m.now()
	m.alerts = append([]Alert{alert}, m.alerts...)
	if len(m.alerts) > maxAlerts {
		for _, removed := range m.alerts[maxAlerts:] {
			m.removeOpenAlertLocked(removed.ID)
		}
		m.alerts = m.alerts[:maxAlerts]
	}
	m.open[key] = alert.ID
	return &alert
}

func (m *Manager) removeOpenAlertLocked(id string) {
	for key, alertID := range m.open {
		if alertID == id {
			delete(m.open, key)
		}
	}
}

func (m *Manager) resolveLocked(key string) {
	id, ok := m.open[key]
	if !ok {
		return
	}
	now := m.now()
	for index := range m.alerts {
		if m.alerts[index].ID == id {
			m.alerts[index].ResolvedAt = &now
			break
		}
	}
	delete(m.open, key)
}

func appendCreated(alerts []Alert, alert *Alert) []Alert {
	if alert == nil {
		return alerts
	}
	return append(alerts, *alert)
}

func Localize(alerts []Alert, locale, fallback string) []Alert {
	result := make([]Alert, len(alerts))
	for index, alert := range alerts {
		alert.MessageDetails = cloneDetails(alert.MessageDetails)
		if alert.MessageCode != "" {
			alert.Message = l10n.Default.Text(locale, fallback, l10n.M(alert.MessageCode, alert.MessageDetails))
		}
		result[index] = alert
	}
	return result
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	result := make(map[string]any, len(details))
	for key, value := range details {
		result[key] = value
	}
	return result
}

func newID() string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
