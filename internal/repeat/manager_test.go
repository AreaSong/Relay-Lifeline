package repeat

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/journal"
)

func TestManagerContinuesAfterSuccessAndFailureWithoutOverlap(t *testing.T) {
	var calls atomic.Int32
	var inFlight atomic.Int32
	var maximum atomic.Int32
	executor := func(_ context.Context, _ Template, _ string, id string) Execution {
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		if current > maximum.Load() {
			maximum.Store(current)
		}
		call := calls.Add(1)
		time.Sleep(5 * time.Millisecond)
		return Execution{ID: id, Success: call%2 == 1, Completed: time.Now()}
	}
	manager, err := New(nil, executor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	manager.RegisterSource("source", testTemplate())
	task, err := manager.Create("source", validCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	waitForExecutions(t, manager, task.ID, 1)
	if _, err := manager.RunNow(task.ID); err != nil {
		t.Fatal(err)
	}
	waitForExecutions(t, manager, task.ID, 2)
	if _, err := manager.RunNow(task.ID); err != nil {
		t.Fatal(err)
	}
	result := waitForExecutions(t, manager, task.ID, 3)
	if result.Successes != 2 || result.Failures != 1 {
		t.Fatalf("成功/失败后都应继续执行: %+v", result)
	}
	if maximum.Load() != 1 {
		t.Fatalf("同一任务发生重叠执行: maximum=%d", maximum.Load())
	}
}

func TestManagerPauseResumeRunNowAndStop(t *testing.T) {
	manager, err := New(nil, func(_ context.Context, _ Template, _ string, id string) Execution {
		return Execution{ID: id, Success: true, Completed: time.Now()}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	manager.RegisterSource("source", testTemplate())
	task, err := manager.Create("source", validCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	waitForExecutions(t, manager, task.ID, 1)
	paused, err := manager.Pause(task.ID)
	if err != nil || paused.State != StatePaused || !paused.NextRunAt.IsZero() {
		t.Fatalf("暂停结果异常: task=%+v err=%v", paused, err)
	}
	resumed, err := manager.Resume(task.ID)
	if err != nil || resumed.State != StateRunning {
		t.Fatalf("恢复结果异常: task=%+v err=%v", resumed, err)
	}
	waitForExecutions(t, manager, task.ID, 2)
	if _, err := manager.Pause(task.ID); err != nil {
		t.Fatal(err)
	}
	running, err := manager.RunNow(task.ID)
	if err != nil || running.State != StateRunning {
		t.Fatalf("立即执行应恢复暂停任务: task=%+v err=%v", running, err)
	}
	waitForExecutions(t, manager, task.ID, 3)
	stopped, err := manager.Stop(task.ID)
	if err != nil || stopped.State != StateStopped {
		t.Fatalf("停止结果异常: task=%+v err=%v", stopped, err)
	}
	if _, err := manager.RunNow(task.ID); err != ErrTaskNotFound {
		t.Fatalf("停止后的任务不应再次执行: %v", err)
	}
}

func TestManagerExpiresAtDeadline(t *testing.T) {
	base := time.Now()
	var nanoseconds atomic.Int64
	nanoseconds.Store(base.UnixNano())
	manager, err := New(nil, func(_ context.Context, _ Template, _ string, id string) Execution {
		return Execution{ID: id, Success: true, Completed: time.Now()}
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Unix(0, nanoseconds.Load()) }
	t.Cleanup(manager.Close)
	manager.RegisterSource("source", testTemplate())
	input := validCreateInput()
	input.Duration = 10 * time.Second
	task, err := manager.Create("source", input)
	if err != nil {
		t.Fatal(err)
	}
	waitForExecutions(t, manager, task.ID, 1)
	nanoseconds.Store(base.Add(input.Duration).UnixNano())
	if _, err := manager.RunNow(task.ID); err != nil {
		t.Fatal(err)
	}
	result := waitForTask(t, manager, task.ID, func(task Task) bool { return task.State == StateExpired })
	if result.Executions != 1 || result.StoppedAt.IsZero() {
		t.Fatalf("截止后不应再执行: %+v", result)
	}
}

func TestManagerValidatesBoundariesAndActiveSource(t *testing.T) {
	manager, err := New(nil, func(context.Context, Template, string, string) Execution { return Execution{} })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	if _, err := manager.Create("missing", validCreateInput()); err != ErrSourceNotFound {
		t.Fatalf("非活动请求应被拒绝: %v", err)
	}
	manager.RegisterSource("source", testTemplate())
	invalid := validCreateInput()
	invalid.Interval = 4999 * time.Millisecond
	if _, err := manager.Create("source", invalid); err != ErrInvalidInput {
		t.Fatalf("小于 5 秒的间隔应被拒绝: %v", err)
	}
	invalid = validCreateInput()
	invalid.Duration = 0
	invalid.ConfirmForever = false
	if _, err := manager.Create("source", invalid); err != ErrInvalidInput {
		t.Fatalf("永久运行未经确认应被拒绝: %v", err)
	}
}

func TestManagerJournalExcludesTemplateAndInterruptsOnReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repeat.jsonl")
	store, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, func(_ context.Context, _ Template, _ string, id string) Execution {
		return Execution{ID: id, Success: true, Completed: time.Now()}
	})
	if err != nil {
		t.Fatal(err)
	}
	template := testTemplate()
	template.Headers.Set("Authorization", "Bearer must-not-be-persisted")
	template.Body = []byte("secret-body-must-not-be-persisted")
	manager.RegisterSource("source", template)
	task, err := manager.Create("source", validCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	waitForExecutions(t, manager, task.ID, 1)
	manager.Pause(task.ID)
	manager.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"must-not-be-persisted", "secret-body"} {
		if strings.Contains(string(contents), secret) {
			t.Fatalf("持续任务 Journal 泄露请求模板: %q", secret)
		}
	}
	replayedStore, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer replayedStore.Close()
	replayed, err := New(replayedStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	tasks := replayed.List()
	if len(tasks) != 1 || tasks[0].State != StateInterrupted || tasks[0].InFlight {
		t.Fatalf("重启后活动任务应标记为中断: %+v", tasks)
	}
}

func validCreateInput() CreateInput {
	return CreateInput{Interval: 5 * time.Second, Duration: time.Minute, Idempotency: "preserve"}
}

func testTemplate() Template {
	return Template{Method: "POST", Path: "/v1/responses", Headers: make(http.Header)}
}

func waitForExecutions(t *testing.T, manager *Manager, id string, executions int) Task {
	t.Helper()
	return waitForTask(t, manager, id, func(task Task) bool { return task.Executions >= executions })
}

func waitForTask(t *testing.T, manager *Manager, id string, ready func(Task) bool) Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, task := range manager.List() {
			if task.ID == id && ready(task) {
				return task
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待持续任务状态超时: id=%s", id)
	return Task{}
}
