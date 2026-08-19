package proxy

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/disk"
	"github.com/areasong/relay-lifeline/internal/egress"
	"github.com/areasong/relay-lifeline/internal/governance"
	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	trafficpolicy "github.com/areasong/relay-lifeline/internal/policy"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/sanitize"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/telemetry"
	"github.com/areasong/relay-lifeline/internal/timeline"
	"github.com/areasong/relay-lifeline/internal/upstream"
)

type Gateway struct {
	store         *config.Store
	registry      *state.Registry
	controller    *state.Controller
	notifier      *notify.Notifier
	risk          *risk.Manager
	logger        *slog.Logger
	client        *http.Client // test-only transport override; target runtimes own production clients
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
	cacheBudget   cacheBudget
	upstreams     *upstream.Manager
	governance    *governance.Manager
	trafficPolicy *trafficpolicy.Manager
	tracer        trace.Tracer
	lifecycleCtx  context.Context
}

func (g *Gateway) SetCaptureManager(manager *capture.Manager) { g.captures = manager }
func (g *Gateway) SetRunLog(store *runlog.Store)              { g.runLogs = store }
func (g *Gateway) SetIncidents(store *incident.Store)         { g.incidents = store }
func (g *Gateway) SetRepeatManager(manager *repeat.Manager)   { g.repeater = manager }
func (g *Gateway) SetGovernanceManager(manager *governance.Manager) {
	if manager != nil {
		g.governance = manager
	}
}
func (g *Gateway) SetLifecycleContext(ctx context.Context) {
	if ctx != nil {
		g.lifecycleCtx = ctx
	}
}
func (g *Gateway) SetMonitoring(store *monitoring.Store) {
	g.monitor = store
	g.recordLoad()
}

func (g *Gateway) UpstreamStatus() upstream.PoolStatus {
	if g.upstreams == nil {
		return upstream.PoolStatus{}
	}
	return g.upstreams.Snapshot()
}

func (g *Gateway) GovernanceStatus() governance.Snapshot {
	if g.governance == nil {
		return governance.Snapshot{}
	}
	return g.governance.Snapshot()
}

func (g *Gateway) PolicyStatus(limit int) trafficpolicy.Status {
	if g.trafficPolicy == nil {
		return trafficpolicy.Status{}
	}
	return g.trafficPolicy.Status(limit)
}

func (g *Gateway) SimulatePolicy(input trafficpolicy.Input) trafficpolicy.Decision {
	if g.trafficPolicy == nil {
		return trafficpolicy.Decision{DryRun: true, Action: "default", Reason: "unavailable"}
	}
	return g.trafficPolicy.Evaluate(input, true)
}

func NewGateway(store *config.Store, registry *state.Registry, controller *state.Controller, notifier *notify.Notifier, logger *slog.Logger, managers ...*risk.Manager) *Gateway {
	riskManager := risk.New()
	if len(managers) > 0 && managers[0] != nil {
		riskManager = managers[0]
	}
	policy := egressPolicyForConfig(store.Get())
	upstreamManager, upstreamErr := upstream.NewWithEgress(store.Get().Upstreams, store.Get().Upstream, policy)
	if upstreamErr != nil {
		logger.Warn("invalid upstream pool; using legacy primary", "event", "upstream.pool_invalid", "error", upstreamErr)
		upstreamManager, _ = upstream.NewWithEgress(config.UpstreamPoolConfig{Strategy: "primary-only"}, store.Get().Upstream, policy)
	}
	gateway := &Gateway{
		store: store, registry: registry, controller: controller, notifier: notifier, logger: logger,
		risk: riskManager, random: rand.New(rand.NewSource(time.Now().UnixNano())),
		resourceCheck: checkResources, upstreams: upstreamManager, governance: governance.New(store.Get().Governance), tracer: telemetry.Tracer("relay-lifeline/proxy"),
		trafficPolicy: trafficpolicy.New(store.Get().TrafficPolicy), lifecycleCtx: context.Background(),
	}
	gateway.limiter.SetOnChange(gateway.queueChanged)
	store.Subscribe(gateway.applyConfig)
	return gateway
}

func (g *Gateway) applyConfig(cfg config.Config) {
	if g.upstreams != nil {
		policy := egressPolicyForConfig(cfg)
		if err := g.upstreams.ApplyWithEgress(cfg.Upstreams, cfg.Upstream, policy); err != nil {
			g.logger.Error("apply upstream pool", "event", "upstream.pool_apply_failed", "error", err)
		}
	}
	if g.governance != nil {
		g.governance.Apply(cfg.Governance)
	}
	if g.trafficPolicy != nil {
		g.trafficPolicy.Apply(cfg.TrafficPolicy)
	}
}

func (g *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	cfg := g.store.Get()
	inboundContext := telemetry.Extract(request.Context(), propagation.HeaderCarrier(request.Header))
	inboundContext, span := g.tracer.Start(inboundContext, "relay.proxy.request", trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.request.method", request.Method), attribute.String("http.route", request.URL.Path)))
	request = request.WithContext(inboundContext)
	defer span.End()
	clientLocale := l10n.FromAcceptLanguage(request.Header.Get("Accept-Language"), cfg.Localization.DefaultLocale)
	if !g.controller.Accepting() {
		span.SetStatus(codes.Error, "maintenance admission denied")
		writer.Header().Set("Retry-After", "30")
		if g.monitor != nil {
			g.monitor.RecordEvent(monitoring.Event{Code: "request.rejected", Category: g.controller.Mode(), Outcome: "rejected"})
		}
		http.Error(writer, g.text(clientLocale, cfg.Localization.FallbackLocale, l10n.M("proxy.maintenance")), http.StatusServiceUnavailable)
		return
	}
	body, err := readRequestBody(writer, request, int64(cfg.Server.MaxRequestBody), clientLocale, cfg.Localization.FallbackLocale)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request body rejected")
		return
	}
	if err := g.resourceCheck(cfg); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "resource protection")
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
	identity := clientIdentityFromHeaders(request.Header)
	// Persist the request anchor before policy evaluation so canary/shadow
	// bucketing and all subsequent audit records use one stable request ID.
	requestID, retryNow, timelineErr := g.registry.AddWithIdentityChecked(request.Method, request.URL.Path, cancel, identity)
	if timelineErr != nil {
		if g.monitor != nil {
			g.monitor.RecordPersistenceFailure()
		}
		span.RecordError(timelineErr)
		span.SetStatus(codes.Error, "request journal unavailable")
		http.Error(writer, g.text(clientLocale, cfg.Localization.FallbackLocale, l10n.M("proxy.persistence_unavailable")), http.StatusServiceUnavailable)
		return
	}
	span.SetAttributes(attribute.String("relay.request.id", requestID))
	writer.Header().Set("X-Relay-Lifeline-Request-ID", requestID)
	policyDecision := g.evaluateTrafficPolicyWithID(request, body, false, requestID)
	if policyDecision.Denied && policyDecision.Enforced {
		cancel()
		_ = g.registry.Remove(requestID, lifecycle.StateRejected)
		span.SetStatus(codes.Error, "traffic policy denied")
		span.SetAttributes(attribute.String("relay.policy.rule", policyDecision.MatchedRuleID))
		http.Error(writer, "request denied by traffic policy", http.StatusForbidden)
		return
	}
	var governanceReservation *governance.Reservation
	if g.governance != nil {
		principal := governance.PrincipalFromRequest(request)
		reservation, decision, admissionErr := g.governance.AdmitContext(request.Context(), governance.AdmissionContext{
			Principal: principal, Tenant: request.Header.Get("X-Relay-Lifeline-Tenant-Id"), Model: requestModel(body), Upstream: policyDecision.TargetID,
			RequestID: requestID, Attempt: 1,
		})
		if admissionErr != nil && cfg.Governance.Mode == "enforce" {
			cancel()
			_ = g.registry.Remove(requestID, lifecycle.StateRejected)
			span.RecordError(admissionErr)
			span.SetStatus(codes.Error, "governance denied")
			span.SetAttributes(attribute.String("relay.governance.reason", decision.Reason))
			status := http.StatusTooManyRequests
			if errors.Is(admissionErr, governance.ErrTokenLimit) || errors.Is(admissionErr, governance.ErrCostLimit) {
				status = http.StatusPaymentRequired
			}
			http.Error(writer, governanceMessage(clientLocale, cfg.Localization.FallbackLocale, decision.Reason), status)
			return
		}
		governanceReservation = reservation
		defer func() {
			if governanceReservation != nil {
				if releaseErr := governanceReservation.Release(); releaseErr != nil {
					g.reportGovernanceLedgerFailure(releaseErr, governanceReservation.ID(), "release")
				}
			}
		}()
	}
	watchDownstreamClose(ctx, writer, onDisconnect)
	policyChanged := g.registry.PolicyChanges(requestID)
	if g.repeater != nil {
		g.repeater.RegisterSource(requestID, repeat.Template{
			Method: request.Method, Path: request.URL.RequestURI(), Headers: request.Header,
			Body: body, Streaming: streaming,
		})
		defer g.repeater.UnregisterSource(requestID)
	}
	started := time.Now()
	previousTargetID := ""
	previousTargetDomain := ""
	everWroteRequest := false
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
			if g.monitor != nil {
				g.monitor.RecordCaptureFailure()
			}
			g.addRunLog("warn", "capture.request_failed", "无法捕获请求正文", requestID, 0, 0, map[string]any{"reason": captureErr.Error()})
		} else {
			g.addRunLog("info", "request.received", "收到代理请求", requestID, 0, 0, map[string]any{"method": request.Method, "path": request.URL.Path})
		}
	}
	defer func() {
		resolution := g.registry.UncertainResolution(requestID)
		switch resolution {
		case state.UncertainConfirmSuccess:
			outcome = lifecycle.StateConfirmedSuccess
		case state.UncertainAbandon:
			outcome = lifecycle.StateAbandoned
		default:
			if outcome != lifecycle.StateSuccessful && outcome != lifecycle.StateRejected && (ctx.Err() != nil || clientDisconnected.Load()) {
				outcome = lifecycle.StateCanceled
				event := timeline.Event{Type: "canceled", MessageCode: "timeline.canceled"}
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					outcome = lifecycle.StateExpired
					event = timeline.Event{Type: "expired", MessageCode: "timeline.expired"}
				}
				if persistenceErr := g.registry.RecordEvent(requestID, event); persistenceErr != nil {
					g.reportRequestPersistenceFailure(requestID, "terminal_event", persistenceErr)
				}
				g.addRunLog("info", "request.canceled", "客户端已取消请求", requestID, 0, 0, nil)
			}
		}
		cancel()
		g.risk.ResolveRequest(requestID)
		if persistenceErr := g.registry.Remove(requestID, outcome); persistenceErr != nil {
			g.reportRequestPersistenceFailure(requestID, "finish", persistenceErr)
		}
		if g.monitor != nil {
			g.monitor.RecordFinal(string(outcome))
			g.monitor.RecordEvent(monitoring.Event{Code: terminalEventCode(outcome), RequestID: requestID})
			g.recordLoad()
		}
		if g.captures != nil {
			if captureErr := g.captures.Finish(requestID, string(outcome), finalAttempt); captureErr != nil {
				if g.monitor != nil {
					g.monitor.RecordCaptureFailure()
				}
				g.addRunLog("warn", "capture.finish_failed", "无法完成捕获记录", requestID, finalAttempt, 0, map[string]any{"reason": captureErr.Error()})
			}
		}
		span.SetAttributes(attribute.String("relay.outcome", string(outcome)), attribute.Int("relay.attempts", finalAttempt))
		if outcome == lifecycle.StateSuccessful || outcome == lifecycle.StateConfirmedSuccess {
			span.SetStatus(codes.Ok, "")
		} else {
			span.SetStatus(codes.Error, string(outcome))
		}
	}()

	downstream := startDownstream(writer, streaming, cfg.Stream.HeartbeatInterval.Duration, func() {
		g.addRunLog("debug", "downstream.heartbeat", "已发送下游保活心跳", requestID, 0, 0, nil)
	}, func(error) {
		onDisconnect()
	}, cfg.Server.DownstreamWriteIdleTimeout.Duration)
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
	permitHeld := true
	defer func() {
		if permitHeld {
			g.limiter.Release()
		}
	}()
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
		if err := g.registry.RecordEvent(requestID, timeline.Event{Type: "attempt_started", Attempt: attempt, MessageCode: "timeline.attempt_started"}); err != nil {
			downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, l10n.M("proxy.persistence_unavailable")))
			return
		}
		g.addRunLog("info", "upstream.attempt_started", "开始上游请求", requestID, attempt, 0, nil)
		attemptStarted := time.Now()
		attemptContext, attemptSpan := g.tracer.Start(ctx, "relay.proxy.attempt", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.Int("relay.attempt", attempt)))
		var result attemptResult
		var selectedTarget *upstream.Lease
		var governanceAttemptDecision governance.Decision
		var governanceAttemptErr error
		if g.upstreams != nil {
			selectedTarget, err = g.upstreams.Select(attemptContext, upstream.SelectionContext{
				PreferredTargetID: policyDecision.TargetID, RequirePreferredTarget: policyDecision.Enforced && !policyDecision.Adaptive,
				PreviousTargetID: previousTargetID, PreviousDomain: previousTargetDomain,
				WroteRequest: everWroteRequest, IdempotencyKey: request.Header.Get("Idempotency-Key"), AllowCrossDomain: cfg.Lifecycle.AllowCrossDomainFailover,
			})
			if err != nil {
				message := l10n.M("proxy.no_healthy_upstream")
				if errors.Is(err, upstream.ErrFailoverNotSafe) {
					message = l10n.M("proxy.failover_not_safe")
				}
				result = attemptResult{err: err, phase: lifecycle.PhaseConnect, targetID: previousTargetID, targetDomain: previousTargetDomain, wroteRequest: everWroteRequest, validation: Validation{Message: message}}
			} else {
				if governanceReservation != nil {
					if attempt == 1 {
						governanceAttemptDecision, governanceAttemptErr = governanceReservation.BindUpstreamWithDecision(selectedTarget.Target().ID)
					} else {
						governanceAttemptDecision, governanceAttemptErr = governanceReservation.BeginAttempt(fmt.Sprintf("attempt-%d", attempt), selectedTarget.Target().ID)
					}
				}
				if governanceAttemptErr != nil && cfg.Governance.Mode == "enforce" {
					selectedTarget.Release()
					result = governanceFailureResult(governanceAttemptErr, selectedTarget.Target())
				} else {
					if previousTargetID != "" && selectedTarget.Target().ID != previousTargetID && g.monitor != nil {
						g.monitor.RecordFailover()
					}
					client := selectedTarget.Client()
					if g.client != nil {
						client = g.client
					}
					result = runAttemptForTarget(attemptContext, client, cfg, selectedTarget.Target(), request, body, streaming, &g.cacheBudget)
					selectedTarget.Complete(upstream.Observation{Success: result.validation.Success, WroteRequest: result.wroteRequest, StatusCode: attemptStatus(result), Category: attemptCategory(result), Latency: time.Since(attemptStarted)})
				}
			}
		} else {
			if governanceReservation != nil && attempt > 1 {
				governanceAttemptDecision, governanceAttemptErr = governanceReservation.BeginAttempt(fmt.Sprintf("attempt-%d", attempt))
			}
			client := g.client
			if client == nil {
				client = newHTTPClient(cfg)
			}
			if governanceAttemptErr == nil || cfg.Governance.Mode != "enforce" {
				result = runAttempt(attemptContext, client, cfg, request, body, streaming, &g.cacheBudget)
			} else {
				result = governanceFailureResult(governanceAttemptErr, upstream.Target{})
			}
		}
		if governanceAttemptErr != nil && cfg.Governance.Mode == "enforce" {
			if errors.Is(governanceAttemptErr, governance.ErrLedgerUnavailable) {
				g.reportGovernanceLedgerFailure(governanceAttemptErr, governanceReservation.ID(), "attempt")
			}
			attemptSpan.RecordError(governanceAttemptErr)
			attemptSpan.SetStatus(codes.Error, "governance reservation failed")
			attemptSpan.End()
			if result.buffer != nil {
				result.buffer.Close()
			}
			g.addRunLog("warn", "governance.attempt_rejected", "治理策略拒绝上游尝试", requestID, attempt, 0, map[string]any{"reason": governanceAttemptDecision.Reason})
			downstream.fail(governanceMessage(clientLocale, cfg.Localization.FallbackLocale, governanceDecisionReason(governanceAttemptDecision, governanceAttemptErr)))
			return
		}
		if governanceReservation != nil {
			var recordErr error
			if usage := result.validation.Usage; usage != nil {
				_, recordErr = governanceReservation.Record(fmt.Sprintf("attempt-%d", attempt), governance.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, Known: true})
			} else if result.wroteRequest {
				_, recordErr = governanceReservation.Record(fmt.Sprintf("attempt-%d", attempt), governance.Usage{Known: false})
			}
			if recordErr != nil {
				g.reportGovernanceLedgerFailure(recordErr, governanceReservation.ID(), "settlement")
				if cfg.Governance.Mode == "enforce" {
					if result.buffer != nil {
						result.buffer.Close()
					}
					downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, l10n.M("proxy.persistence_unavailable")))
					return
				}
			}
		}
		attemptSpan.SetAttributes(attribute.Int("http.response.status_code", attemptStatus(result)), attribute.Bool("relay.wrote_request", result.wroteRequest), attribute.String("relay.upstream_target", result.targetID), attribute.String("relay.attempt.phase", string(result.phase)))
		if result.err != nil {
			attemptSpan.RecordError(result.err)
			attemptSpan.SetStatus(codes.Error, result.validation.Message.ID)
		} else if !result.validation.Success {
			attemptSpan.SetStatus(codes.Error, result.validation.Message.ID)
		} else {
			attemptSpan.SetStatus(codes.Ok, "")
		}
		attemptSpan.End()
		previousTargetID, previousTargetDomain = result.targetID, result.targetDomain
		everWroteRequest = everWroteRequest || result.wroteRequest
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
				if g.monitor != nil {
					g.monitor.RecordCaptureFailure()
				}
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
			if governanceReservation != nil {
				if releaseErr := governanceReservation.Release(); releaseErr != nil {
					g.reportGovernanceLedgerFailure(releaseErr, governanceReservation.ID(), "release_before_delivery")
					if result.buffer != nil {
						result.buffer.Close()
					}
					downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, l10n.M("proxy.persistence_unavailable")))
					return
				}
				governanceReservation = nil
			}
			g.dispatchShadow(policyDecision, result.targetID, request, body, cfg)
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
			if persistenceErr := g.registry.RecordEvent(requestID, timeline.Event{Type: "completed", Attempt: attempt, MessageCode: "timeline.completed"}); persistenceErr != nil {
				g.reportRequestPersistenceFailure(requestID, "completed_event", persistenceErr)
			}
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
			if g.monitor != nil {
				g.monitor.RecordUncertain()
			}
			g.registry.UpdateMessage(requestID, lifecycle.StateUncertain, attempt, describeAttempt(result), time.Time{})
			g.registry.RecordEvent(requestID, timeline.Event{
				Type: "uncertain", Attempt: attempt, Category: "duplicate_risk",
				MessageCode: "timeline.uncertain", AttemptPhase: string(result.phase),
				TargetID: result.targetID, TargetDomain: result.targetDomain, WroteRequest: true,
				IdempotencyKeyHash: idempotencyKeyHash(request.Header.Get("Idempotency-Key")), RequestBytes: int64(len(body)), LatencyMilliseconds: time.Since(attemptStarted).Milliseconds(),
			})
			g.notifier.Send(notify.Event{Type: "uncertain_delivery", RequestID: requestID, Attempts: attempt, MessageCode: "notify.uncertain_delivery"})
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
		if err := g.registry.RecordEvent(requestID, timeline.Event{
			Type: "attempt_failed", Attempt: attempt, StatusCode: statusCode,
			Category: category, MessageCode: reason.ID, MessageDetails: reason.Data,
			ErrorDetail: errorDetail, AttemptPhase: string(result.phase),
			TargetID: result.targetID, TargetDomain: result.targetDomain, WroteRequest: result.wroteRequest,
			IdempotencyKeyHash: idempotencyKeyHash(request.Header.Get("Idempotency-Key")), RequestBytes: int64(len(body)), LatencyMilliseconds: time.Since(attemptStarted).Milliseconds(),
		}); err != nil {
			downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, l10n.M("proxy.persistence_unavailable")))
			return
		}
		g.publishAlerts(g.risk.EvaluateAttempt(requestID, attempt, started, statusCode, cfg.Risk))
		g.logger.Warn(g.logText(cfg, "log.upstream_failed"), "event", "upstream.request_failed", "request_id", requestID, "attempt", attempt, "reason_code", reason.ID, "status", statusCode)
		g.addRunLog("warn", "upstream.request_failed", "上游请求失败", requestID, attempt, statusCode, map[string]any{"reasonCode": reason.ID})
		if time.Since(started) >= cfg.Notifications.StalledAfter.Duration && g.registry.MarkNotified(requestID) {
			g.notifier.Send(notify.Event{Type: "stalled", RequestID: requestID, Attempts: attempt, Elapsed: time.Since(started), MessageCode: "notify.stalled"})
		}
		resumeReason, stopReason, err := g.awaitRetry(ctx, requestID, retryNow, policyChanged, result, attempt, started, reason, statusCode)
		if err != nil {
			return
		}
		if stopReason == retryStopExpired {
			g.finishExpiredRetry(requestID, attempt, downstream, clientLocale, cfg, reason)
			outcome = lifecycle.StateExpired
			return
		}
		if stopReason != retryStopNone {
			if stopReason == retryStopAttemptsExhausted {
				g.registry.RecordEvent(requestID, timeline.Event{Type: "retry_attempts_exhausted", Attempt: attempt, MessageCode: "timeline.retry_attempts_exhausted"})
				g.addRunLog("info", "retry.attempts_exhausted", "单请求新增尝试次数已用尽", requestID, attempt, 0, nil)
			}
			downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, reason))
			return
		}
		if permitHeld {
			g.limiter.Release()
			permitHeld = false
		}
		if err := g.limiter.Acquire(ctx, func() (int, int) {
			current := g.store.Get().Queue
			return current.MaxActive, current.MaxWaiting
		}); err != nil {
			outcome = lifecycle.StateRejected
			downstream.fail(g.text(clientLocale, cfg.Localization.FallbackLocale, l10n.M("proxy.queue_full")))
			return
		}
		permitHeld = true
		g.registry.RecordEvent(requestID, timeline.Event{Type: "retry_resumed", Attempt: attempt + 1, MessageCode: resumeReason})
		g.addRunLog("info", "retry.resumed", "重试等待结束", requestID, attempt+1, 0, map[string]any{"reasonCode": resumeReason})
	}
}

func (g *Gateway) evaluateTrafficPolicy(request *http.Request, body []byte, dryRun bool) trafficpolicy.Decision {
	return g.evaluateTrafficPolicyWithID(request, body, dryRun, "")
}

func (g *Gateway) evaluateTrafficPolicyWithID(request *http.Request, body []byte, dryRun bool, requestID string) trafficpolicy.Decision {
	if g.trafficPolicy == nil {
		return trafficpolicy.Decision{DryRun: dryRun, Action: "default", Reason: "unavailable"}
	}
	cfg := g.store.Get()
	sloHealthy, remaining, burnRate := true, 1.0, 0.0
	if cfg.SLO.Enabled && g.monitor != nil {
		slo := g.monitor.SLO(cfg.SLO.Window.Duration, cfg.SLO.AvailabilityTarget, cfg.SLO.RecoveryLatencyTarget.Duration)
		sloHealthy, remaining, burnRate = slo.Healthy, slo.ErrorBudgetRemaining, slo.BurnRate
	}
	signals := make([]trafficpolicy.TargetSignal, 0)
	failureRate := 0.0
	if g.upstreams != nil {
		failureCount, observationCount := 0, 0
		for _, target := range g.upstreams.Snapshot().Targets {
			signals = append(signals, trafficpolicy.TargetSignal{ID: target.Target.ID, CircuitState: string(target.State), Observations: target.FailureCount + target.SuccessCount, LatencyMs: target.LastLatencyMs, ErrorRate: target.ErrorRate, RateLimitRate: target.RateLimitRate, CostMicrosPer1K: target.Target.CostMicrosPer1K, CapabilityScore: target.Target.CapabilityScore})
			failureCount += target.FailureCount
			observationCount += target.FailureCount + target.SuccessCount
		}
		if observationCount > 0 {
			failureRate = float64(failureCount) / float64(observationCount)
		}
	}
	return g.trafficPolicy.Evaluate(trafficpolicy.Input{
		Method: request.Method, Path: request.URL.Path, Model: requestModel(body), Principal: governance.PrincipalFromRequest(request),
		RequestID:      requestID,
		IdempotencyKey: request.Header.Get("Idempotency-Key"), BodyBytes: int64(len(body)), SLOHealthy: sloHealthy,
		ErrorBudgetRemaining: remaining, ErrorBudgetBurnRate: burnRate, Targets: signals,
		FailureRate: failureRate,
	}, dryRun)
}

func (g *Gateway) dispatchShadow(decision trafficpolicy.Decision, primaryTargetID string, source *http.Request, body []byte, cfg config.Config) {
	if g.trafficPolicy == nil || g.upstreams == nil || !decision.ShadowEligible || decision.ShadowTargetID == "" {
		return
	}
	if decision.ShadowTargetID == primaryTargetID {
		g.trafficPolicy.SkipShadow()
		return
	}
	shadowLease, ok := g.trafficPolicy.AcquireShadow(decision)
	if !ok {
		return
	}
	parent := g.lifecycleCtx
	if parent == nil {
		parent = context.Background()
	}
	request := source.Clone(parent)
	request.Header = source.Header.Clone()
	request.URL = cloneURL(source.URL)
	go func() {
		ctx, cancel := context.WithTimeout(parent, min(cfg.Lifecycle.MaxRequestDuration.Duration, 30*time.Second))
		if cfg.Lifecycle.MaxRequestDuration.Duration <= 0 {
			cancel()
			ctx, cancel = context.WithTimeout(parent, 30*time.Second)
		}
		defer cancel()
		targetLease, err := g.upstreams.Select(ctx, upstream.SelectionContext{PreferredTargetID: decision.ShadowTargetID, RequirePreferredTarget: true})
		if err != nil {
			shadowLease.Complete(false)
			return
		}
		success, actualCost := g.sendShadow(ctx, targetLease, request, body, int64(cfg.TrafficPolicy.Shadow.MaxRequestBody))
		// Shadow traffic must not influence the production circuit breaker or
		// adaptive score. Release only the concurrency lease; its outcome is
		// tracked by the policy manager's shadow counters.
		targetLease.Release()
		shadowLease.Complete(success)
		g.trafficPolicy.RecordShadowCost(actualCost)
		if g.monitor != nil {
			outcome := "succeeded"
			if !success {
				outcome = "failed"
			}
			g.monitor.RecordEvent(monitoring.Event{Code: "traffic.shadow", Category: decision.ShadowTargetID, Outcome: outcome})
		}
	}()
}

func (g *Gateway) sendShadow(ctx context.Context, lease *upstream.Lease, source *http.Request, body []byte, responseLimit int64) (bool, int64) {
	targetURL, err := buildTargetURL(lease.Target().BaseURL, source.URL)
	if err != nil {
		return false, 0
	}
	request, err := http.NewRequestWithContext(ctx, source.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		return false, 0
	}
	copyHeaders(request.Header, source.Header)
	request.Header.Set("X-Relay-Lifeline-Shadow", "1")
	response, err := lease.Client().Do(request)
	if err != nil {
		return false, 0
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if int64(len(data)) > responseLimit || readErr != nil {
		return false, 0
	}
	var payload struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(data, &payload)
	total := payload.Usage.TotalTokens
	if total <= 0 {
		total = payload.Usage.InputTokens + payload.Usage.OutputTokens
	}
	if total < 0 {
		total = 0
	}
	cost := total * lease.Target().CostMicrosPer1K / 1000
	return response.StatusCode >= 200 && response.StatusCode < 300, cost
}

func cloneURL(source *url.URL) *url.URL {
	if source == nil {
		return &url.URL{}
	}
	clone := *source
	return &clone
}

func idempotencyKeyHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
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
	available, err := disk.AvailableBytes(directory)
	if err != nil {
		return err
	}
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
	identity, _ := g.registry.Identity(requestID)
	g.runLogs.Add(runlog.Entry{
		Level: level, Event: event, Message: message, RequestID: requestID,
		ClientID: identity.ClientID, TaskID: identity.TaskID,
		Attempt: attempt, StatusCode: statusCode, Fields: fields,
	})
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

func requestModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "unknown"
	}
	if payload.Model == "" {
		return "unknown"
	}
	return payload.Model
}

func governanceMessage(locale, fallback, reason string) string {
	message := l10n.M("proxy.governance_denied", map[string]any{"Reason": reason})
	return l10n.Default.Text(locale, fallback, message)
}

func governanceDecisionReason(decision governance.Decision, err error) string {
	if decision.Reason != "" {
		return decision.Reason
	}
	if errors.Is(err, governance.ErrLedgerUnavailable) {
		return "ledger_unavailable"
	}
	return "budget_limit"
}

func (g *Gateway) reportGovernanceLedgerFailure(err error, reservationID, operation string) {
	if err == nil {
		return
	}
	g.logger.Error("governance ledger operation failed", "event", "governance.ledger_failed", "operation", operation, "reservation_id", reservationID, "error", err)
	g.notifier.Send(notify.Event{Type: "governance_ledger_failed", MessageCode: "notify.governance_ledger_failed"})
}

func (g *Gateway) reportRequestPersistenceFailure(requestID, operation string, err error) {
	if err == nil {
		return
	}
	g.logger.Error("request journal operation failed", "event", "request.persistence_degraded", "operation", operation, "request_id", requestID, "error", err)
	if g.monitor != nil {
		g.monitor.RecordPersistenceFailure()
	}
	g.addRunLog("error", "request.persistence_degraded", "请求终态持久化降级", requestID, 0, 0, map[string]any{"operation": operation, "reason": err.Error()})
	if g.monitor != nil {
		g.monitor.RecordEvent(monitoring.Event{Code: "request.persistence_degraded", Category: operation, RequestID: requestID, Outcome: "degraded"})
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

func governanceFailureResult(err error, target upstream.Target) attemptResult {
	return attemptResult{
		err:          err,
		phase:        lifecycle.PhasePrepare,
		targetID:     target.ID,
		targetDomain: target.IdempotencyDomain,
		validation:   Validation{Message: l10n.M("proxy.persistence_unavailable")},
	}
}

func terminalEventCode(outcome lifecycle.State) string {
	switch outcome {
	case lifecycle.StateSuccessful:
		return "request.succeeded"
	case lifecycle.StateConfirmedSuccess:
		return "request.uncertain_confirmed"
	case lifecycle.StateAbandoned:
		return "request.uncertain_abandoned"
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
	return fmt.Sprintf("Relay-Lifeline -> %s", sanitize.URL(g.store.Get().Upstream.BaseURL))
}

func egressPolicyForConfig(cfg config.Config) egress.Policy {
	hosts := append([]string(nil), cfg.Egress.AllowedHosts...)
	add := func(raw string) {
		parsed, err := url.Parse(raw)
		if err == nil && parsed.Hostname() != "" {
			hosts = append(hosts, parsed.Hostname())
		}
	}
	add(cfg.Upstream.BaseURL)
	for _, target := range cfg.Upstreams.Targets {
		add(target.BaseURL)
	}
	return egress.Policy{DenyPrivateNetworks: cfg.Egress.DenyPrivateNetworks, AllowedHosts: hosts}
}
