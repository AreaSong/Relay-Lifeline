package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/state"
)

func TestGatewayWarnsOnAuthErrorsButContinuesRetrying(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) <= 3 {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	notificationEvents := make(chan string, 4)
	webhook := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var event struct {
			Type string `json:"type"`
		}
		_ = json.NewDecoder(request.Body).Decode(&event)
		notificationEvents <- event.Type
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()
	cfg := config.Default()
	cfg.Upstream.BaseURL = upstream.URL
	cfg.Retry.MinInterval.Duration = time.Millisecond
	cfg.Retry.MaxInterval.Duration = 2 * time.Millisecond
	cfg.Stream.HeartbeatInterval.Duration = time.Millisecond
	cfg.Queue.RecoverySpacing.Duration = 0
	cfg.Risk.AuthErrorAttempts = 3
	cfg.Notifications.WebhookURL = webhook.URL
	store := config.NewStore("", cfg)
	registry := state.NewRegistry()
	controller := state.NewController()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := notify.NewWithSigning(store, logger, notify.SigningConfig{
		KeyID: "test-key", Secret: strings.Repeat("s", 32),
	})
	defer notifier.Close()
	riskManager := risk.New()
	gateway := NewGateway(store, registry, controller, notifier, logger, riskManager)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if attempts.Load() != 4 || registry.Snapshot(false).Successful != 1 {
		t.Fatalf("鉴权错误后未继续到成功: attempts=%d status=%+v", attempts.Load(), registry.Snapshot(false))
	}
	alerts := riskManager.Recent(10)
	if len(alerts) != 1 || alerts[0].Type != "auth_errors" || alerts[0].ResolvedAt == nil {
		t.Fatalf("鉴权风险提醒异常: %+v", alerts)
	}
	history := registry.History()
	foundWarning := false
	for _, event := range history[0].Events {
		foundWarning = foundWarning || event.Type == "risk_warning"
	}
	if !foundWarning {
		t.Fatalf("时间线缺少风险提醒: %+v", history[0].Events)
	}
	received := map[string]bool{}
	for len(received) < 2 {
		select {
		case eventType := <-notificationEvents:
			received[eventType] = true
		case <-time.After(time.Second):
			t.Fatalf("未收到风险与恢复通知: %+v", received)
		}
	}
	if !received["auth_errors"] || !received["recovered"] {
		t.Fatalf("通知类型异常: %+v", received)
	}
}

func TestGatewayStopsWhenClientCancels(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"stream":true}`))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	time.Sleep(60 * time.Millisecond)
	count := attempts.Load()
	time.Sleep(60 * time.Millisecond)
	if attempts.Load() != count {
		t.Fatalf("取消后仍在重试: %d -> %d", count, attempts.Load())
	}
	if registry.Snapshot(false).Active != 0 {
		t.Fatal("取消后请求未清理")
	}
}

func TestGatewayAdminCancelStopsActiveUpstreamRequest(t *testing.T) {
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(upstreamStarted)
		select {
		case <-request.Context().Done():
		case <-releaseUpstream:
		}
	}))
	defer upstream.Close()
	defer close(releaseUpstream)
	gateway, registry := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"stream":true}`))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		response.Body.Close()
		t.Fatal("上游请求未开始")
	}
	snapshot := registry.Snapshot(false)
	if len(snapshot.Requests) != 1 || !registry.Cancel(snapshot.Requests[0].ID) {
		response.Body.Close()
		t.Fatalf("无法取消活动请求: %+v", snapshot.Requests)
	}
	response.Body.Close()
	deadline := time.Now().Add(time.Second)
	for registry.Snapshot(false).Active != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if registry.Snapshot(false).Active != 0 {
		t.Fatal("上游取消后活动请求未清理")
	}
}

func TestGatewayRecordsMonitoringLifecycleAndBoundedErrorCategory(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstream.Close()
	gateway, _ := testGateway(t, upstream.URL)
	metricsStore := monitoring.New()
	gateway.SetMonitoring(metricsStore)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()

	metrics := metricsStore.Metrics(15 * time.Minute)
	if metrics.Totals.Requests != 1 || metrics.Totals.Successful != 1 || metrics.Totals.Attempts != 2 || metrics.Totals.FailedAttempts != 1 || metrics.Totals.Recovered != 1 {
		t.Fatalf("网关监控计数异常: %+v", metrics.Totals)
	}
	if metrics.Load.Active != 0 || metrics.Load.Queued != 0 || metrics.Load.Waiting != 0 || metrics.Load.Requesting != 0 {
		t.Fatalf("请求结束后负载未归零: %+v", metrics.Load)
	}
	errors := metricsStore.Errors()
	serverFailures := false
	for _, category := range errors.Categories {
		if category.Code == "server" && category.Count == 1 {
			serverFailures = true
		}
	}
	if !serverFailures {
		t.Fatalf("HTTP 503 未归入 server: %+v", errors.Categories)
	}
	events := metricsStore.Events(0, 10)
	wantCodes := []string{"request.received", "upstream.attempt_started", "upstream.failure", "upstream.attempt_started", "upstream.recovered", "request.succeeded"}
	if len(events.Events) != len(wantCodes) {
		t.Fatalf("网关运行事件数量异常: %+v", events.Events)
	}
	for index, code := range wantCodes {
		if events.Events[index].Code != code {
			t.Fatalf("网关运行事件[%d] = %q，期望 %q", index, events.Events[index].Code, code)
		}
	}
}

func TestAttemptCategoryIsLimitedAndSpecific(t *testing.T) {
	tests := []struct {
		name   string
		result attemptResult
		want   string
	}{
		{name: "传输", result: attemptResult{err: errors.New("private transport detail")}, want: "transport"},
		{name: "协议", result: attemptResult{response: &http.Response{StatusCode: http.StatusOK}}, want: "protocol"},
		{name: "鉴权", result: attemptResult{response: &http.Response{StatusCode: http.StatusUnauthorized}}, want: "auth"},
		{name: "限流", result: attemptResult{response: &http.Response{StatusCode: http.StatusTooManyRequests}}, want: "rate_limit"},
		{name: "客户端", result: attemptResult{response: &http.Response{StatusCode: http.StatusBadRequest}}, want: "client"},
		{name: "服务端", result: attemptResult{response: &http.Response{StatusCode: http.StatusBadGateway}}, want: "server"},
		{name: "其他 HTTP", result: attemptResult{response: &http.Response{StatusCode: http.StatusTemporaryRedirect}}, want: "http"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := attemptCategory(test.result); got != test.want {
				t.Fatalf("attemptCategory() = %q，期望 %q", got, test.want)
			}
		})
	}
}
