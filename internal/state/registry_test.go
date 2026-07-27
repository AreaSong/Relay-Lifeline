package state

import (
	"context"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
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

func TestRetryWaitingOnlySignalsWaitingRequests(t *testing.T) {
	registry := NewRegistry()
	waitingID, waitingRetry := registry.Add("POST", "/v1/responses", func() {})
	requestingID, requestingRetry := registry.Add("POST", "/v1/responses", func() {})
	registry.UpdateMessage(waitingID, "waiting", 1, l10n.Message{}, time.Now().Add(time.Minute))
	registry.UpdateMessage(requestingID, "requesting", 1, l10n.Message{}, time.Time{})

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
