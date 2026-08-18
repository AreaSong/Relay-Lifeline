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
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
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
