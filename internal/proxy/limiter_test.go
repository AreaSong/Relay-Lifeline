package proxy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLimiterAdmitsWaitersInFIFOOrder(t *testing.T) {
	limiter := &Limiter{}
	limits := func() (int, int) { return 1, 3 }
	if err := limiter.Acquire(context.Background(), limits); err != nil {
		t.Fatal(err)
	}
	order := make(chan int, 2)
	for id := 1; id <= 2; id++ {
		id := id
		go func() {
			if err := limiter.Acquire(context.Background(), limits); err != nil {
				return
			}
			order <- id
			limiter.Release()
		}()
		waitForLimiterStats(t, limiter, 1, id)
	}
	limiter.Release()
	first := <-order
	second := <-order
	if first != 1 || second != 2 {
		t.Fatalf("队列未按 FIFO 放行: %d, %d", first, second)
	}
}

func TestLimiterRemovesCanceledWaiter(t *testing.T) {
	limiter := &Limiter{}
	limits := func() (int, int) { return 1, 2 }
	if err := limiter.Acquire(context.Background(), limits); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- limiter.Acquire(ctx, limits) }()
	waitForLimiterStats(t, limiter, 1, 1)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消结果异常: %v", err)
	}
	waitForLimiterStats(t, limiter, 1, 0)
	limiter.Release()
}

func waitForLimiterStats(t *testing.T, limiter *Limiter, active, waiting int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		actualActive, actualWaiting := limiter.Stats()
		if actualActive == active && actualWaiting == waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
	actualActive, actualWaiting := limiter.Stats()
	t.Fatalf("限流器状态超时: active=%d waiting=%d", actualActive, actualWaiting)
}
