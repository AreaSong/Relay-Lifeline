package proxy

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

type Gateway struct {
	store         *config.Store
	registry      *state.Registry
	controller    *state.Controller
	notifier      *notify.Notifier
	risk          *risk.Manager
	logger        *slog.Logger
	client        *http.Client
	limiter       Limiter
	retryGate     RetryGate
	randomMu      sync.Mutex
	random        *rand.Rand
	captures      *capture.Manager
	runLogs       *runlog.Store
	monitor       *monitoring.Store
	incidents     *incident.Store
	resourceCheck func(config.Config) error
	repeater      *repeat.Manager
}

func (g *Gateway) SetCaptureManager(manager *capture.Manager) { g.captures = manager }
func (g *Gateway) SetRunLog(store *runlog.Store)              { g.runLogs = store }
func (g *Gateway) SetIncidents(store *incident.Store)         { g.incidents = store }
func (g *Gateway) SetRepeatManager(manager *repeat.Manager)   { g.repeater = manager }
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
		resourceCheck: checkResources,
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
	if err := g.resourceCheck(cfg); err != nil {
		http.Error(writer, g.text(clientLocale, cfg.Localization.FallbackLocale, l10n.M("proxy.resource_protected")), http.StatusServiceUnavailable)
		return
	}
	streaming := requestWantsStream(body, request.Header)
	prepareIdempotencyKey(request.Header, cfg.Lifecycle)
	baseContext := request.Context()
	if cfg.Lifecycle.ClientDisconnectPolicy == "finish-attempt" {
		baseContext = context.WithoutCancel(baseContext)
	}
	ctx, cancel := requestContext(baseContext, cfg.Lifecycle.MaxRequestDuration.Duration)
	var clientDisconnected atomic.Bool
	onDisconnect := func() {
		clientDisconnected.Store(true)
		if cfg.Lifecycle.ClientDisconnectPolicy == "cancel" {
			cancel()
		}
	}
	watchDownstreamClose(ctx, writer, onDisconnect)
	requestID, retryNow := g.registry.Add(request.Method, request.URL.Path, cancel)
	if g.repeater != nil {
		g.repeater.RegisterSource(requestID, repeat.Template{
			Method: request.Method, Path: request.URL.RequestURI(), Headers: request.Header,
			Body: body, Streaming: streaming,
		})
		defer g.repeater.UnregisterSource(requestID)
	}
	started := time.Now()
	outcome := lifecycle.StateFailed
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
		if outcome != lifecycle.StateSuccessful && outcome != lifecycle.StateRejected && (ctx.Err() != nil || clientDisconnected.Load()) {
			outcome = lifecycle.StateCanceled
			event := timeline.Event{Type: "canceled", MessageCode: "timeline.canceled"}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				outcome = lifecycle.StateExpired
				event = timeline.Event{Type: "expired", MessageCode: "timeline.expired"}
			}
			g.registry.RecordEvent(requestID, event)
			g.addRunLog("info", "request.canceled", "客户端已取消请求", requestID, 0, 0, nil)
		}
		cancel()
		g.risk.ResolveRequest(requestID)
		g.registry.Remove(requestID, outcome)
		if g.monitor != nil {
			g.monitor.RecordFinal(string(outcome))
			g.monitor.RecordEvent(monitoring.Event{Code: terminalEventCode(outcome), RequestID: requestID})
			g.recordLoad()
		}
		if g.captures != nil {
			if captureErr := g.captures.Finish(requestID, string(outcome), finalAttempt); captureErr != nil {
				g.addRunLog("warn", "capture.finish_failed", "无法完成捕获记录", requestID, finalAttempt, 0, map[string]any{"reason": captureErr.Error()})
			}
		}
	}()

	downstream := startDownstream(writer, streaming, cfg.Stream.HeartbeatInterval.Duration, func() {
		g.addRunLog("debug", "downstream.heartbeat", "已发送下游保活心跳", requestID, 0, 0, nil)
	}, func(error) {
		onDisconnect()
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
		outcome = lifecycle.StateRejected
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
		g.registry.UpdateMessage(requestID, lifecycle.StateForwarding, attempt, l10n.Message{}, time.Time{})
		if g.monitor != nil {
			g.monitor.RecordAttempt()
			g.monitor.RecordEvent(monitoring.Event{Code: "upstream.attempt_started", RequestID: requestID, Attempt: attempt})
			g.recordLoad()
		}
		g.registry.RecordEvent(requestID, timeline.Event{Type: "attempt_started", Attempt: attempt, MessageCode: "timeline.attempt_started"})
		g.addRunLog("info", "upstream.attempt_started", "开始上游请求", requestID, attempt, 0, nil)
		attemptStarted := time.Now()
		result := runAttempt(ctx, g.client, cfg, request, body, streaming)
		finalAttempt = attempt
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
		if clientDisconnected.Load() && cfg.Lifecycle.ClientDisconnectPolicy == "finish-attempt" {
			if result.buffer != nil {
				result.buffer.Close()
			}
			return
		}
		if result.validation.Success {
			g.registry.SetUpstreamMessage(true, l10n.Message{})
			if g.incidents != nil {
				g.incidents.RecordSuccess()
			}
			g.registry.UpdateMessage(requestID, lifecycle.StateBuffering, attempt, l10n.Message{}, time.Time{})
			g.registry.UpdateMessage(requestID, lifecycle.StateDelivering, attempt, l10n.Message{}, time.Time{})
			if err := downstream.deliver(result.buffer); err != nil {
				result.buffer.Close()
				g.registry.RecordEvent(requestID, timeline.Event{Type: "delivery_failed", Attempt: attempt, Category: "client", MessageCode: "timeline.delivery_failed", AttemptPhase: string(lifecycle.PhaseDelivery)})
				return
			}
			result.buffer.Close()
			outcome = lifecycle.StateSuccessful
			elapsed := time.Since(started)
			if hadFailure && g.monitor != nil {
				g.monitor.RecordRecovery(elapsed, attempt)
				g.monitor.RecordEvent(monitoring.Event{Code: "upstream.recovered", RequestID: requestID, Attempt: attempt})
			}
			g.registry.UpdateMessage(requestID, lifecycle.StateCompleted, attempt, l10n.Message{}, time.Time{})
			g.registry.RecordEvent(requestID, timeline.Event{Type: "completed", Attempt: attempt, MessageCode: "timeline.completed"})
			if (g.registry.WasNotified(requestID) || g.risk.HasOpenRequestAlerts(requestID)) && cfg.Notifications.NotifyOnRecovery {
				g.notifier.Send(notify.Event{Type: "recovered", RequestID: requestID, Attempts: attempt, Elapsed: elapsed, MessageCode: "notify.recovered"})
			}
			g.logger.Info(g.logText(cfg, "log.request_success"), "event", "request.succeeded", "request_id", requestID, "attempt", attempt, "elapsed_ms", elapsed.Milliseconds())
			g.addRunLog("info", "request.succeeded", "完整响应已交付", requestID, attempt, attemptStatus(result), map[string]any{"elapsedMilliseconds": elapsed.Milliseconds()})
			return
		}
		errorDetail := extractSafeErrorDetail(cfg.Observability, result, streaming)
		if result.response != nil {
			g.registry.UpdateMessage(requestID, lifecycle.StateBuffering, attempt, l10n.Message{}, time.Time{})
		} else if result.wroteRequest && cfg.Lifecycle.TrackUncertainDelivery {
			g.registry.UpdateMessage(requestID, lifecycle.StateUncertain, attempt, describeAttempt(result), time.Time{})
			g.registry.RecordEvent(requestID, timeline.Event{
				Type: "uncertain", Attempt: attempt, Category: "duplicate_risk",
				MessageCode: "timeline.uncertain", AttemptPhase: string(result.phase),
			})
		}
		if result.buffer != nil {
			result.buffer.Close()
		}
		g.registry.RecordFailure()
		reason := describeAttempt(result)
		statusCode := attemptStatus(result)
		category := attemptCategory(result)
		if g.incidents != nil {
			g.incidents.RecordFailure(requestID, category, statusCode)
		}
		hadFailure = true
		if g.monitor != nil {
			g.monitor.RecordAttemptFailure(category)
			g.monitor.RecordEvent(monitoring.Event{Code: "upstream.failure", Category: category, RequestID: requestID, StatusCode: statusCode, Attempt: attempt})
		}
		g.registry.SetUpstreamMessage(false, reason)
		g.registry.RecordEvent(requestID, timeline.Event{
			Type: "attempt_failed", Attempt: attempt, StatusCode: statusCode,
			Category: category, MessageCode: reason.ID, MessageDetails: reason.Data,
			ErrorDetail: errorDetail, AttemptPhase: string(result.phase),
		})
		g.publishAlerts(g.risk.EvaluateAttempt(requestID, attempt, started, statusCode, cfg.Risk))
		g.logger.Warn(g.logText(cfg, "log.upstream_failed"), "event", "upstream.request_failed", "request_id", requestID, "attempt", attempt, "reason_code", reason.ID, "status", statusCode)
		g.addRunLog("warn", "upstream.request_failed", "上游请求失败", requestID, attempt, statusCode, map[string]any{"reasonCode": reason.ID})
		policy, policyActive := g.registry.RetryPolicy(requestID)
		if !g.retryAllowed(cfg, result, attempt, policyActive) {
			downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, reason))
			return
		}

		delay := g.retryDelay(cfg, result.response)
		if policyActive {
			delay = retryPolicyDelay(policy.Interval, cfg, result.response)
			remaining := time.Until(policy.Deadline)
			if remaining <= 0 {
				g.finishExpiredRetry(requestID, attempt, downstream, clientLocale, cfg, reason)
				outcome = lifecycle.StateExpired
				return
			}
			delay = min(delay, remaining)
		}
		g.addRunLog("info", "retry.scheduled", "已安排再次请求", requestID, attempt, statusCode, map[string]any{"waitMilliseconds": delay.Milliseconds()})
		nextRetry := time.Now().Add(delay)
		g.registry.UpdateMessage(requestID, lifecycle.StateWaiting, attempt, reason, nextRetry)
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
		if policyActive && !time.Now().Before(policy.Deadline) {
			g.finishExpiredRetry(requestID, attempt, downstream, clientLocale, cfg, reason)
			outcome = lifecycle.StateExpired
			return
		}
		g.registry.RecordEvent(requestID, timeline.Event{Type: "retry_resumed", Attempt: attempt + 1, MessageCode: resumeReason})
		g.addRunLog("info", "retry.resumed", "重试等待结束", requestID, attempt+1, 0, map[string]any{"reasonCode": resumeReason})
	}
}

func (g *Gateway) retryAllowed(cfg config.Config, result attemptResult, attempt int, active bool) bool {
	if active {
		return !result.validation.Success
	}
	return shouldRetry(cfg, result) && (cfg.Retry.MaxAttempts == 0 || attempt < cfg.Retry.MaxAttempts)
}

func retryPolicyDelay(interval time.Duration, cfg config.Config, response *http.Response) time.Duration {
	delay := interval
	if cfg.Retry.HonorRetryAfter {
		delay = max(delay, retryAfter(response))
	}
	return delay
}

func (g *Gateway) finishExpiredRetry(id string, attempt int, downstream *downstreamWriter, locale string, cfg config.Config, reason l10n.Message) {
	g.registry.RecordEvent(id, timeline.Event{Type: "retry_window_expired", Attempt: attempt, MessageCode: "timeline.retry_window_expired"})
	g.addRunLog("info", "retry.window_expired", "单请求重试窗口已结束", id, attempt, 0, nil)
	downstream.fail(g.text(locale, cfg.Localization.FallbackLocale, reason))
}

func checkResources(cfg config.Config) error {
	directory := cfg.Stream.TempDir
	if directory == "" {
		directory = os.TempDir()
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(directory, &stats); err != nil {
		return err
	}
	available := int64(stats.Bavail) * int64(stats.Bsize)
	if available < int64(cfg.Risk.MinimumFreeDisk) {
		return fmt.Errorf("available disk %d below minimum %d", available, cfg.Risk.MinimumFreeDisk)
	}
	return nil
}

func watchDownstreamClose(ctx context.Context, writer http.ResponseWriter, onClose func()) {
	notifier, ok := writer.(http.CloseNotifier)
	if !ok {
		return
	}
	closed := notifier.CloseNotify()
	go func() {
		select {
		case <-ctx.Done():
		case <-closed:
			onClose()
		}
	}()
}

func requestContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if maximum > 0 {
		return context.WithTimeout(parent, maximum)
	}
	return context.WithCancel(parent)
}

func prepareIdempotencyKey(header http.Header, cfg config.LifecycleConfig) {
	if !cfg.PreserveIdempotencyKey {
		header.Del("Idempotency-Key")
	}
	if cfg.GenerateIdempotencyKey && header.Get("Idempotency-Key") == "" {
		buffer := make([]byte, 16)
		if _, err := cryptorand.Read(buffer); err == nil {
			header.Set("Idempotency-Key", "lifeline-"+hex.EncodeToString(buffer))
		}
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

func terminalEventCode(outcome lifecycle.State) string {
	switch outcome {
	case lifecycle.StateSuccessful:
		return "request.succeeded"
	case lifecycle.StateCanceled:
		return "request.canceled"
	case lifecycle.StateRejected:
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
