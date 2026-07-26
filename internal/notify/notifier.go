package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
)

type Event struct {
	Type           string         `json:"type"`
	RequestID      string         `json:"requestId"`
	Attempts       int            `json:"attempts"`
	Elapsed        time.Duration  `json:"-"`
	Message        string         `json:"message"`
	MessageCode    string         `json:"messageCode,omitempty"`
	MessageDetails map[string]any `json:"messageDetails,omitempty"`
}

type webhookPayload struct {
	Type           string `json:"type"`
	EventCode      string `json:"eventCode"`
	RequestID      string `json:"requestId"`
	Attempts       int    `json:"attempts"`
	Elapsed        string `json:"elapsed"`
	ElapsedSeconds int64  `json:"elapsedSeconds"`
	Message        string `json:"message"`
}

type Notifier struct {
	store  *config.Store
	logger *slog.Logger
	client *http.Client
	queue  chan delivery
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type delivery struct {
	url      string
	payload  []byte
	attempts int
	backoff  time.Duration
}

func New(store *config.Store, logger *slog.Logger) *Notifier {
	ctx, cancel := context.WithCancel(context.Background())
	notifier := &Notifier{
		store: store, logger: logger, client: &http.Client{Timeout: 5 * time.Second},
		queue: make(chan delivery, 100), ctx: ctx, cancel: cancel,
	}
	notifier.wg.Add(1)
	go notifier.run()
	return notifier
}

func (n *Notifier) Send(event Event) {
	cfg := n.store.Get().Notifications
	allConfig := n.store.Get()
	webhookURL := cfg.WebhookURL
	if webhookURL == "" || !includes(cfg.EventTypes, event.Type) {
		return
	}
	message := event.Message
	if event.MessageCode != "" {
		message = l10n.Default.Text(cfg.Locale, allConfig.Localization.FallbackLocale, l10n.M(event.MessageCode, event.MessageDetails))
	}
	payload, err := json.Marshal(webhookPayload{
		Type: event.Type, EventCode: strings.ToUpper(event.Type), RequestID: event.RequestID, Attempts: event.Attempts,
		Elapsed: event.Elapsed.Round(time.Second).String(), ElapsedSeconds: int64(event.Elapsed.Round(time.Second).Seconds()), Message: message,
	})
	if err != nil {
		return
	}
	job := delivery{url: webhookURL, payload: payload, attempts: cfg.DeliveryAttempts, backoff: cfg.DeliveryBackoff.Duration}
	select {
	case n.queue <- job:
	default:
		n.logger.Warn(n.logText(allConfig, "notify.queue_full"), "event", "notification.queue_full", "event_type", event.Type)
	}
}

func includes(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (n *Notifier) Close() {
	n.once.Do(func() {
		n.cancel()
		n.wg.Wait()
	})
}

func (n *Notifier) run() {
	defer n.wg.Done()
	for {
		select {
		case <-n.ctx.Done():
			return
		case job := <-n.queue:
			n.deliver(job)
		}
	}
}

func (n *Notifier) deliver(job delivery) {
	var lastErr error
	for attempt := 1; attempt <= job.attempts; attempt++ {
		if err := n.post(job); err == nil {
			return
		} else {
			lastErr = err
		}
		if attempt < job.attempts && !n.wait(job.backoff) {
			return
		}
	}
	cfg := n.store.Get()
	n.logger.Warn(n.logText(cfg, "notify.delivery_failed"), "event", "notification.delivery_failed", "error", l10n.Default.Error(cfg.Logging.Locale, cfg.Localization.FallbackLocale, lastErr), "attempts", job.attempts)
}

func (n *Notifier) post(job delivery) error {
	request, err := http.NewRequestWithContext(n.ctx, http.MethodPost, job.url, bytes.NewReader(job.payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := n.client.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return l10n.E("notify.endpoint_http_error", nil, map[string]any{"Status": response.StatusCode})
	}
	return nil
}

func (n *Notifier) logText(cfg config.Config, messageID string) string {
	return l10n.Default.Text(cfg.Logging.Locale, cfg.Localization.FallbackLocale, l10n.M(messageID))
}

func (n *Notifier) wait(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-n.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
