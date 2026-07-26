package notify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
