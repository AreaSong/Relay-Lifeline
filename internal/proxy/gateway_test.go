package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/governance"
	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/state"
)

func testGateway(t *testing.T, upstreamURL string) (*Gateway, *state.Registry) {
	t.Helper()
	cfg := config.Default()
	cfg.Upstream.BaseURL = upstreamURL
	cfg.Egress.AllowedHosts = append(cfg.Egress.AllowedHosts, "127.0.0.1")
	cfg.Retry.MinInterval.Duration = 15 * time.Millisecond
	cfg.Retry.MaxInterval.Duration = 20 * time.Millisecond
	cfg.Stream.HeartbeatInterval.Duration = 5 * time.Millisecond
	cfg.Queue.RecoverySpacing.Duration = 0
	cfg.Notifications.StalledAfter.Duration = time.Hour
	store := config.NewStore("", cfg)
	registry := state.NewRegistry()
	controller := state.NewController()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := notify.New(store, logger)
	t.Cleanup(notifier.Close)
	return NewGateway(store, registry, controller, notifier, logger), registry
}

func testGovernedGateway(t *testing.T, cfg config.Config, ledger *journal.Store) (*Gateway, *state.Registry, *governance.Manager) {
	t.Helper()
	if cfg.Egress.AllowedHosts == nil {
		cfg.Egress.AllowedHosts = []string{"127.0.0.1"}
	} else {
		cfg.Egress.AllowedHosts = append(cfg.Egress.AllowedHosts, "127.0.0.1")
	}
	store := config.NewStore("", cfg)
	registry := state.NewRegistry()
	controller := state.NewController()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := notify.New(store, logger)
	t.Cleanup(notifier.Close)
	manager, err := governance.NewPersistent(cfg.Governance, ledger)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(store, registry, controller, notifier, logger)
	gateway.SetGovernanceManager(manager)
	return gateway, registry, manager
}

func openGovernanceTestLedger(t *testing.T) *journal.Store {
	t.Helper()
	ledger, err := journal.Open(filepath.Join(t.TempDir(), "usage-ledger.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger
}

func TestGatewayBindsSelectedUpstreamAndRejectsBeforeSecondCall(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFirst)
	var calls atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstreamServer.Close()

	cfg := config.Default()
	cfg.Upstream.BaseURL = upstreamServer.URL
	cfg.Upstreams = config.UpstreamPoolConfig{Strategy: "primary-only", Targets: []config.UpstreamTargetConfig{{ID: "target-a", BaseURL: upstreamServer.URL, Weight: 1, IdempotencyDomain: "domain-a"}}}
	cfg.Governance.Mode = "enforce"
	cfg.Governance.UnknownUsagePolicy = governance.UnknownUsageObserve
	cfg.Governance.Budgets = []config.GovernanceBudgetConfig{{Scope: "upstream", Key: "target-a", MaxConcurrent: 1}}
	cfg.Retry.MaxAttempts = 1
	cfg.Queue.RecoverySpacing.Duration = 0
	gateway, _, manager := testGovernedGateway(t, cfg, nil)
	server := httptest.NewServer(gateway)
	defer server.Close()

	firstDone := make(chan *http.Response, 1)
	go func() {
		response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test"}`))
		if err != nil {
			firstDone <- nil
			return
		}
		firstDone <- response
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first selected upstream call did not start")
	}

	second, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	secondBody, _ := io.ReadAll(second.Body)
	_ = second.Body.Close()
	if second.StatusCode != http.StatusOK || !bytes.Contains(secondBody, []byte("relay_lifeline_error")) {
		t.Fatalf("binding failure should return the governance error envelope: status=%d body=%q", second.StatusCode, secondBody)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("second request reached upstream after target budget rejection: calls=%d", got)
	}
	releaseFirst()
	select {
	case response := <-firstDone:
		if response == nil {
			t.Fatal("first request failed before response")
		}
		_, _ = io.ReadAll(response.Body)
		_ = response.Body.Close()
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("governance reservation leaked after bound request: %+v", snapshot)
	}
}

func TestGatewayRejectsMissingTenantContextBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstreamServer.Close()

	cfg := config.Default()
	cfg.Upstream.BaseURL = upstreamServer.URL
	cfg.Governance.Mode = "enforce"
	cfg.Governance.UnknownUsagePolicy = governance.UnknownUsageObserve
	cfg.Governance.Budgets = []config.GovernanceBudgetConfig{{Scope: "tenant", Key: "tenant-a", TokenLimit: 1}}
	cfg.Retry.MaxAttempts = 1
	gateway, _, manager := testGovernedGateway(t, cfg, nil)
	server := httptest.NewServer(gateway)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Language", "en-US")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusTooManyRequests || !bytes.Contains(body, []byte("tenant_required")) || !bytes.Contains(body, []byte("Governance")) {
		t.Fatalf("missing tenant context should be rejected during admission: status=%d body=%q", response.StatusCode, body)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream was called without tenant context: %d", calls.Load())
	}
	if got := manager.Snapshot().Counters.Rejected["tenant_required"]; got != 1 {
		t.Fatalf("unexpected tenant admission decision count: %+v", manager.Snapshot().Counters.Rejected)
	}
}

func TestGatewayRetryCreatesIndependentGovernanceReservation(t *testing.T) {
	var calls atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstreamServer.Close()

	cfg := config.Default()
	cfg.Upstream.BaseURL = upstreamServer.URL
	cfg.Governance.Mode = "enforce"
	cfg.Governance.UnknownUsagePolicy = governance.UnknownUsageObserve
	cfg.Governance.TokenLimit = 20
	cfg.Governance.TokenReservation = 6
	cfg.Governance.ReservationMinTokens = 6
	cfg.Governance.ReservationMaxTokens = 6
	cfg.Retry.MaxAttempts = 2
	cfg.Retry.MinInterval.Duration = time.Millisecond
	cfg.Retry.MaxInterval.Duration = 2 * time.Millisecond
	cfg.Queue.RecoverySpacing.Duration = 0
	ledger := openGovernanceTestLedger(t)
	gateway, _, manager := testGovernedGateway(t, cfg, ledger)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("retry did not complete: status=%d calls=%d body=%q err=%v", response.StatusCode, calls.Load(), body, readErr)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.Counters.Settlements != 2 {
		t.Fatalf("retry governance accounting incomplete: %+v", snapshot)
	}
	attemptReservations := 0
	for _, entry := range ledger.Entries() {
		if entry.Type == "governance.attempt_reserved" {
			attemptReservations++
		}
	}
	if attemptReservations != 1 {
		t.Fatalf("retry did not persist an independent attempt reservation: entries=%+v", ledger.Entries())
	}
}

func TestGatewayEnforceSettlementAndReleaseFailuresDoNotDeliver(t *testing.T) {
	tests := []struct {
		name        string
		failureType string
		wantEntries int
	}{
		{name: "settlement", failureType: "governance.usage_recorded", wantEntries: 2},
		{name: "release", failureType: "governance.released", wantEntries: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, `{"status":"completed"}`)
			}))
			defer upstreamServer.Close()

			cfg := config.Default()
			cfg.Upstream.BaseURL = upstreamServer.URL
			cfg.Governance.Mode = "enforce"
			cfg.Governance.UnknownUsagePolicy = governance.UnknownUsageObserve
			cfg.Retry.MaxAttempts = 1
			ledger := openGovernanceTestLedger(t)
			ledger.SetHooks(journal.Hooks{Write: func(file *os.File, data []byte) (int, error) {
				var entry journal.Entry
				if err := json.Unmarshal(data, &entry); err == nil && entry.Type == test.failureType {
					return len(data) - 1, nil
				}
				return file.Write(data)
			}})
			gateway, _, manager := testGovernedGateway(t, cfg, ledger)
			server := httptest.NewServer(gateway)
			defer server.Close()

			response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test"}`))
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil || calls.Load() != 1 {
				t.Fatalf("unexpected request result: calls=%d body=%q err=%v", calls.Load(), body, readErr)
			}
			if !bytes.Contains(body, []byte("relay_lifeline_error")) {
				t.Fatalf("failed governance persistence must not deliver upstream success: %q", body)
			}
			if snapshot := manager.Snapshot(); snapshot.Reservations != 1 || snapshot.Counters.PersistenceFailures == 0 {
				t.Fatalf("failed operation should remain visible and degraded: %+v", snapshot)
			}
			if got := len(ledger.Entries()); got != test.wantEntries {
				t.Fatalf("unexpected durable event count after %s failure: got=%d want=%d", test.name, got, test.wantEntries)
			}
		})
	}
}

func TestGatewayRepeatFailsClosedWhenGovernanceLedgerUnavailable(t *testing.T) {
	var calls atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstreamServer.Close()
	cfg := config.Default()
	cfg.Upstream.BaseURL = upstreamServer.URL
	cfg.Governance.Mode = "enforce"
	cfg.Governance.UnknownUsagePolicy = governance.UnknownUsageObserve
	ledger := openGovernanceTestLedger(t)
	ledger.SetHooks(journal.Hooks{Write: func(_ *os.File, data []byte) (int, error) { return len(data) - 1, nil }})
	gateway, _, _ := testGovernedGateway(t, cfg, ledger)
	template := repeat.Template{Method: http.MethodPost, Path: "/v1/responses", Body: []byte(`{"model":"test"}`)}
	result := gateway.ExecuteRepeat(context.Background(), template, "preserve", "repeat-ledger-failure")
	if result.Success || result.ErrorCode != "governance.ledger_unavailable" {
		t.Fatalf("repeat must fail closed on governance admission persistence: %+v", result)
	}
	if calls.Load() != 0 {
		t.Fatalf("repeat called upstream after governance admission failure: %d", calls.Load())
	}
}

func TestGatewayRetriesErrorsAndDeliversOneCompleteStream(t *testing.T) {
	var attempts atomic.Int32
	idempotencyKeyEvents := make(chan string, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization 未透传")
		}
		if request.Header.Get("X-Codex-Thread-ID") != "" || request.Header.Get("X-Codex-Session-ID") != "" {
			t.Errorf("本地 Codex 关联标识泄露到上游")
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"model":"test","stream":true}` {
			t.Errorf("请求体发生变化: %s", body)
		}
		idempotencyKeyEvents <- request.Header.Get("Idempotency-Key")
		if attempts.Add(1) < 3 {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"must-not-leak\"}\n\n")
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	runtimeLogs := runlog.New(func() runlog.Limits { return runlog.Limits{MaxItems: 100, Retention: time.Hour} })
	gateway.SetRunLog(runtimeLogs)
	server := httptest.NewServer(gateway)
	defer server.Close()

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Idempotency-Key", "stable-client-key")
	request.Header.Set("X-Codex-Session-ID", "codex-session")
	request.Header.Set("X-Codex-Thread-ID", "codex-thread")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("重试次数异常: %d", attempts.Load())
	}
	if strings.Count(string(body), "response.completed") != 1 {
		t.Fatalf("响应未单次交付: %s", body)
	}
	if strings.Contains(string(body), "must-not-leak") {
		t.Fatalf("失败尝试的半截流泄露到客户端: %s", body)
	}
	idempotencyKeys := []string{<-idempotencyKeyEvents, <-idempotencyKeyEvents, <-idempotencyKeyEvents}
	if idempotencyKeys[0] != "stable-client-key" || idempotencyKeys[1] != idempotencyKeys[0] || idempotencyKeys[2] != idempotencyKeys[0] {
		t.Fatalf("客户端幂等键未稳定透传: %+v", idempotencyKeys)
	}
	if !strings.Contains(string(body), "keepalive") {
		t.Fatalf("等待期间缺少心跳: %s", body)
	}
	history := registry.History()
	if len(history) != 1 || history[0].State != "successful" || history[0].Attempt != 3 || history[0].ClientID != "codex-session" || history[0].TaskID != "codex-thread" {
		t.Fatalf("请求历史异常: %+v", history)
	}
	if response.Header.Get("X-Relay-Lifeline-Request-ID") != history[0].ID {
		t.Fatalf("响应缺少稳定请求关联 Header: headers=%v history=%+v", response.Header, history[0])
	}
	failed, waiting, completed := 0, 0, 0
	for _, event := range history[0].Events {
		switch event.Type {
		case "attempt_failed":
			failed++
		case "waiting":
			waiting++
		case "completed":
			completed++
		}
	}
	if failed != 2 || waiting != 2 || completed != 1 {
		t.Fatalf("请求时间线不完整: %+v", history[0].Events)
	}
	events := map[string]bool{}
	for _, entry := range runtimeLogs.List(0, "", "", "") {
		events[entry.Event] = true
		if entry.ClientID != "codex-session" || entry.TaskID != "codex-thread" {
			t.Fatalf("运行日志缺少客户端关联标识: %+v", entry)
		}
	}
	for _, event := range []string{"queue.entered", "queue.acquired", "downstream.heartbeat", "retry.scheduled", "retry.resumed", "request.succeeded"} {
		if !events[event] {
			t.Fatalf("结构化运行日志缺少 %s: %+v", event, events)
		}
	}
}

func TestGatewayExtractsInboundTraceAndInjectsCurrentClientSpan(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})

	outboundTraceparent := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		outboundTraceparent <- request.Header.Get("traceparent")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstream.Close()
	gateway, _ := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	const incoming = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"stream":false}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("traceparent", incoming)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()

	spans := recorder.Ended()
	serverSpan := findSpan(t, spans, "relay.proxy.request")
	attemptSpan := findSpan(t, spans, "relay.proxy.attempt")
	clientSpan := findSpan(t, spans, "relay.upstream.http")
	if serverSpan.SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || serverSpan.Parent().SpanID().String() != "00f067aa0ba902b7" || !serverSpan.Parent().IsRemote() {
		t.Fatalf("入站 W3C 父级未保留: span=%v parent=%v", serverSpan.SpanContext(), serverSpan.Parent())
	}
	if attemptSpan.Parent().SpanID() != serverSpan.SpanContext().SpanID() || clientSpan.Parent().SpanID() != attemptSpan.SpanContext().SpanID() {
		t.Fatalf("Span 父子关系异常: server=%v attempt.parent=%v client.parent=%v", serverSpan.SpanContext(), attemptSpan.Parent(), clientSpan.Parent())
	}
	propagated := <-outboundTraceparent
	if propagated == incoming || !strings.Contains(propagated, serverSpan.SpanContext().TraceID().String()) || !strings.Contains(propagated, clientSpan.SpanContext().SpanID().String()) {
		t.Fatalf("出站 traceparent 未由当前 client Span 重建: %q", propagated)
	}
}

func findSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	var names []string
	for _, span := range spans {
		names = append(names, span.Name())
	}
	t.Fatalf("缺少 Span %q: %v", name, names)
	return nil
}

func TestGatewayNeverForgetsAnEarlierWrittenAttemptDuringFailover(t *testing.T) {
	cfg := config.Default()
	cfg.Upstream.BaseURL = "http://a.invalid"
	cfg.Upstreams = config.UpstreamPoolConfig{
		Strategy: "weighted-priority",
		Targets: []config.UpstreamTargetConfig{
			{ID: "a", BaseURL: "http://a.invalid", Weight: 1, IdempotencyDomain: "shared"},
			{ID: "b", BaseURL: "http://b.invalid", Weight: 1, IdempotencyDomain: "shared"},
		},
		Health:  config.UpstreamHealthConfig{Mode: "passive"},
		Circuit: config.UpstreamCircuitConfig{Enabled: true, MinimumRequests: 5, FailurePercent: 100, OpenDuration: config.Duration{Duration: time.Minute}, HalfOpenMax: 1},
	}
	cfg.Retry.MaxAttempts = 3
	cfg.Lifecycle.AllowUncertainRetry = true
	cfg.Retry.MinInterval.Duration = time.Millisecond
	cfg.Retry.MaxInterval.Duration = time.Millisecond
	cfg.Queue.RecoverySpacing.Duration = 0
	cfg.Notifications.StalledAfter.Duration = time.Hour
	store := config.NewStore("", cfg)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := notify.New(store, logger)
	defer notifier.Close()
	gateway := NewGateway(store, state.NewRegistry(), state.NewController(), notifier, logger)

	var attempts atomic.Int32
	hosts := make(chan string, 3)
	gateway.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		hosts <- request.URL.Host
		if attempt == 1 {
			if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.WroteRequest != nil {
				trace.WroteRequest(httptrace.WroteRequestInfo{})
			}
			return nil, errors.New("response headers lost after write")
		}
		if attempt == 2 {
			return nil, errors.New("connect failed before write")
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"status":"completed"}`)), Request: request,
		}, nil
	})}

	server := httptest.NewServer(gateway)
	defer server.Close()
	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if attempts.Load() != 3 {
		t.Fatalf("尝试次数异常: %d", attempts.Load())
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if host := <-hosts; host != "a.invalid" {
			t.Fatalf("第 %d 次尝试错误切换到 %s；早期已写出状态必须累积", attempt, host)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestGatewayAppliesHotUpstreamAndGovernanceConfiguration(t *testing.T) {
	cfg := config.Default()
	store := config.NewStore("", cfg)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := notify.New(store, logger)
	t.Cleanup(notifier.Close)
	gateway := NewGateway(store, state.NewRegistry(), state.NewController(), notifier, logger)

	next := cfg
	next.Upstreams = config.UpstreamPoolConfig{
		Strategy: "weighted-priority",
		Targets:  []config.UpstreamTargetConfig{{ID: "hot-target", BaseURL: "https://relay.example.test", Weight: 1, IdempotencyDomain: "shared"}},
		Health:   cfg.Upstreams.Health, Circuit: cfg.Upstreams.Circuit,
	}
	next.Governance.Mode = "enforce"
	next.Governance.MaxConcurrent = 3
	if _, err := store.UpdateWithResult(next, false); err != nil {
		t.Fatal(err)
	}
	upstreamStatus := gateway.UpstreamStatus()
	if upstreamStatus.Strategy != "weighted-priority" || len(upstreamStatus.Targets) != 1 || upstreamStatus.Targets[0].Target.ID != "hot-target" {
		t.Fatalf("Gateway 未同步上游热配置: %+v", upstreamStatus)
	}
	if status := gateway.GovernanceStatus(); status.Mode != "enforce" {
		t.Fatalf("Gateway 未同步治理热配置: %+v", status)
	}
}

func TestGatewayRejectsUnsupportedMediaWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(writer, "binary")
	}))
	defer upstream.Close()
	gateway, _ := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/audio/speech", "application/json", strings.NewReader(`{"input":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || attempts.Load() != 1 || !bytes.Contains(body, []byte("relay_lifeline_error")) {
		t.Fatalf("不支持的媒体响应处理异常: attempts=%d body=%q err=%v", attempts.Load(), body, readErr)
	}
}

func TestNonStreamingHeartbeatKeepsFinalJSONValid(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"response-ok","status":"completed"}`)
	}))
	defer upstream.Close()
	gateway, _ := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !json.Valid(body) || !bytes.Contains(body, []byte(`"id":"response-ok"`)) || !bytes.HasPrefix(body, []byte("\n")) {
		t.Fatalf("非流式心跳破坏最终 JSON: %q err=%v", body, err)
	}
}

func TestGatewayRetriesEveryHTTPErrorClass(t *testing.T) {
	statuses := []int{400, 401, 408, 409, 425, 429, 500, 502, 503}
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempt := int(attempts.Add(1))
		if attempt <= len(statuses) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(statuses[attempt-1])
			_, _ = io.WriteString(writer, `{"error":{"message":"retry"}}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"recovered","status":"completed"}`)
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || !bytes.Contains(body, []byte(`"id":"recovered"`)) {
		t.Fatalf("全错误重试后未交付成功响应: body=%q err=%v", body, readErr)
	}
	if attempts.Load() != int32(len(statuses)+1) || registry.Snapshot(false).Successful != 1 {
		t.Fatalf("HTTP 错误覆盖异常: attempts=%d status=%+v", attempts.Load(), registry.Snapshot(false))
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestGatewayRecoversFromDNSConnectionAndTimeoutErrors(t *testing.T) {
	var attempts atomic.Int32
	gateway, registry := testGateway(t, "http://upstream.invalid")
	gateway.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		switch attempts.Add(1) {
		case 1:
			return nil, &net.DNSError{Err: "no such host", Name: "upstream.invalid"}
		case 2:
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		case 3:
			return nil, context.DeadlineExceeded
		default:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"status":"completed"}`)),
				Request:    request,
			}, nil
		}
	})}
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || attempts.Load() != 4 || registry.Snapshot(false).Successful != 1 {
		t.Fatalf("传输错误恢复异常: attempts=%d status=%+v err=%v", attempts.Load(), registry.Snapshot(false), readErr)
	}
}

func TestGatewayMarksWrittenRequestWithoutHeadersAsUncertain(t *testing.T) {
	var attempts atomic.Int32
	gateway, registry := testGateway(t, "http://upstream.invalid")
	cfg := gateway.store.Get()
	cfg.Lifecycle.AllowUncertainRetry = true
	gateway.store = config.NewStore("", cfg)
	gateway.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			trace := httptrace.ContextClientTrace(request.Context())
			if trace == nil || trace.WroteRequest == nil {
				t.Fatal("请求缺少 WroteRequest 跟踪")
			}
			trace.WroteRequest(httptrace.WroteRequestInfo{})
			return nil, errors.New("connection lost before response headers")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"completed"}`)),
			Request:    request,
		}, nil
	})}
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || attempts.Load() != 2 {
		t.Fatalf("不确定请求恢复异常: attempts=%d err=%v", attempts.Load(), readErr)
	}
	history := registry.History()
	if len(history) != 1 {
		t.Fatalf("请求历史异常: %+v", history)
	}
	foundUncertain, foundFailurePhase := false, false
	for _, event := range history[0].Events {
		if event.Type == "uncertain" && event.AttemptPhase == "response_headers" {
			foundUncertain = true
		}
		if event.Type == "attempt_failed" && event.AttemptPhase == "response_headers" {
			foundFailurePhase = true
		}
	}
	if !foundUncertain || !foundFailurePhase {
		t.Fatalf("时间线缺少不确定交付风险或失败阶段: %+v", history[0].Events)
	}
}

func TestGatewayBlocksUncertainRetryUntilExplicitConfirmation(t *testing.T) {
	var attempts atomic.Int32
	gateway, registry := testGateway(t, "http://upstream.invalid")
	gateway.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			trace := httptrace.ContextClientTrace(request.Context())
			trace.WroteRequest(httptrace.WroteRequestInfo{})
			return nil, errors.New("connection lost after write")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"completed"}`)), Request: request}, nil
	})}
	server := httptest.NewServer(gateway)
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			err = response.Body.Close()
		}
		done <- err
	}()

	var id string
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := registry.Snapshot(false)
		if len(snapshot.Requests) == 1 && snapshot.Requests[0].State == "uncertain" {
			id = snapshot.Requests[0].ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" || attempts.Load() != 1 {
		t.Fatalf("uncertain request should be blocked after one attempt: id=%q attempts=%d", id, attempts.Load())
	}
	time.Sleep(40 * time.Millisecond)
	if attempts.Load() != 1 {
		t.Fatalf("uncertain request retried without confirmation: %d", attempts.Load())
	}
	if result := registry.ResolveUncertain(id, state.UncertainRequestCompensation, "测试确认允许补偿重试"); result.Outcome != state.RequestActionAccepted {
		t.Fatalf("explicit uncertain compensation confirmation failed: %+v", result)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("confirmed uncertain retry did not resume")
	}
	if attempts.Load() != 2 {
		t.Fatalf("confirmed retry attempts=%d", attempts.Load())
	}
}

func TestGatewayResolutionDoesNotRecordCancellationAsFinalOutcome(t *testing.T) {
	gateway, registry := testGateway(t, "http://upstream.invalid")
	gateway.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(request.Context())
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return nil, errors.New("connection lost after write")
	})}
	server := httptest.NewServer(gateway)
	defer server.Close()
	done := make(chan struct{})
	go func() {
		response, _ := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		close(done)
	}()
	var id string
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := registry.Snapshot(false)
		if len(snapshot.Requests) == 1 && snapshot.Requests[0].State == lifecycle.StateUncertain {
			id = snapshot.Requests[0].ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("请求未进入不确定状态")
	}
	if result := registry.ResolveUncertain(id, state.UncertainConfirmSuccess, "已核验上游成功"); result.Outcome != state.RequestActionAccepted {
		t.Fatalf("确认成功处置失败: %+v", result)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("人工终结未唤醒网关")
	}
	history := registry.History()
	if len(history) != 1 || history[0].State != string(lifecycle.StateConfirmedSuccess) {
		t.Fatalf("人工终结终态异常: %+v", history)
	}
	for _, event := range history[0].Events {
		if event.Type == "canceled" || event.Type == "expired" {
			t.Fatalf("人工确认成功不应写入取消/过期事件: %+v", history[0].Events)
		}
	}
}

func TestLifecycleIdempotencyConfiguration(t *testing.T) {
	header := http.Header{"Idempotency-Key": []string{"client-key"}}
	prepareIdempotencyKey(header, config.LifecycleConfig{GenerateIdempotencyKey: true})
	generated := header.Get("Idempotency-Key")
	if !strings.HasPrefix(generated, "lifeline-") || len(generated) != len("lifeline-")+32 {
		t.Fatalf("未生成稳定幂等键: %q", generated)
	}
	prepareIdempotencyKey(header, config.LifecycleConfig{PreserveIdempotencyKey: true, GenerateIdempotencyKey: true})
	if header.Get("Idempotency-Key") != generated {
		t.Fatal("同一请求重试链的幂等键发生变化")
	}
	prepareIdempotencyKey(header, config.LifecycleConfig{})
	if header.Get("Idempotency-Key") != "" {
		t.Fatal("关闭保留与生成后仍透传幂等键")
	}
}

func TestGatewayRepeatExecutionPreservesOrRegeneratesIdempotencyKey(t *testing.T) {
	keys := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		keys <- request.Header.Get("Idempotency-Key")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstream.Close()
	gateway, _ := testGateway(t, upstream.URL)
	template := repeat.Template{
		Method: http.MethodPost, Path: "/v1/responses", Headers: http.Header{"Idempotency-Key": []string{"client-key"}},
		Body: []byte(`{"stream":false}`),
	}
	preserved := gateway.ExecuteRepeat(context.Background(), template, "preserve", "preserved")
	regenerated := gateway.ExecuteRepeat(context.Background(), template, "regenerate", "regenerated")
	if !preserved.Success || !regenerated.Success {
		t.Fatalf("持续任务执行结果异常: preserved=%+v regenerated=%+v", preserved, regenerated)
	}
	preservedKey, regeneratedKey := <-keys, <-keys
	if preservedKey != "client-key" {
		t.Fatalf("保留模式修改了客户端幂等键: %q", preservedKey)
	}
	if regeneratedKey == "client-key" || !strings.HasPrefix(regeneratedKey, "lifeline-") {
		t.Fatalf("重新生成模式未生成新幂等键: %q", regeneratedKey)
	}
}

func TestGatewayRetryPolicyOverridesAttemptLimitAndExpires(t *testing.T) {
	var attempts atomic.Int32
	firstAttempt := make(chan struct{})
	releaseFirst := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			close(firstAttempt)
			<-releaseFirst
		}
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	cfg := gateway.store.Get()
	cfg.Retry.MaxAttempts = 1
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
	select {
	case <-firstAttempt:
	case <-time.After(time.Second):
		t.Fatal("首轮上游请求未开始")
	}
	snapshot := registry.Snapshot(false)
	if len(snapshot.Requests) != 1 || !registry.SetRetryPolicy(snapshot.Requests[0].ID, 40*time.Millisecond, 5*time.Millisecond) {
		t.Fatalf("无法给活动请求设置限时恢复: %+v", snapshot.Requests)
	}
	close(releaseFirst)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("限时恢复请求未在截止后结束")
	}
	if attempts.Load() <= 1 {
		t.Fatalf("限时恢复未覆盖全局最大尝试次数: %d", attempts.Load())
	}
	history := registry.History()
	if len(history) != 1 || history[0].State != string(lifecycle.StateExpired) {
		t.Fatalf("限时恢复截止终态异常: %+v", history)
	}
	foundExpired := false
	for _, event := range history[0].Events {
		foundExpired = foundExpired || event.Type == "retry_window_expired"
	}
	if !foundExpired {
		t.Fatalf("时间线缺少限时恢复到期事件: %+v", history[0].Events)
	}
}

func TestGatewayExpiresAtLifecycleDeadline(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	cfg := gateway.store.Get()
	cfg.Lifecycle.MaxRequestDuration.Duration = 30 * time.Millisecond
	gateway.store = config.NewStore("", cfg)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()
	history := registry.History()
	if len(history) != 1 || history[0].State != "expired" {
		t.Fatalf("生命周期超时终态异常: %+v", history)
	}
}

func TestGatewayRejectsNewRequestInResourceProtectionMode(t *testing.T) {
	gateway, registry := testGateway(t, "http://upstream.invalid")
	gateway.resourceCheck = func(config.Config) error { return errors.New("disk pressure") }
	server := httptest.NewServer(gateway)
	defer server.Close()
	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || registry.Snapshot(false).TotalRequests != 0 {
		t.Fatalf("资源保护未在接纳前拒绝: status=%d snapshot=%+v", response.StatusCode, registry.Snapshot(false))
	}
}

func TestGatewayRetriesResponseHeaderTimeout(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			time.Sleep(60 * time.Millisecond)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	cfg := gateway.store.Get()
	cfg.Upstream.ResponseHeaderTimeout.Duration = 20 * time.Millisecond
	cfg.Lifecycle.AllowUncertainRetry = true
	gateway.client = newHTTPClient(cfg)
	if err := gateway.store.Update(cfg, false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || attempts.Load() != 2 || registry.Snapshot(false).Successful != 1 {
		t.Fatalf("响应头超时恢复异常: attempts=%d status=%+v err=%v", attempts.Load(), registry.Snapshot(false), readErr)
	}
}

func TestGatewayRejectsNewRequestsWhileDraining(t *testing.T) {
	gateway, registry := testGateway(t, "http://127.0.0.1:1")
	gateway.Controller().Drain()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test"}`))
	gateway.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") == "" || registry.Snapshot(false).TotalRequests != 0 {
		t.Fatalf("排空接纳保护异常: status=%d retry=%q snapshot=%+v", recorder.Code, recorder.Header().Get("Retry-After"), registry.Snapshot(false))
	}
}
