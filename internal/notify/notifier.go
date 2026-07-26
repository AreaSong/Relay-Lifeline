package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

type Event struct {
	Type      string        `json:"type"`
	RequestID string        `json:"requestId"`
	Attempts  int           `json:"attempts"`
	Elapsed   time.Duration `json:"-"`
	Message   string        `json:"message"`
}

type webhookPayload struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	Attempts  int    `json:"attempts"`
	Elapsed   string `json:"elapsed"`
	Message   string `json:"message"`
}

type Notifier struct {
	store  *config.Store
	logger *slog.Logger
	client *http.Client
}

func New(store *config.Store, logger *slog.Logger) *Notifier {
	return &Notifier{store: store, logger: logger, client: &http.Client{Timeout: 5 * time.Second}}
}

func (n *Notifier) Send(event Event) {
	webhookURL := n.store.Get().Notifications.WebhookURL
	if webhookURL == "" {
		return
	}
	payload, err := json.Marshal(webhookPayload{
		Type: event.Type, RequestID: event.RequestID, Attempts: event.Attempts,
		Elapsed: event.Elapsed.Round(time.Second).String(), Message: event.Message,
	})
	if err != nil {
		return
	}
	go func() {
		request, requestErr := http.NewRequestWithContext(context.Background(), http.MethodPost, webhookURL, bytes.NewReader(payload))
		if requestErr != nil {
			return
		}
		request.Header.Set("Content-Type", "application/json")
		response, responseErr := n.client.Do(request)
		if responseErr != nil {
			n.logger.Warn("通知发送失败", "error", responseErr)
			return
		}
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			n.logger.Warn("通知端点返回错误", "status", response.StatusCode)
		}
	}()
}
