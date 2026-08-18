package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	notifier := newSignedTestNotifier(store)
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
	notifier := newSignedTestNotifier(store)
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
	notifier := newSignedTestNotifier(store)
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

func TestWebhookLocaleHotReloadAndStableFields(t *testing.T) {
	received := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		received <- payload
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Notifications.WebhookURL = server.URL
	cfg.Notifications.Locale = "en-US"
	store := config.NewStore("", cfg)
	notifier := newSignedTestNotifier(store)
	defer notifier.Close()

	notifier.Send(Event{Type: "stalled", RequestID: "english", Attempts: 2, Elapsed: 90 * time.Second, MessageCode: "notify.stalled"})
	english := waitForPayload(t, received)
	if english["eventCode"] != "STALLED" || english["message"] != "The upstream request remains unavailable" || english["elapsedSeconds"] != float64(90) {
		t.Fatalf("英文 Webhook 字段异常: %+v", english)
	}

	cfg.Notifications.Locale = "zh-CN"
	if err := store.Update(cfg, false); err != nil {
		t.Fatal(err)
	}
	notifier.Send(Event{Type: "stalled", RequestID: "chinese", Attempts: 3, Elapsed: 2 * time.Minute, MessageCode: "notify.stalled"})
	chinese := waitForPayload(t, received)
	if chinese["eventCode"] != "STALLED" || chinese["message"] != "上游请求持续不可用" || chinese["elapsedSeconds"] != float64(120) {
		t.Fatalf("中文 Webhook 字段异常: %+v", chinese)
	}
}

func TestWebhookOperationsExposeHealthHistoryAndTestDelivery(t *testing.T) {
	received := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		received <- struct{}{}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Notifications.WebhookURL = server.URL
	store := config.NewStore("", cfg)
	notifier := newSignedTestNotifier(store)
	defer notifier.Close()
	if err := notifier.Test(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("测试通知未送达")
	}
	deadline := time.Now().Add(time.Second)
	for notifier.Snapshot().Delivered == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := notifier.Snapshot()
	if !status.Configured || status.Enqueued != 1 || status.Delivered != 1 || status.QueueCapacity != 100 {
		t.Fatalf("通知健康快照异常: %+v", status)
	}
	history := notifier.Deliveries(10)
	if len(history) != 1 || history[0].EventType != "test" || history[0].Outcome != "delivered" || history[0].StatusCode != http.StatusAccepted {
		t.Fatalf("通知投递历史异常: %+v", history)
	}
}

func TestWebhookTestRequiresConfiguration(t *testing.T) {
	store := config.NewStore("", config.Default())
	notifier := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer notifier.Close()
	if err := notifier.Test(); err != ErrWebhookNotConfigured {
		t.Fatalf("未配置 Webhook 时应拒绝测试: %v", err)
	}
}

func TestWebhookRejectsUnsignedRuntimeConfiguration(t *testing.T) {
	cfg := config.Default()
	cfg.Notifications.WebhookURL = "https://example.test/hook"
	notifier := New(config.NewStore("", cfg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer notifier.Close()
	if err := notifier.Test(); err != ErrWebhookSigningNotConfigured {
		t.Fatalf("缺少签名时应拒绝投递: %v", err)
	}
}

func TestWebhookSigningHeadersUseTimestampAndPayload(t *testing.T) {
	received := make(chan struct {
		header http.Header
		body   []byte
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		received <- struct {
			header http.Header
			body   []byte
		}{request.Header.Clone(), body}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Notifications.WebhookURL = server.URL
	store := config.NewStore("", cfg)
	secret := strings.Repeat("s", 32)
	notifier := NewWithSigning(store, slog.New(slog.NewTextHandler(io.Discard, nil)), SigningConfig{KeyID: "primary-2026", Secret: secret})
	defer notifier.Close()
	notifier.Send(Event{Type: "stalled", RequestID: "request-1", Message: "signed"})
	select {
	case result := <-received:
		timestamp := result.header.Get(SignatureTimestampHeader)
		if timestamp == "" || result.header.Get(SignatureKeyIDHeader) != "primary-2026" {
			t.Fatalf("签名头缺失: %v", result.header)
		}
		hasher := hmac.New(sha256.New, []byte(secret))
		_, _ = hasher.Write([]byte(timestamp + "." + string(result.body)))
		want := "v1=" + hex.EncodeToString(hasher.Sum(nil))
		if result.header.Get(SignatureHeader) != want {
			t.Fatalf("签名不匹配: got=%q want=%q", result.header.Get(SignatureHeader), want)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到签名 Webhook")
	}
}

func TestWebhookSigningConfigurationRequiresCompleteSecret(t *testing.T) {
	if err := ValidateSigningConfig(SigningConfig{}, true); err != ErrSigningKeyIDRequired {
		t.Fatalf("缺少 Key ID 的错误异常: %v", err)
	}
	if err := ValidateSigningConfig(SigningConfig{KeyID: "primary", Secret: "short"}, true); err != ErrSigningSecretShort {
		t.Fatalf("短签名密钥的错误异常: %v", err)
	}
	if err := ValidateSigningConfig(SigningConfig{KeyID: "primary", Secret: strings.Repeat("s", 32)}, true); err != nil {
		t.Fatalf("完整签名配置不应失败: %v", err)
	}
}

func waitForPayload(t *testing.T, received <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case payload := <-received:
		return payload
	case <-time.After(time.Second):
		t.Fatal("未收到 Webhook")
		return nil
	}
}

func newSignedTestNotifier(store *config.Store) *Notifier {
	return NewWithSigning(store, slog.New(slog.NewTextHandler(io.Discard, nil)), SigningConfig{
		KeyID: "test-key", Secret: strings.Repeat("s", 32),
	})
}
