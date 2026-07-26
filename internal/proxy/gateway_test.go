package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/state"
)

func testGateway(t *testing.T, upstreamURL string) (*Gateway, *state.Registry) {
	t.Helper()
	cfg := config.Default()
	cfg.Upstream.BaseURL = upstreamURL
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

func TestGatewayRetriesErrorsAndDeliversOneCompleteStream(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization 未透传")
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"model":"test","stream":true}` {
			t.Errorf("请求体发生变化: %s", body)
		}
		if attempts.Add(1) < 3 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer test-key")
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
	if !strings.Contains(string(body), "keepalive") {
		t.Fatalf("等待期间缺少心跳: %s", body)
	}
	history := registry.History()
	if len(history) != 1 || history[0].State != "successful" || history[0].Attempt != 3 {
		t.Fatalf("请求历史异常: %+v", history)
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
}

func TestGatewayStoresOnlySafeErrorDetailInTimeline(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("X-Request-ID", "req-503")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"internal":"business-payload","error":{"message":"no available account; Bearer private-token","type":"provider_unavailable","code":"no_account"}}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()

	history := registry.History()
	if len(history) != 1 || history[0].LastErrorDetail == nil {
		t.Fatalf("历史缺少安全错误详情: %+v", history)
	}
	detail := history[0].LastErrorDetail
	if detail.Type != "provider_unavailable" || detail.Code != "no_account" || detail.UpstreamRequestID != "req-503" {
		t.Fatalf("安全错误字段异常: %+v", detail)
	}
	encoded, _ := json.Marshal(history)
	for _, secret := range []string{"private-token", "business-payload"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("时间线泄露 %q: %s", secret, encoded)
		}
	}
}

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
	notifier := notify.New(store, logger)
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

func TestReplayBufferSpillsAndDeletes(t *testing.T) {
	directory := t.TempDir()
	buffer := NewReplayBuffer(4, directory)
	if _, err := io.WriteString(buffer, "0123456789"); err != nil {
		t.Fatal(err)
	}
	if buffer.file == nil {
		t.Fatal("应转存临时文件")
	}
	name := buffer.file.Name()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("临时文件权限 = %o，期望 600", info.Mode().Perm())
	}
	var output strings.Builder
	if _, err := buffer.WriteTo(&output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "0123456789" {
		t.Fatalf("缓存内容错误: %s", output.String())
	}
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("临时文件未删除")
	}
}
