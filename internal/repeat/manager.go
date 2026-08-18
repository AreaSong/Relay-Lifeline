package repeat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/journal"
)

const journalSnapshot = "repeat.snapshot"
const maxExecutionAudit = 100

var (
	ErrSourceNotFound = errors.New("active request source not found")
	ErrTaskNotFound   = errors.New("repeat task not found")
	ErrTaskExists     = errors.New("active repeat task already exists")
	ErrInvalidInput   = errors.New("invalid repeat task input")
)

type State string

const (
	StateRunning     State = "running"
	StatePaused      State = "paused"
	StateStopped     State = "stopped"
	StateExpired     State = "expired"
	StateInterrupted State = "interrupted"
)

type Template struct {
	Method    string
	Path      string
	Headers   http.Header
	Body      []byte
	Streaming bool
}

type CreateInput struct {
	Interval         time.Duration
	Duration         time.Duration
	Idempotency      string
	ConfirmForever   bool
	MaxExecutions    int
	MaxFailures      int
	FailureThreshold int
	MaxTokens        int64
}

type Execution struct {
	ID                   string
	Success              bool
	StatusCode           int
	ErrorCode            string
	Completed            time.Time
	DurationMilliseconds int64
	UsageTokens          int64
	UsageAvailable       bool
}

type ExecutionAudit struct {
	ID                   string    `json:"id"`
	StartedAt            time.Time `json:"startedAt"`
	Completed            time.Time `json:"completedAt"`
	Success              bool      `json:"success"`
	StatusCode           int       `json:"statusCode,omitempty"`
	ErrorCode            string    `json:"errorCode,omitempty"`
	DurationMilliseconds int64     `json:"durationMilliseconds"`
	UsageTokens          int64     `json:"usageTokens,omitempty"`
	UsageAvailable       bool      `json:"usageAvailable"`
}

type Executor func(context.Context, Template, string, string) Execution

type Task struct {
	ID                   string           `json:"id"`
	SourceRequestID      string           `json:"sourceRequestId"`
	Method               string           `json:"method"`
	Path                 string           `json:"path"`
	State                State            `json:"state"`
	Idempotency          string           `json:"idempotency"`
	IntervalMilliseconds int64            `json:"intervalMilliseconds"`
	DurationMilliseconds int64            `json:"durationMilliseconds"`
	StartedAt            time.Time        `json:"startedAt"`
	Deadline             time.Time        `json:"deadline,omitempty"`
	NextRunAt            time.Time        `json:"nextRunAt,omitempty"`
	LastRunAt            time.Time        `json:"lastRunAt,omitempty"`
	StoppedAt            time.Time        `json:"stoppedAt,omitempty"`
	Executions           int              `json:"executions"`
	Successes            int              `json:"successes"`
	Failures             int              `json:"failures"`
	LastOutcome          string           `json:"lastOutcome,omitempty"`
	LastStatusCode       int              `json:"lastStatusCode,omitempty"`
	LastErrorCode        string           `json:"lastErrorCode,omitempty"`
	LastExecutionID      string           `json:"lastExecutionId,omitempty"`
	InFlight             bool             `json:"inFlight"`
	MaxExecutions        int              `json:"maxExecutions,omitempty"`
	MaxFailures          int              `json:"maxFailures,omitempty"`
	FailureThreshold     int              `json:"failureThreshold,omitempty"`
	ConsecutiveFailures  int              `json:"consecutiveFailures,omitempty"`
	CircuitOpen          bool             `json:"circuitOpen,omitempty"`
	StopReason           string           `json:"stopReason,omitempty"`
	MaxTokens            int64            `json:"maxTokens,omitempty"`
	TokensUsed           int64            `json:"tokensUsed,omitempty"`
	LastUsageTokens      int64            `json:"lastUsageTokens,omitempty"`
	TokenUsageMissing    bool             `json:"tokenUsageMissing,omitempty"`
	ExecutionAudit       []ExecutionAudit `json:"executionAudit,omitempty"`
}

type runtimeTask struct {
	task     Task
	template Template
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{}
}

type Manager struct {
	mu       sync.RWMutex
	sources  map[string]Template
	tasks    map[string]*runtimeTask
	journal  *journal.Store
	executor Executor
	closed   bool
	now      func() time.Time
}

func New(eventJournal *journal.Store, executor Executor) (*Manager, error) {
	manager := &Manager{
		sources: make(map[string]Template), tasks: make(map[string]*runtimeTask),
		journal: eventJournal, executor: executor, now: time.Now,
	}
	if err := manager.replay(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) RegisterSource(id string, template Template) {
	m.mu.Lock()
	m.sources[id] = cloneTemplate(template)
	m.mu.Unlock()
}

func (m *Manager) UnregisterSource(id string) {
	m.mu.Lock()
	delete(m.sources, id)
	m.mu.Unlock()
}

func (m *Manager) Create(sourceID string, input CreateInput) (Task, error) {
	if !validInput(input) {
		return Task{}, ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Task{}, ErrInvalidInput
	}
	template, ok := m.sources[sourceID]
	if !ok {
		return Task{}, ErrSourceNotFound
	}
	if m.hasActiveTaskLocked(sourceID) {
		return Task{}, ErrTaskExists
	}
	now := m.now()
	id := newID()
	ctx, cancel := context.WithCancel(context.Background())
	task := Task{
		ID: id, SourceRequestID: sourceID, Method: template.Method, Path: template.Path,
		State: StateRunning, Idempotency: input.Idempotency,
		IntervalMilliseconds: input.Interval.Milliseconds(), DurationMilliseconds: input.Duration.Milliseconds(),
		StartedAt: now, NextRunAt: now, MaxExecutions: input.MaxExecutions, MaxFailures: input.MaxFailures, FailureThreshold: input.FailureThreshold, MaxTokens: input.MaxTokens,
	}
	if input.Duration > 0 {
		task.Deadline = now.Add(input.Duration)
	}
	runtime := &runtimeTask{task: task, template: cloneTemplate(template), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	if err := m.appendLocked(task); err != nil {
		cancel()
		return Task{}, err
	}
	m.tasks[id] = runtime
	go m.run(id)
	return task, nil
}

func validInput(input CreateInput) bool {
	if input.Interval < 5*time.Second || input.Interval > 24*time.Hour {
		return false
	}
	if input.Duration < 0 || input.Duration > 24*time.Hour {
		return false
	}
	if input.Duration == 0 && !input.ConfirmForever {
		return false
	}
	if input.MaxExecutions < 0 || input.MaxExecutions > 100000 || input.MaxFailures < 0 || input.MaxFailures > 100000 || input.FailureThreshold < 0 || input.FailureThreshold > 1000 || input.MaxTokens < 0 || input.MaxTokens > 1_000_000_000_000 {
		return false
	}
	if input.FailureThreshold > 0 && input.MaxFailures > 0 && input.FailureThreshold > input.MaxFailures {
		return false
	}
	return input.Idempotency == "preserve" || input.Idempotency == "regenerate"
}

func (m *Manager) List() []Task {
	m.mu.RLock()
	result := make([]Task, 0, len(m.tasks))
	for _, runtime := range m.tasks {
		result = append(result, runtime.task)
	}
	m.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	return result
}

func (m *Manager) Pause(id string) (Task, error) {
	return m.changeState(id, StatePaused)
}

func (m *Manager) Resume(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.tasks[id]
	if !ok || terminal(runtime.task.State) {
		return Task{}, ErrTaskNotFound
	}
	runtime.task.State = StateRunning
	runtime.task.CircuitOpen = false
	runtime.task.ConsecutiveFailures = 0
	runtime.task.StopReason = ""
	runtime.task.TokenUsageMissing = false
	runtime.task.NextRunAt = m.now()
	if err := m.appendLocked(runtime.task); err != nil {
		return Task{}, err
	}
	signal(runtime.wake)
	return runtime.task, nil
}

func (m *Manager) changeState(id string, state State) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.tasks[id]
	if !ok || terminal(runtime.task.State) {
		return Task{}, ErrTaskNotFound
	}
	runtime.task.State = state
	if state == StateRunning {
		runtime.task.NextRunAt = m.now()
	} else {
		runtime.task.NextRunAt = time.Time{}
	}
	if err := m.appendLocked(runtime.task); err != nil {
		return Task{}, err
	}
	signal(runtime.wake)
	return runtime.task, nil
}

func (m *Manager) RunNow(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.tasks[id]
	if !ok || terminal(runtime.task.State) {
		return Task{}, ErrTaskNotFound
	}
	runtime.task.State = StateRunning
	runtime.task.NextRunAt = m.now()
	runtime.task.TokenUsageMissing = false
	if err := m.appendLocked(runtime.task); err != nil {
		return Task{}, err
	}
	signal(runtime.wake)
	return runtime.task, nil
}

func (m *Manager) Stop(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.tasks[id]
	if !ok || terminal(runtime.task.State) {
		return Task{}, ErrTaskNotFound
	}
	runtime.task.State = StateStopped
	runtime.task.StoppedAt = m.now()
	runtime.task.NextRunAt = time.Time{}
	runtime.task.InFlight = false
	runtime.cancel()
	if err := m.appendLocked(runtime.task); err != nil {
		return Task{}, err
	}
	return runtime.task, nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, runtime := range m.tasks {
		if terminal(runtime.task.State) {
			continue
		}
		runtime.task.State = StateInterrupted
		runtime.task.StoppedAt = m.now()
		runtime.task.NextRunAt = time.Time{}
		runtime.task.InFlight = false
		runtime.cancel()
		_ = m.appendLocked(runtime.task)
	}
}

func (m *Manager) run(id string) {
	for {
		runtime, wait, ready := m.next(id)
		if !ready {
			return
		}
		if !waitFor(runtime.ctx, runtime.wake, wait) {
			return
		}
		if !m.beginExecution(id) {
			continue
		}
		executionID := id + "-" + newID()
		result := m.executor(runtime.ctx, cloneTemplate(runtime.template), runtime.task.Idempotency, executionID)
		m.finishExecution(id, result)
	}
}

func (m *Manager) next(id string) (*runtimeTask, time.Duration, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.tasks[id]
	if !ok || terminal(runtime.task.State) {
		return nil, 0, false
	}
	if runtime.task.State == StatePaused {
		return runtime, 24 * time.Hour, true
	}
	now := m.now()
	if !runtime.task.Deadline.IsZero() && !now.Before(runtime.task.Deadline) {
		m.expireLocked(runtime)
		return nil, 0, false
	}
	return runtime, max(runtime.task.NextRunAt.Sub(now), 0), true
}

func (m *Manager) beginExecution(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.tasks[id]
	if !ok || runtime.task.State != StateRunning || runtime.task.InFlight {
		return false
	}
	if runtime.task.MaxExecutions > 0 && runtime.task.Executions >= runtime.task.MaxExecutions {
		m.finishLocked(runtime, StateExpired, "max_executions")
		return false
	}
	if !runtime.task.Deadline.IsZero() && !m.now().Before(runtime.task.Deadline) {
		m.expireLocked(runtime)
		return false
	}
	runtime.task.InFlight = true
	runtime.task.LastRunAt = m.now()
	runtime.task.NextRunAt = time.Time{}
	_ = m.appendLocked(runtime.task)
	return true
}

func (m *Manager) finishExecution(id string, result Execution) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.tasks[id]
	if !ok || terminal(runtime.task.State) {
		return
	}
	runtime.task.InFlight = false
	runtime.task.Executions++
	runtime.task.LastExecutionID = result.ID
	runtime.task.LastStatusCode = result.StatusCode
	runtime.task.LastErrorCode = result.ErrorCode
	runtime.task.LastUsageTokens = result.UsageTokens
	if result.Completed.IsZero() {
		result.Completed = m.now()
	}
	runtime.task.ExecutionAudit = append(runtime.task.ExecutionAudit, ExecutionAudit{
		ID: result.ID, StartedAt: runtime.task.LastRunAt, Completed: result.Completed,
		Success: result.Success, StatusCode: result.StatusCode, ErrorCode: result.ErrorCode,
		DurationMilliseconds: result.DurationMilliseconds, UsageTokens: result.UsageTokens, UsageAvailable: result.UsageAvailable,
	})
	if len(runtime.task.ExecutionAudit) > maxExecutionAudit {
		runtime.task.ExecutionAudit = runtime.task.ExecutionAudit[len(runtime.task.ExecutionAudit)-maxExecutionAudit:]
	}
	if result.Success {
		runtime.task.Successes++
		runtime.task.LastOutcome = "successful"
		runtime.task.ConsecutiveFailures = 0
		runtime.task.CircuitOpen = false
		runtime.task.StopReason = ""
	} else {
		runtime.task.Failures++
		runtime.task.LastOutcome = "failed"
		runtime.task.ConsecutiveFailures++
	}
	if runtime.task.MaxTokens > 0 {
		if !result.UsageAvailable {
			runtime.task.TokenUsageMissing = true
			runtime.task.LastOutcome = "usage_missing"
			runtime.task.LastErrorCode = "usage_missing"
			runtime.task.State = StatePaused
			runtime.task.StopReason = "usage_missing"
			runtime.task.NextRunAt = time.Time{}
			_ = m.appendLocked(runtime.task)
			return
		}
		runtime.task.TokenUsageMissing = false
		runtime.task.TokensUsed += result.UsageTokens
		if runtime.task.TokensUsed >= runtime.task.MaxTokens {
			m.finishLocked(runtime, StateExpired, "max_tokens")
			_ = m.appendLocked(runtime.task)
			return
		}
	}
	if runtime.task.MaxFailures > 0 && runtime.task.Failures >= runtime.task.MaxFailures {
		m.finishLocked(runtime, StateExpired, "max_failures")
	} else if !result.Success && runtime.task.FailureThreshold > 0 && runtime.task.ConsecutiveFailures >= runtime.task.FailureThreshold {
		runtime.task.State = StatePaused
		runtime.task.CircuitOpen = true
		runtime.task.StopReason = "failure_circuit_open"
		runtime.task.NextRunAt = time.Time{}
	} else if runtime.task.MaxExecutions > 0 && runtime.task.Executions >= runtime.task.MaxExecutions {
		m.finishLocked(runtime, StateExpired, "max_executions")
	}
	if runtime.task.State == StateRunning {
		runtime.task.NextRunAt = m.now().Add(time.Duration(runtime.task.IntervalMilliseconds) * time.Millisecond)
	}
	_ = m.appendLocked(runtime.task)
}

func (m *Manager) expireLocked(runtime *runtimeTask) {
	m.finishLocked(runtime, StateExpired, "deadline")
	_ = m.appendLocked(runtime.task)
}

func (m *Manager) finishLocked(runtime *runtimeTask, state State, reason string) {
	runtime.task.State = state
	runtime.task.StopReason = reason
	runtime.task.StoppedAt = m.now()
	runtime.task.NextRunAt = time.Time{}
	runtime.task.InFlight = false
	runtime.cancel()
}

func (m *Manager) replay() error {
	if m.journal == nil {
		return nil
	}
	for _, entry := range m.journal.Entries() {
		if entry.Type != journalSnapshot {
			return errors.New("unsupported repeat journal entry")
		}
		var task Task
		if err := json.Unmarshal(entry.Payload, &task); err != nil {
			return err
		}
		m.tasks[task.ID] = &runtimeTask{task: task}
	}
	for _, runtime := range m.tasks {
		if terminal(runtime.task.State) {
			continue
		}
		runtime.task.State = StateInterrupted
		runtime.task.StoppedAt = m.now()
		runtime.task.NextRunAt = time.Time{}
		runtime.task.InFlight = false
		if err := m.appendLocked(runtime.task); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) appendLocked(task Task) error {
	if m.journal == nil {
		return nil
	}
	_, err := m.journal.Append(task.ID, journalSnapshot, task)
	return err
}

func (m *Manager) hasActiveTaskLocked(sourceID string) bool {
	for _, runtime := range m.tasks {
		if runtime.task.SourceRequestID == sourceID && !terminal(runtime.task.State) {
			return true
		}
	}
	return false
}
