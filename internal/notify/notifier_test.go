package notify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

func TestWebhookNotificationContainsOnlyMetadata(t *testing.T) {
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		received <- payload
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Notifications.WebhookURL = server.URL
	store := config.NewStore("", cfg)
	notifier := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer notifier.Close()
	notifier.Send(Event{Type: "stalled", RequestID: "abc123", Attempts: 4, Elapsed: 2 * time.Minute, Message: "HTTP 503"})
	select {
	case payload := <-received:
		encoded, _ := json.Marshal(payload)
		if strings.Contains(string(encoded), "Authorization") || payload["requestId"] != "abc123" {
			t.Fatalf("通知内容异常: %s", encoded)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到通知")
	}
}

func TestWebhookEventFilter(t *testing.T) {
	var requests atomic.Int32
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		received <- struct{}{}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Notifications.WebhookURL = server.URL
	cfg.Notifications.EventTypes = []string{"recovered"}
	store := config.NewStore("", cfg)
	notifier := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer notifier.Close()
	notifier.Send(Event{Type: "stalled", Message: "filtered"})
	time.Sleep(20 * time.Millisecond)
	if requests.Load() != 0 {
		t.Fatal("被禁用的通知事件仍被投递")
	}
	notifier.Send(Event{Type: "recovered", Message: "allowed"})
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("启用的通知事件未被投递")
	}
}

func TestWebhookRetriesWithoutBlockingCaller(t *testing.T) {
	var attempts int
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(writer, "retry", http.StatusServiceUnavailable)
			return
		}
		received <- struct{}{}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Notifications.WebhookURL = server.URL
	cfg.Notifications.DeliveryBackoff.Duration = time.Millisecond
	store := config.NewStore("", cfg)
	notifier := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer notifier.Close()
	started := time.Now()
	notifier.Send(Event{Type: "many_attempts", RequestID: "request", Message: "test"})
	if time.Since(started) > 20*time.Millisecond {
		t.Fatal("发送通知阻塞了调用方")
	}
	select {
	case <-received:
		if attempts != 3 {
			t.Fatalf("通知尝试次数 = %d", attempts)
		}
	case <-time.After(time.Second):
		t.Fatal("通知重试未成功")
	}
}
