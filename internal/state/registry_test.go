package state

import (
	"context"
	"testing"
	"time"
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
