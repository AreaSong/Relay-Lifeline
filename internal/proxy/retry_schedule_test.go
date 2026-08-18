package proxy

import (
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/state"
)

func TestRetryPolicyDelayModesAndRetryAfter(t *testing.T) {
	gateway := &Gateway{random: rand.New(rand.NewSource(7))}
	cfg := config.Default()
	response := &http.Response{Header: http.Header{"Retry-After": []string{"30"}}}
	fixed := state.RetryPolicy{Schedule: state.RetrySchedule{Mode: state.RetryScheduleFixed, Interval: 5 * time.Second}, HonorRetryAfter: true}
	if delay := gateway.policyDelay(fixed, cfg, response, 1); delay != 30*time.Second {
		t.Fatalf("Retry-After 未覆盖固定间隔: %s", delay)
	}
	immediate := state.RetryPolicy{Schedule: state.RetrySchedule{Mode: state.RetryScheduleImmediate}}
	if delay := gateway.policyDelay(immediate, cfg, nil, 1); delay != 0 {
		t.Fatalf("立即模式产生额外等待: %s", delay)
	}
	randomPolicy := state.RetryPolicy{Schedule: state.RetrySchedule{Mode: state.RetryScheduleRandom, Minimum: 5 * time.Second, Maximum: 15 * time.Second}}
	for index := 0; index < 50; index++ {
		delay := gateway.policyDelay(randomPolicy, cfg, nil, 1)
		if delay < 5*time.Second || delay > 15*time.Second {
			t.Fatalf("随机间隔越界: %s", delay)
		}
	}
}

func TestExponentialDelayCapsWithoutOverflow(t *testing.T) {
	if delay := exponentialDelay(5*time.Second, time.Minute, 0); delay != 5*time.Second {
		t.Fatalf("指数初始值异常: %s", delay)
	}
	if delay := exponentialDelay(5*time.Second, time.Minute, 3); delay != 40*time.Second {
		t.Fatalf("指数增长异常: %s", delay)
	}
	if delay := exponentialDelay(5*time.Second, time.Minute, 100); delay != time.Minute {
		t.Fatalf("指数上限异常: %s", delay)
	}
}

func TestWaitingPolicyUpdateReschedulesImmediatelyAndHonorsAttemptLimit(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	cfg := gateway.store.Get()
	cfg.Retry.MinInterval = config.Duration{Duration: 10 * time.Second}
	cfg.Retry.MaxInterval = config.Duration{Duration: 10 * time.Second}
	gateway.store = config.NewStore("", cfg)
	server := httptest.NewServer(gateway)
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
		if err == nil {
			_, _ = io.ReadAll(response.Body)
			err = response.Body.Close()
		}
		done <- err
	}()

	var id string
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := registry.Snapshot(false)
		if len(snapshot.Requests) == 1 && snapshot.Requests[0].State == "waiting" {
			id = snapshot.Requests[0].ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("请求未进入等待状态")
	}
	result := registry.ApplyRetryPolicy(id, state.RetryPolicySpec{
		Duration: time.Minute, Schedule: state.RetrySchedule{Mode: state.RetryScheduleImmediate},
		MaxAdditionalAttempts: 1,
	}, true)
	if result.Outcome != state.RequestActionAccepted {
		t.Fatalf("立即策略设置失败: %+v", result)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待请求没有被新策略立即重排")
	}
	if attempts.Load() != 2 {
		t.Fatalf("新增尝试限制未生效: %d", attempts.Load())
	}
	history := registry.History()
	foundRescheduled, foundExhausted := false, false
	for _, event := range history[0].Events {
		foundRescheduled = foundRescheduled || event.Type == "retry_rescheduled"
		foundExhausted = foundExhausted || event.Type == "retry_attempts_exhausted"
	}
	if !foundRescheduled || !foundExhausted {
		t.Fatalf("时间线缺少重排或次数耗尽事件: %+v", history[0].Events)
	}
}
