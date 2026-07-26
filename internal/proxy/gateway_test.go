package proxy

import (
	"context"
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
	return NewGateway(store, registry, controller, notify.New(store, logger), logger), registry
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
	gateway, _ := testGateway(t, upstream.URL)
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
