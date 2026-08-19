package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

func TestControllerPauseAndResume(t *testing.T) {
	controller := NewController()
	controller.Pause()
	done := make(chan error, 1)
	go func() { done <- controller.Wait(context.Background()) }()
	select {
	case <-done:
		t.Fatal("暂停时不应继续")
	case <-time.After(20 * time.Millisecond):
	}
	controller.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("恢复超时")
	}
}

func TestControllerDrainAndMaintenanceRejectAdmissionWithoutBlockingExistingWork(t *testing.T) {
	controller := NewController()
	if !controller.Drain() || controller.Mode() != ControlDraining || controller.Accepting() {
		t.Fatalf("排空状态异常: mode=%s accepting=%t", controller.Mode(), controller.Accepting())
	}
	if err := controller.Wait(context.Background()); err != nil {
		t.Fatalf("排空不应阻塞已接收请求: %v", err)
	}
	if !controller.Maintenance() || controller.Mode() != ControlMaintenance || !controller.Resume() || controller.Mode() != ControlRunning {
		t.Fatalf("维护恢复状态异常: mode=%s", controller.Mode())
	}
}

func TestTerminalJournalFailureKeepsExplicitPendingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	requestJournal, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	store, err := timeline.NewPersistent(func() timeline.Limits {
		return timeline.Limits{MaxItems: 100, Retention: time.Hour}
	}, requestJournal)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store)
	id, _, err := registry.AddWithIdentityChecked("POST", "/v1/responses", func() {}, RequestIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	registry.UpdateMessage(id, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(id, lifecycle.StateBuffering, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(id, lifecycle.StateDelivering, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(id, lifecycle.StateCompleted, 1, l10n.Message{}, time.Time{})
	requestJournal.SetHooks(journal.Hooks{Write: func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }})

	err = registry.Remove(id, lifecycle.StateSuccessful)
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("终态写入应保留 ENOSPC: %v", err)
	}
	snapshot := registry.Snapshot(false)
	if !snapshot.PersistenceDegraded || snapshot.PersistencePending != 1 || snapshot.Active != 0 || snapshot.Successful != 1 || len(snapshot.Requests) != 1 {
		t.Fatalf("终态持久化降级状态异常: %+v", snapshot)
	}
	request := snapshot.Requests[0]
	if request.ID != id || !request.PersistenceDegraded || !request.PersistencePending || request.State != lifecycle.StateCompleted {
		t.Fatalf("待持久化请求状态异常: %+v", request)
	}
	if record, ok := store.Request(id); !ok || !record.CompletedAt.IsZero() {
		t.Fatalf("timeline 内存不应领先失败的终态写入: %+v ok=%v", record, ok)
	}
	if status := requestJournal.Status(); status.State != journal.StateDegraded || status.FailedStage != "write" {
		t.Fatalf("Journal 健康状态异常: %+v", status)
	}
}

func TestRegistryRetryAndCancel(t *testing.T) {
	registry := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	id, retry := registry.Add("POST", "/v1/responses", cancel)
	if !registry.RetryNow(id) {
		t.Fatal("立即重试失败")
	}
	select {
	case <-retry:
	case <-time.After(time.Second):
		t.Fatal("未收到重试信号")
	}
	if !registry.Cancel(id) {
		t.Fatal("取消失败")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("上下文未取消")
	}
}

func TestRegistryCarriesClientIdentityIntoHistory(t *testing.T) {
	registry := NewRegistry()
	identity := RequestIdentity{ClientID: "codex-session", TaskID: "codex-thread"}
	id, _ := registry.AddWithIdentity("POST", "/v1/responses", func() {}, identity)
	snapshot := registry.Snapshot(false)
	if len(snapshot.Requests) != 1 || snapshot.Requests[0].ClientID != identity.ClientID || snapshot.Requests[0].TaskID != identity.TaskID {
		t.Fatalf("活动请求缺少客户端关联标识: %+v", snapshot.Requests)
	}
	stored, ok := registry.Identity(id)
	if !ok || stored != identity {
		t.Fatalf("请求关联标识查询异常: %+v ok=%v", stored, ok)
	}
	registry.Remove(id, lifecycle.StateCanceled)
	history := registry.History()
	if len(history) != 1 || history[0].ClientID != identity.ClientID || history[0].TaskID != identity.TaskID {
		t.Fatalf("历史记录缺少客户端关联标识: %+v", history)
	}
}

func TestRetryWaitingOnlySignalsWaitingRequests(t *testing.T) {
	registry := NewRegistry()
	waitingID, waitingRetry := registry.Add("POST", "/v1/responses", func() {})
	requestingID, requestingRetry := registry.Add("POST", "/v1/responses", func() {})
	registry.UpdateMessage(waitingID, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(waitingID, lifecycle.StateWaiting, 1, l10n.Message{}, time.Now().Add(time.Minute))
	registry.UpdateMessage(requestingID, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})

	if count := registry.RetryWaiting(); count != 1 {
		t.Fatalf("排空唤醒数量 = %d，期望 1", count)
	}
	select {
	case <-waitingRetry:
	default:
		t.Fatal("等待请求未收到立即重试信号")
	}
	select {
	case <-requestingRetry:
		t.Fatal("请求中的任务不应收到等待唤醒信号")
	default:
	}
}
