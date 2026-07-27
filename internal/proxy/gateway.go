package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

type Gateway struct {
	store      *config.Store
	registry   *state.Registry
	controller *state.Controller
	notifier   *notify.Notifier
	risk       *risk.Manager
	logger     *slog.Logger
	client     *http.Client
	limiter    Limiter
	retryGate  RetryGate
	randomMu   sync.Mutex
	random     *rand.Rand
	captures   *capture.Manager
	runLogs    *runlog.Store
	monitor    *monitoring.Store
}

func (g *Gateway) SetCaptureManager(manager *capture.Manager) { g.captures = manager }
func (g *Gateway) SetRunLog(store *runlog.Store)              { g.runLogs = store }
func (g *Gateway) SetMonitoring(store *monitoring.Store) {
	g.monitor = store
	g.recordLoad()
}

func NewGateway(store *config.Store, registry *state.Registry, controller *state.Controller, notifier *notify.Notifier, logger *slog.Logger, managers ...*risk.Manager) *Gateway {
	riskManager := risk.New()
	if len(managers) > 0 && managers[0] != nil {
		riskManager = managers[0]
	}
	gateway := &Gateway{
		store: store, registry: registry, controller: controller, notifier: notifier, logger: logger,
		risk: riskManager, client: newHTTPClient(store.Get()), random: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	gateway.limiter.SetOnChange(gateway.queueChanged)
	return gateway
}

func (g *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	cfg := g.store.Get()
	clientLocale := l10n.FromAcceptLanguage(request.Header.Get("Accept-Language"), cfg.Localization.DefaultLocale)
	body, err := readRequestBody(writer, request, int64(cfg.Server.MaxRequestBody), clientLocale, cfg.Localization.FallbackLocale)
	if err != nil {
		return
	}
	streaming := requestWantsStream(body, request.Header)
	ctx, cancel := context.WithCancel(request.Context())
	requestID, retryNow := g.registry.Add(request.Method, request.URL.Path, cancel)
	started := time.Now()
	outcome := "failed"
	finalAttempt := 0
	hadFailure := false
	if g.monitor != nil {
		g.monitor.RecordReceived()
		g.monitor.RecordEvent(monitoring.Event{Code: "request.received", RequestID: requestID})
		g.recordLoad()
	}
	if g.captures != nil {
		if _, captureErr := g.captures.BeginRequest(requestID, request.Method, request.URL.RequestURI(), request.Header, body); captureErr != nil {
			g.addRunLog("warn", "capture.request_failed", "无法捕获请求正文", requestID, 0, 0, map[string]any{"reason": captureErr.Error()})
		} else {
			g.addRunLog("info", "request.received", "收到代理请求", requestID, 0, 0, map[string]any{"method": request.Method, "path": request.URL.Path})
		}
	}
	defer func() {
		if outcome != "successful" && outcome != "rejected" && ctx.Err() != nil {
			outcome = "canceled"
			g.registry.RecordEvent(requestID, timeline.Event{Type: "canceled", MessageCode: "timeline.canceled"})
			g.addRunLog("info", "request.canceled", "客户端已取消请求", requestID, 0, 0, nil)
		}
		cancel()
		g.risk.ResolveRequest(requestID)
		g.registry.Remove(requestID, outcome)
		if g.monitor != nil {
			g.monitor.RecordFinal(outcome)
			g.monitor.RecordEvent(monitoring.Event{Code: terminalEventCode(outcome), RequestID: requestID})
			g.recordLoad()
		}
		if g.captures != nil {
			if captureErr := g.captures.Finish(requestID, outcome, finalAttempt); captureErr != nil {
				g.addRunLog("warn", "capture.finish_failed", "无法完成捕获记录", requestID, finalAttempt, 0, map[string]any{"reason": captureErr.Error()})
			}
		}
	}()

	downstream := startDownstream(writer, streaming, cfg.Stream.HeartbeatInterval.Duration, func() {
		g.addRunLog("debug", "downstream.heartbeat", "已发送下游保活心跳", requestID, 0, 0, nil)
	})
	defer downstream.stopHeartbeat()
	g.addRunLog("info", "queue.entered", "请求进入并发队列", requestID, 0, 0, nil)
	if err := g.limiter.Acquire(ctx, func() (int, int) {
		current := g.store.Get().Queue
		return current.MaxActive, current.MaxWaiting
	}); err != nil {
		message := l10n.M("proxy.queue_full")
		if !errors.Is(err, ErrQueueFull) {
			return
		}
		outcome = "rejected"
		g.addRunLog("warn", "queue.rejected", "等待队列已满", requestID, 0, 0, nil)
		downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, message))
		return
	}
	defer g.limiter.Release()
	g.addRunLog("info", "queue.acquired", "请求获得上游并发名额", requestID, 0, 0, nil)

	for attempt := 1; ; attempt++ {
		cfg = g.store.Get()
		if err := g.controller.Wait(ctx); err != nil {
			return
		}
		if attempt > 1 {
			if err := g.retryGate.Wait(ctx, cfg.Queue.RecoverySpacing.Duration); err != nil {
				return
			}
		}
		g.registry.UpdateMessage(requestID, "requesting", attempt, l10n.Message{}, time.Time{})
		if g.monitor != nil {
			g.monitor.RecordAttempt()
			g.monitor.RecordEvent(monitoring.Event{Code: "upstream.attempt_started", RequestID: requestID, Attempt: attempt})
			g.recordLoad()
		}
		g.registry.RecordEvent(requestID, timeline.Event{Type: "attempt_started", Attempt: attempt, MessageCode: "timeline.attempt_started"})
		g.addRunLog("info", "upstream.attempt_started", "开始上游请求", requestID, attempt, 0, nil)
		attemptStarted := time.Now()
		result := runAttempt(ctx, g.client, cfg, request, body, streaming)
		if g.captures != nil {
			var captureBody io.Reader
			if result.buffer != nil {
				captureBody, _ = result.buffer.Reader()
			}
			var headers http.Header
			if result.response != nil {
				headers = result.response.Header
			}
			if captureErr := g.captures.RecordAttempt(requestID, attempt, attemptStatus(result), headers, captureBody, result.err, attemptStarted); captureErr != nil {
				g.addRunLog("warn", "capture.attempt_failed", "无法捕获上游响应", requestID, attempt, attemptStatus(result), map[string]any{"reason": captureErr.Error()})
			}
			if closer, ok := captureBody.(io.Closer); ok {
				_ = closer.Close()
			}
		}
		if result.validation.Success {
			g.registry.SetUpstreamMessage(true, l10n.Message{})
			if err := downstream.deliver(result.buffer); err != nil {
				result.buffer.Close()
				g.registry.RecordEvent(requestID, timeline.Event{Type: "delivery_failed", Attempt: attempt, Category: "client", MessageCode: "timeline.delivery_failed"})
				return
			}
			result.buffer.Close()
			outcome = "successful"
			finalAttempt = attempt
			elapsed := time.Since(started)
			if hadFailure && g.monitor != nil {
				g.monitor.RecordRecovery(elapsed, attempt)
				g.monitor.RecordEvent(monitoring.Event{Code: "upstream.recovered", RequestID: requestID, Attempt: attempt})
			}
			g.registry.UpdateMessage(requestID, "completed", attempt, l10n.Message{}, time.Time{})
			g.registry.RecordEvent(requestID, timeline.Event{Type: "completed", Attempt: attempt, MessageCode: "timeline.completed"})
			if (g.registry.WasNotified(requestID) || g.risk.HasOpenRequestAlerts(requestID)) && cfg.Notifications.NotifyOnRecovery {
				g.notifier.Send(notify.Event{Type: "recovered", RequestID: requestID, Attempts: attempt, Elapsed: elapsed, MessageCode: "notify.recovered"})
			}
			g.logger.Info(g.logText(cfg, "log.request_success"), "event", "request.succeeded", "request_id", requestID, "attempt", attempt, "elapsed_ms", elapsed.Milliseconds())
			g.addRunLog("info", "request.succeeded", "完整响应已交付", requestID, attempt, attemptStatus(result), map[string]any{"elapsedMilliseconds": elapsed.Milliseconds()})
			return
		}
		errorDetail := extractSafeErrorDetail(cfg.Observability, result, streaming)
		if result.buffer != nil {
			result.buffer.Close()
		}
		g.registry.RecordFailure()
		reason := describeAttempt(result)
		statusCode := attemptStatus(result)
		category := attemptCategory(result)
		hadFailure = true
		if g.monitor != nil {
			g.monitor.RecordAttemptFailure(category)
			g.monitor.RecordEvent(monitoring.Event{Code: "upstream.failure", Category: category, RequestID: requestID, StatusCode: statusCode, Attempt: attempt})
		}
		g.registry.SetUpstreamMessage(false, reason)
		g.registry.RecordEvent(requestID, timeline.Event{
			Type: "attempt_failed", Attempt: attempt, StatusCode: statusCode,
			Category: category, MessageCode: reason.ID, MessageDetails: reason.Data,
			ErrorDetail: errorDetail,
		})
		g.publishAlerts(g.risk.EvaluateAttempt(requestID, attempt, started, statusCode, cfg.Risk))
		g.logger.Warn(g.logText(cfg, "log.upstream_failed"), "event", "upstream.request_failed", "request_id", requestID, "attempt", attempt, "reason_code", reason.ID, "status", statusCode)
		g.addRunLog("warn", "upstream.request_failed", "上游请求失败", requestID, attempt, statusCode, map[string]any{"reasonCode": reason.ID})
		if !shouldRetry(cfg, result) || cfg.Retry.MaxAttempts > 0 && attempt >= cfg.Retry.MaxAttempts {
			downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, reason))
			return
		}

		delay := g.retryDelay(cfg, result.response)
		g.addRunLog("info", "retry.scheduled", "已安排再次请求", requestID, attempt, statusCode, map[string]any{"waitMilliseconds": delay.Milliseconds()})
		nextRetry := time.Now().Add(delay)
		g.registry.UpdateMessage(requestID, "waiting", attempt, reason, nextRetry)
		g.recordLoad()
		g.registry.RecordEvent(requestID, timeline.Event{
			Type: "waiting", Attempt: attempt, MessageCode: "timeline.waiting", WaitMilliseconds: delay.Milliseconds(),
		})
		if time.Since(started) >= cfg.Notifications.StalledAfter.Duration && g.registry.MarkNotified(requestID) {
			g.notifier.Send(notify.Event{Type: "stalled", RequestID: requestID, Attempts: attempt, Elapsed: time.Since(started), MessageCode: "notify.stalled"})
		}
		resumeReason, err := waitForRetry(ctx, retryNow, delay, cfg.Risk.WarningAfter.Duration-time.Since(started), func() {
			g.publishAlerts(g.risk.EvaluateLongRunning(requestID, attempt, started, g.store.Get().Risk))
		})
		if err != nil {
			return
		}
		g.registry.RecordEvent(requestID, timeline.Event{Type: "retry_resumed", Attempt: attempt + 1, MessageCode: resumeReason})
		g.addRunLog("info", "retry.resumed", "重试等待结束", requestID, attempt+1, 0, map[string]any{"reasonCode": resumeReason})
	}
}

func (g *Gateway) addRunLog(level, event, message, requestID string, attempt, statusCode int, fields map[string]any) {
	if g.runLogs == nil {
		return
	}
	g.runLogs.Add(runlog.Entry{Level: level, Event: event, Message: message, RequestID: requestID, Attempt: attempt, StatusCode: statusCode, Fields: fields})
}

func readRequestBody(writer http.ResponseWriter, request *http.Request, limit int64, locale, fallback string) ([]byte, error) {
	defer request.Body.Close()
	reader := io.LimitReader(request.Body, limit+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		http.Error(writer, l10n.Default.Text(locale, fallback, l10n.M("api.request.read_failed")), http.StatusBadRequest)
		return nil, err
	}
	if int64(len(body)) > limit {
		message := l10n.Default.Text(locale, fallback, l10n.M("api.request.body_too_large"))
		http.Error(writer, message, http.StatusRequestEntityTooLarge)
		return nil, l10n.E("api.request.body_too_large", nil)
	}
	return body, nil
}

func (g *Gateway) retryDelay(cfg config.Config, response *http.Response) time.Duration {
	minimum, maximum := cfg.Retry.MinInterval.Duration, cfg.Retry.MaxInterval.Duration
	delay := minimum
	if maximum > minimum {
		g.randomMu.Lock()
		delay += time.Duration(g.random.Int63n(int64(maximum-minimum) + 1))
		g.randomMu.Unlock()
	}
	if cfg.Retry.HonorRetryAfter {
		delay = max(delay, retryAfter(response))
	}
	return delay
}

func waitForRetry(ctx context.Context, retryNow <-chan struct{}, delay, riskAfter time.Duration, onRisk func()) (string, error) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var riskTimer *time.Timer
	var riskChannel <-chan time.Time
	if riskAfter <= 0 {
		onRisk()
	} else if riskAfter < delay {
		riskTimer = time.NewTimer(riskAfter)
		riskChannel = riskTimer.C
		defer riskTimer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-retryNow:
			return "timeline.retry_manual", nil
		case <-timer.C:
			return "timeline.retry_timer", nil
		case <-riskChannel:
			onRisk()
			riskChannel = nil
		}
	}
}

func (g *Gateway) queueChanged(active, waiting int) {
	g.recordLoad()
	cfg := g.store.Get()
	alerts := g.risk.EvaluateQueue(active, waiting, cfg.Queue.MaxActive, cfg.Queue.MaxWaiting, cfg.Risk)
	g.publishAlerts(alerts)
}

func (g *Gateway) publishAlerts(alerts []risk.Alert) {
	for _, alert := range alerts {
		if alert.RequestID != "" {
			g.registry.RecordEvent(alert.RequestID, timeline.Event{Type: "risk_warning", Category: alert.Type, MessageCode: alert.MessageCode, MessageDetails: alert.MessageDetails})
		}
		g.notifier.Send(notify.Event{
			Type: alert.Type, RequestID: alert.RequestID, Attempts: alert.Attempts,
			Elapsed: time.Duration(alert.ElapsedMilliseconds) * time.Millisecond, MessageCode: alert.MessageCode, MessageDetails: alert.MessageDetails,
		})
	}
}

func attemptStatus(result attemptResult) int {
	if result.response == nil {
		return 0
	}
	return result.response.StatusCode
}

func attemptCategory(result attemptResult) string {
	if result.err != nil || result.response == nil {
		return "transport"
	}
	status := result.response.StatusCode
	switch {
	case status >= 200 && status < 300:
		return "protocol"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth"
	case status == http.StatusTooManyRequests:
		return "rate_limit"
	case status >= 400 && status < 500:
		return "client"
	case status >= 500 && status < 600:
		return "server"
	default:
		return "http"
	}
}

func terminalEventCode(outcome string) string {
	switch outcome {
	case "successful":
		return "request.succeeded"
	case "canceled":
		return "request.canceled"
	case "rejected":
		return "request.rejected"
	default:
		return "request.failed"
	}
}

func (g *Gateway) recordLoad() {
	if g.monitor == nil {
		return
	}
	snapshot := g.registry.Snapshot(g.controller.IsPaused())
	g.monitor.SetLoad(snapshot.Active, snapshot.Queued, snapshot.Waiting, snapshot.Requesting)
}

func (g *Gateway) Registry() *state.Registry     { return g.registry }
func (g *Gateway) Controller() *state.Controller { return g.controller }

func (g *Gateway) text(locale, fallback string, message l10n.Message) string {
	return l10n.Default.Text(locale, fallback, message)
}

func (g *Gateway) logText(cfg config.Config, messageID string) string {
	return g.text(cfg.Logging.Locale, cfg.Localization.FallbackLocale, l10n.M(messageID))
}

func (g *Gateway) String() string {
	return fmt.Sprintf("Relay-Lifeline -> %s", g.store.Get().Upstream.BaseURL)
}
