package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/egress"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/sanitize"
)

const deliveryHistoryLimit = 100

var ErrWebhookNotConfigured = errors.New("webhook is not configured")
var ErrWebhookSigningNotConfigured = errors.New("webhook signing is not configured")

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
	store   *config.Store
	logger  *slog.Logger
	client  *http.Client
	policy  egress.Policy
	signing SigningConfig
	queue   chan delivery
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	once    sync.Once
	mu      sync.Mutex
	nextID  uint64
	stats   Status
	history []DeliveryRecord
}

type delivery struct {
	id        uint64
	eventType string
	requestID string
	url       string
	payload   []byte
	attempts  int
	backoff   time.Duration
}

type Status struct {
	Configured        bool      `json:"configured"`
	SigningConfigured bool      `json:"signingConfigured"`
	SigningKeyID      string    `json:"signingKeyId,omitempty"`
	QueueDepth        int       `json:"queueDepth"`
	QueueCapacity     int       `json:"queueCapacity"`
	Enqueued          uint64    `json:"enqueued"`
	Delivered         uint64    `json:"delivered"`
	Failed            uint64    `json:"failed"`
	Dropped           uint64    `json:"dropped"`
	LastAttemptAt     time.Time `json:"lastAttemptAt,omitempty"`
	LastSuccessAt     time.Time `json:"lastSuccessAt,omitempty"`
	LastFailureAt     time.Time `json:"lastFailureAt,omitempty"`
}

type DeliveryRecord struct {
	ID          uint64    `json:"id"`
	EventType   string    `json:"eventType"`
	RequestID   string    `json:"requestId,omitempty"`
	Outcome     string    `json:"outcome"`
	Attempts    int       `json:"attempts"`
	StatusCode  int       `json:"statusCode,omitempty"`
	CompletedAt time.Time `json:"completedAt"`
}

func New(store *config.Store, logger *slog.Logger) *Notifier {
	return NewWithSigning(store, logger, SigningConfig{})
}

func NewWithSigning(store *config.Store, logger *slog.Logger, signing SigningConfig) *Notifier {
	return NewWithSigningAndEgress(store, logger, signing, egress.Policy{})
}

func NewWithSigningAndEgress(store *config.Store, logger *slog.Logger, signing SigningConfig, policy egress.Policy) *Notifier {
	ctx, cancel := context.WithCancel(context.Background())
	client := (&egress.Policy{}).Client(&http.Client{Timeout: 5 * time.Second})
	client.Transport = policy.Transport(client.Transport.(*http.Transport))
	notifier := &Notifier{
		store: store, logger: logger, client: client, policy: policy.Normalize(), signing: signing,
		queue: make(chan delivery, 100), ctx: ctx, cancel: cancel,
	}
	notifier.wg.Add(1)
	go notifier.run()
	return notifier
}

func (n *Notifier) Send(event Event) {
	_ = n.enqueue(event, false)
}

func (n *Notifier) Test() error {
	return n.enqueue(Event{Type: "test", Message: "Relay-Lifeline webhook test"}, true)
}

func (n *Notifier) enqueue(event Event, force bool) error {
	cfg := n.store.Get().Notifications
	allConfig := n.store.Get()
	webhookURL := cfg.WebhookURL
	if webhookURL == "" {
		return ErrWebhookNotConfigured
	}
	if !n.signing.Configured() {
		return ErrWebhookSigningNotConfigured
	}
	if !force && !includes(cfg.EventTypes, event.Type) {
		return nil
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
		return err
	}
	n.mu.Lock()
	n.nextID++
	id := n.nextID
	n.mu.Unlock()
	job := delivery{id: id, eventType: event.Type, requestID: event.RequestID, url: webhookURL, payload: payload, attempts: cfg.DeliveryAttempts, backoff: cfg.DeliveryBackoff.Duration}
	select {
	case n.queue <- job:
		n.mu.Lock()
		n.stats.Enqueued++
		n.mu.Unlock()
		return nil
	default:
		n.mu.Lock()
		n.stats.Dropped++
		n.recordLocked(DeliveryRecord{ID: id, EventType: event.Type, RequestID: event.RequestID, Outcome: "dropped", CompletedAt: time.Now().UTC()})
		n.mu.Unlock()
		n.logger.Warn(n.logText(allConfig, "notify.queue_full"), "event", "notification.queue_full", "event_type", event.Type)
		return errors.New("webhook queue is full")
	}
}

func (n *Notifier) Snapshot() Status {
	n.mu.Lock()
	result := n.stats
	n.mu.Unlock()
	result.Configured = n.store.Get().Notifications.WebhookURL != ""
	result.SigningConfigured = n.signing.Configured()
	result.SigningKeyID = n.signing.KeyID
	result.QueueDepth = len(n.queue)
	result.QueueCapacity = cap(n.queue)
	return result
}

func (n *Notifier) Deliveries(limit int) []DeliveryRecord {
	if limit < 1 || limit > deliveryHistoryLimit {
		limit = deliveryHistoryLimit
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if limit > len(n.history) {
		limit = len(n.history)
	}
	result := make([]DeliveryRecord, limit)
	copy(result, n.history[len(n.history)-limit:])
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
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
	statusCode := 0
	completedAttempts := 0
	for attempt := 1; attempt <= job.attempts; attempt++ {
		completedAttempts = attempt
		n.mu.Lock()
		n.stats.LastAttemptAt = time.Now().UTC()
		n.mu.Unlock()
		code, err := n.post(job)
		statusCode = code
		if err == nil {
			n.mu.Lock()
			n.stats.Delivered++
			n.stats.LastSuccessAt = time.Now().UTC()
			n.recordLocked(DeliveryRecord{ID: job.id, EventType: job.eventType, RequestID: job.requestID, Outcome: "delivered", Attempts: attempt, StatusCode: statusCode, CompletedAt: time.Now().UTC()})
			n.mu.Unlock()
			return
		} else {
			lastErr = err
		}
		if attempt < job.attempts && !n.wait(job.backoff) {
			return
		}
	}
	n.mu.Lock()
	n.stats.Failed++
	n.stats.LastFailureAt = time.Now().UTC()
	n.recordLocked(DeliveryRecord{ID: job.id, EventType: job.eventType, RequestID: job.requestID, Outcome: "failed", Attempts: completedAttempts, StatusCode: statusCode, CompletedAt: time.Now().UTC()})
	n.mu.Unlock()
	cfg := n.store.Get()
	n.logger.Warn(n.logText(cfg, "notify.delivery_failed"), "event", "notification.delivery_failed", "error", sanitize.Text(l10n.Default.Error(cfg.Logging.Locale, cfg.Localization.FallbackLocale, lastErr)), "attempts", job.attempts)
}

func (n *Notifier) post(job delivery) (int, error) {
	if n.policy.DenyPrivateNetworks || len(n.policy.AllowedHosts) > 0 {
		if err := n.policy.ValidateURL(job.url); err != nil {
			return 0, err
		}
	}
	request, err := http.NewRequestWithContext(n.ctx, http.MethodPost, job.url, bytes.NewReader(job.payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	if n.signing.Configured() {
		timestamp, signature := n.signing.Sign(job.payload, time.Now().UTC())
		request.Header.Set(SignatureTimestampHeader, timestamp)
		request.Header.Set(SignatureKeyIDHeader, n.signing.KeyID)
		request.Header.Set(SignatureHeader, signature)
	}
	response, err := n.client.Do(request)
	if err != nil {
		return 0, err
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, l10n.E("notify.endpoint_http_error", nil, map[string]any{"Status": response.StatusCode})
	}
	return response.StatusCode, nil
}

func (n *Notifier) recordLocked(record DeliveryRecord) {
	n.history = append(n.history, record)
	if len(n.history) > deliveryHistoryLimit {
		n.history = n.history[len(n.history)-deliveryHistoryLimit:]
	}
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
