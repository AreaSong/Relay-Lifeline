package upstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

func poolForTests() config.UpstreamPoolConfig {
	return config.UpstreamPoolConfig{
		Strategy: "weighted-priority",
		Targets: []config.UpstreamTargetConfig{
			{ID: "a", BaseURL: "http://a", Priority: 0, Weight: 2, MaxActive: 2, IdempotencyDomain: "d1"},
			{ID: "b", BaseURL: "http://b", Priority: 1, Weight: 1, MaxActive: 2, IdempotencyDomain: "d1"},
		},
		Circuit: config.UpstreamCircuitConfig{Enabled: true, MinimumRequests: 2, FailurePercent: 50, OpenDuration: config.Duration{Duration: time.Second}, HalfOpenMax: 1},
	}
}

func TestManagerHonorsPriorityAndLeaseLimit(t *testing.T) {
	m, err := New(poolForTests(), config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Select(context.Background(), SelectionContext{})
	if err != nil || first.Target().ID != "a" {
		t.Fatalf("priority selection failed: lease=%v err=%v", first, err)
	}
	second, err := m.Select(context.Background(), SelectionContext{})
	if err != nil || second.Target().ID != "a" {
		t.Fatalf("same-priority selection failed: lease=%v err=%v", second, err)
	}
	third, err := m.Select(context.Background(), SelectionContext{})
	if err != nil || third.Target().ID != "b" {
		t.Fatalf("expected lower-priority fallback, got %v and lease %v", err, third)
	}
	first.Release()
	second.Release()
	third.Release()
}

func TestManagerDoesNotCrossDomainAfterWriteWithoutIdempotency(t *testing.T) {
	m, err := New(poolForTests(), config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := m.Select(context.Background(), SelectionContext{})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if _, err := m.Select(context.Background(), SelectionContext{PreviousTargetID: "missing", PreviousDomain: "d1", WroteRequest: true}); !errors.Is(err, ErrFailoverNotSafe) {
		t.Fatalf("expected failover safety error, got %v", err)
	}
}

func TestManagerCircuitTransitionsToHalfOpen(t *testing.T) {
	m, err := New(poolForTests(), config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Unix(100, 0) }
	for i := 0; i < 2; i++ {
		m.Observe("a", Observation{Category: "transport"})
	}
	if got := m.Snapshot().Targets[0].State; got != CircuitOpen {
		t.Fatalf("expected open circuit, got %s", got)
	}
	m.now = func() time.Time { return time.Unix(102, 0) }
	lease, err := m.Select(context.Background(), SelectionContext{})
	if err != nil || lease.Target().ID != "a" {
		t.Fatalf("half-open selection failed: lease=%v err=%v", lease, err)
	}
	if got := m.Snapshot().Targets[0].State; got != CircuitHalfOpen {
		t.Fatalf("expected half-open circuit, got %s", got)
	}
	lease.Release()
	m.Observe("a", Observation{Success: true})
	if got := m.Snapshot().Targets[0].State; got != CircuitClosed {
		t.Fatalf("expected closed circuit after recovery, got %s", got)
	}
}

func TestManagerPrimaryOnlyDoesNotFallBackToSecondary(t *testing.T) {
	pool := poolForTests()
	pool.Strategy = "primary-only"
	pool.Targets[0].MaxActive = 1
	m, err := New(pool, config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := m.Select(context.Background(), SelectionContext{})
	if err != nil || lease.Target().ID != "a" {
		t.Fatalf("主目标选择异常: lease=%v err=%v", lease, err)
	}
	defer lease.Release()
	if second, selectErr := m.Select(context.Background(), SelectionContext{}); !errors.Is(selectErr, ErrTargetConcurrencyFull) || second != nil {
		t.Fatalf("primary-only 不应降级至次目标: lease=%v err=%v", second, selectErr)
	}
}

func TestManagerWeightedSelectionUsesConfiguredWeights(t *testing.T) {
	pool := poolForTests()
	pool.Targets[1].Priority = 0
	m, err := New(pool, config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for index := 0; index < 6; index++ {
		lease, selectErr := m.Select(context.Background(), SelectionContext{})
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		counts[lease.Target().ID]++
		lease.Release()
	}
	if counts["a"] != 4 || counts["b"] != 2 {
		t.Fatalf("加权轮询分布异常: %+v", counts)
	}
}

func TestManagerFailoverSafetyMatrix(t *testing.T) {
	pool := poolForTests()
	pool.Targets = []config.UpstreamTargetConfig{
		{ID: "a", BaseURL: "http://a", Priority: 0, Weight: 1, IdempotencyDomain: "d1"},
		{ID: "b", BaseURL: "http://b", Priority: 0, Weight: 1, IdempotencyDomain: "d1"},
		{ID: "c", BaseURL: "http://c", Priority: 0, Weight: 1, IdempotencyDomain: "d2"},
	}
	pool.Circuit.MinimumRequests = 1
	m, err := New(pool, config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}

	lease, err := m.Select(context.Background(), SelectionContext{PreviousTargetID: "a", PreviousDomain: "d1", WroteRequest: true, IdempotencyKey: "stable"})
	if err != nil || lease.Target().ID != "b" {
		t.Fatalf("同域稳定幂等键应切换目标: target=%v err=%v", lease, err)
	}
	lease.Release()

	m.Observe("a", Observation{Category: "transport"})
	m.Observe("b", Observation{Category: "transport"})
	if _, err := m.Select(context.Background(), SelectionContext{PreviousTargetID: "a", PreviousDomain: "d1", WroteRequest: true, IdempotencyKey: "stable"}); !errors.Is(err, ErrFailoverNotSafe) {
		t.Fatalf("跨域故障转移未显式允许时应拒绝: %v", err)
	}
	crossDomain, err := m.Select(context.Background(), SelectionContext{PreviousTargetID: "a", PreviousDomain: "d1", WroteRequest: true, IdempotencyKey: "stable", AllowCrossDomain: true})
	if err != nil || crossDomain.Target().ID != "c" {
		t.Fatalf("显式允许的跨域幂等故障转移失败: lease=%v err=%v", crossDomain, err)
	}
	crossDomain.Release()

	withoutWrite, err := m.Select(context.Background(), SelectionContext{PreviousTargetID: "a", PreviousDomain: "d1", WroteRequest: false})
	if err != nil || withoutWrite.Target().ID != "c" {
		t.Fatalf("未写出请求时应可安全切换: lease=%v err=%v", withoutWrite, err)
	}
	withoutWrite.Release()
}

func TestManagerHalfOpenLeaseCompletesAtomically(t *testing.T) {
	pool := poolForTests()
	pool.Targets = pool.Targets[:1]
	pool.Circuit.MinimumRequests = 1
	m, err := New(pool, config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	m.Observe("a", Observation{Category: "transport"})
	now = now.Add(2 * time.Second)
	probe, err := m.Select(context.Background(), SelectionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Select(context.Background(), SelectionContext{}); !errors.Is(err, ErrNoHealthyTarget) {
		t.Fatalf("HalfOpenMax 应限制并发探测: %v", err)
	}
	probe.Complete(Observation{Success: true, Latency: 25 * time.Millisecond})
	status := m.Snapshot().Targets[0]
	if status.State != CircuitClosed || status.Active != 0 || status.HalfOpenLeases != 0 || status.LastLatencyMs != 25 {
		t.Fatalf("半开探测完成状态不原子: %+v", status)
	}
}

func TestManagerUsesIndependentAndReplaceableTargetTransports(t *testing.T) {
	pool := poolForTests()
	pool.Targets[1].Priority = 0
	m, err := New(pool, config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}
	firstClient := m.targets["a"].client
	secondClient := m.targets["b"].client
	if firstClient == nil || secondClient == nil || firstClient == secondClient {
		t.Fatal("每个目标必须使用独立 HTTP client/Transport")
	}
	if err := m.Apply(pool, config.Default().Upstream); err != nil {
		t.Fatal(err)
	}
	if m.targets["a"].client != firstClient || m.targets["b"].client != secondClient {
		t.Fatal("配置未变化时不应抛弃目标连接池")
	}
	changed := config.Default().Upstream
	changed.ResponseHeaderTimeout.Duration++
	if err := m.Apply(pool, changed); err != nil {
		t.Fatal(err)
	}
	if m.targets["a"].client == firstClient || m.targets["b"].client == secondClient {
		t.Fatal("Transport 配置变化后应发布新 generation")
	}
}

func TestManagerApplyPreservesInFlightLeaseForUnchangedGeneration(t *testing.T) {
	pool := poolForTests()
	m, err := New(pool, config.Default().Upstream)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := m.Select(context.Background(), SelectionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(pool, config.Default().Upstream); err != nil {
		t.Fatal(err)
	}
	lease.Complete(Observation{Success: true})
	status := m.Snapshot().Targets[0]
	if status.Active != 0 || status.SuccessCount != 1 {
		t.Fatalf("旧 lease 完成未提交到复用 generation: %+v", status)
	}
}
