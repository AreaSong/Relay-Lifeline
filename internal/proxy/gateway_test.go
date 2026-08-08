package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/risk"
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
	events := map[string]bool{}
	for _, entry := range runtimeLogs.List(0, "", "", "") {
		events[entry.Event] = true
	}
	for _, event := range []string{"queue.entered", "queue.acquired", "downstream.heartbeat", "retry.scheduled", "retry.resumed", "request.succeeded"} {
		if !events[event] {
			t.Fatalf("结构化运行日志缺少 %s: %+v", event, events)
		}
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

func TestGatewayRetriesStalledResponseBodyWithoutLeakingPartialStream(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if attempts.Add(1) == 1 {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"must-not-leak\"}\n\n")
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	cfg := gateway.store.Get()
	cfg.Upstream.ResponseBodyIdleTimeout.Duration = 20 * time.Millisecond
	if err := gateway.store.Update(cfg, false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || attempts.Load() != 2 || !bytes.Contains(body, []byte("response.completed")) || bytes.Contains(body, []byte("must-not-leak")) {
		t.Fatalf("正文空闲超时恢复异常: attempts=%d body=%q err=%v", attempts.Load(), body, readErr)
	}
	history := registry.History()
	if len(history) != 1 || history[0].Attempt != 2 {
		t.Fatalf("正文空闲超时时间线异常: %+v", history)
	}
}

func TestGatewayConcurrentRecoveryRespectsActiveLimit(t *testing.T) {
	const requests = 64
	var active atomic.Int32
	var peak atomic.Int32
	counts := make(map[string]int)
	var countsMu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for observed := peak.Load(); current > observed && !peak.CompareAndSwap(observed, current); observed = peak.Load() {
		}
		id := request.Header.Get("X-Test-Request")
		countsMu.Lock()
		counts[id]++
		attempt := counts[id]
		countsMu.Unlock()
		time.Sleep(2 * time.Millisecond)
		writer.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"error":{"message":"temporary"}}`)
			return
		}
		_, _ = io.WriteString(writer, `{"status":"completed"}`)
	}))
	defer upstream.Close()
	gateway, registry := testGateway(t, upstream.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()

	errors := make(chan error, requests)
	var group sync.WaitGroup
	for index := 0; index < requests; index++ {
		group.Add(1)
		go func(id int) {
			defer group.Done()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"stream":false}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Test-Request", fmt.Sprintf("request-%d", id))
			response, err := http.DefaultClient.Do(request)
			if err == nil {
				_, err = io.ReadAll(response.Body)
				response.Body.Close()
			}
			errors <- err
		}(index)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	status := registry.Snapshot(false)
	if peak.Load() > 8 || status.Active != 0 || status.Successful != requests {
		t.Fatalf("并发恢复状态异常: peak=%d status=%+v", peak.Load(), status)
	}
}

func TestGatewayCapturesRequestEveryAttemptAndFinalResponse(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"error":{"message":"first failure"}}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	gateway, _ := testGateway(t, upstream.URL)
	cfg := config.Default().Capture
	cfg.StorageDir = t.TempDir()
	cfg.MaxBodySize = 1 << 20
	cfg.MaxTotalSize = 8 << 20
	cfg.MinimumFreeDisk = 64 << 20
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x23}, 32))
	manager := capture.New(func() config.CaptureConfig { return cfg }, key)
	if err := manager.Activate(1, time.Minute); err != nil {
		t.Fatal(err)
	}
	gateway.SetCaptureManager(manager)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()
	records := manager.List()
	if len(records) != 1 || records[0].State != "successful" || len(records[0].Attempts) != 2 || records[0].Final == nil {
		t.Fatalf("网关捕获不完整: %+v", records)
	}
	preview, err := manager.Preview(records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Parts) != 4 || preview.Parts[1].StatusCode != http.StatusServiceUnavailable || preview.Parts[2].StatusCode != http.StatusOK || preview.Parts[3].Name != "final" {
		t.Fatalf("请求、尝试或最终响应缺失: %+v", preview.Parts)
	}
}

func TestGatewayMarksLastFailedAttemptAsFinalCapture(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(writer, `{"error":{"message":"still unavailable"}}`)
	}))
	defer upstream.Close()
	gateway, _ := testGateway(t, upstream.URL)
	proxyConfig := gateway.store.Get()
	proxyConfig.Retry.MaxAttempts = 1
	if err := gateway.store.Update(proxyConfig, false); err != nil {
		t.Fatal(err)
	}
	captureConfig := config.Default().Capture
	captureConfig.StorageDir = t.TempDir()
	captureConfig.MaxBodySize = 1 << 20
	captureConfig.MaxTotalSize = 8 << 20
	captureConfig.MinimumFreeDisk = 64 << 20
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
	manager := capture.New(func() config.CaptureConfig { return captureConfig }, key)
	if err := manager.Activate(1, time.Minute); err != nil {
		t.Fatal(err)
	}
	gateway.SetCaptureManager(manager)
	server := httptest.NewServer(gateway)
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()
	records := manager.List()
	if len(records) != 1 || records[0].State != "failed" || len(records[0].Attempts) != 1 || records[0].Final == nil {
		t.Fatalf("最终失败响应未完整捕获: %+v", records)
	}
	preview, err := manager.Preview(records[0].ID)
	if err != nil || len(preview.Parts) != 3 || preview.Parts[2].Name != "final" || !strings.Contains(preview.Parts[2].Body, "still unavailable") {
		t.Fatalf("最终失败正文不可检查: parts=%+v err=%v", preview.Parts, err)
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
