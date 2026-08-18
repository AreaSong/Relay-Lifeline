package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
)

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
