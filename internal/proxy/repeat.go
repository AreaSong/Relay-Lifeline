package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/governance"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/upstream"
)

func (g *Gateway) ExecuteRepeat(ctx context.Context, template repeat.Template, idempotency, executionID string) repeat.Execution {
	ctx, span := g.tracer.Start(ctx, "relay.repeat.execute", trace.WithAttributes(attribute.String("relay.repeat.execution_id", executionID)))
	completed := time.Now()
	result := repeat.Execution{ID: executionID, Completed: completed}
	defer func() {
		span.SetAttributes(attribute.Bool("relay.repeat.success", result.Success), attribute.Int("http.response.status_code", result.StatusCode))
		if result.Success {
			span.SetStatus(codes.Ok, "")
		} else {
			span.SetStatus(codes.Error, result.ErrorCode)
		}
		span.End()
	}()
	cfg := g.store.Get()
	if !g.controller.Accepting() {
		result.ErrorCode = "proxy.maintenance"
		return result
	}
	if err := g.resourceCheck(cfg); err != nil {
		result.ErrorCode = "proxy.resource_protected"
		return result
	}
	if err := g.controller.Wait(ctx); err != nil {
		result.ErrorCode = "proxy.request_canceled"
		return result
	}
	if err := g.limiter.Acquire(ctx, func() (int, int) {
		queue := g.store.Get().Queue
		return queue.MaxActive, queue.MaxWaiting
	}); err != nil {
		result.ErrorCode = "proxy.queue_full"
		return result
	}
	defer g.limiter.Release()
	result = g.runRepeatAttempt(ctx, cfg, template, idempotency, executionID)
	return result
}

func (g *Gateway) runRepeatAttempt(ctx context.Context, cfg config.Config, template repeat.Template, idempotency, executionID string) repeat.Execution {
	source, err := http.NewRequestWithContext(ctx, template.Method, "http://relay.local"+template.Path, bytes.NewReader(template.Body))
	if err != nil {
		return repeat.Execution{ID: executionID, ErrorCode: "proxy.request_create_failed", Completed: time.Now()}
	}
	source.Header = template.Headers.Clone()
	if idempotency == "regenerate" {
		prepareIdempotencyKey(source.Header, config.LifecycleConfig{GenerateIdempotencyKey: true})
	}
	var reservation *governance.Reservation
	if g.governance != nil {
		var decision governance.Decision
		reservation, decision, err = g.governance.AdmitWithContext(ctx, executionID, governance.AdmissionContext{
			Principal: governance.PrincipalFromRequest(source), Tenant: source.Header.Get("X-Relay-Lifeline-Tenant-Id"), Model: requestModel(template.Body),
			RequestID: executionID, Attempt: 1,
		})
		if err != nil && cfg.Governance.Mode == "enforce" {
			return repeat.Execution{ID: executionID, ErrorCode: "governance." + decision.Reason, Completed: time.Now()}
		}
		if reservation != nil {
			defer func() {
				if releaseErr := reservation.Release(); releaseErr != nil {
					g.reportGovernanceLedgerFailure(releaseErr, reservation.ID(), "release")
				}
			}()
		}
	}
	started := time.Now()
	g.recordRepeatStart(executionID)
	attempt := attemptResult{}
	if g.upstreams != nil {
		lease, selectErr := g.upstreams.Select(ctx, upstream.SelectionContext{})
		if selectErr != nil {
			attempt = attemptResult{err: selectErr, phase: lifecycle.PhaseConnect, validation: Validation{Message: l10n.M("proxy.no_healthy_upstream")}}
		} else {
			var bindDecision governance.Decision
			var bindErr error
			if reservation != nil {
				bindDecision, bindErr = reservation.BindUpstreamWithDecision(lease.Target().ID)
			}
			if bindErr != nil && cfg.Governance.Mode == "enforce" {
				lease.Release()
				if errors.Is(bindErr, governance.ErrLedgerUnavailable) {
					g.reportGovernanceLedgerFailure(bindErr, reservation.ID(), "repeat_bind")
				}
				attempt = governanceFailureResult(bindErr, lease.Target())
				attempt.validation.Message = l10n.M("proxy.governance_denied", map[string]any{"Reason": governanceDecisionReason(bindDecision, bindErr)})
			} else {
				client := lease.Client()
				if g.client != nil {
					client = g.client
				}
				attempt = runAttemptForTarget(ctx, client, cfg, lease.Target(), source, template.Body, template.Streaming, &g.cacheBudget)
				lease.Complete(upstream.Observation{Success: attempt.validation.Success, WroteRequest: attempt.wroteRequest, StatusCode: attemptStatus(attempt), Category: attemptCategory(attempt), Latency: time.Since(started)})
			}
		}
	} else {
		client := g.client
		if client == nil {
			client = newHTTPClient(cfg)
		}
		attempt = runAttempt(ctx, client, cfg, source, template.Body, template.Streaming, &g.cacheBudget)
	}
	if reservation != nil {
		var recordErr error
		if usage := attempt.validation.Usage; usage != nil {
			_, recordErr = reservation.Record("attempt-1", governance.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, Known: true})
		} else if attempt.wroteRequest {
			_, recordErr = reservation.Record("attempt-1", governance.Usage{Known: false})
		}
		if recordErr != nil {
			g.reportGovernanceLedgerFailure(recordErr, reservation.ID(), "settlement")
			if cfg.Governance.Mode == "enforce" {
				attempt.err = recordErr
				attempt.validation.Success = false
				attempt.validation.Message = l10n.M("proxy.persistence_unavailable")
			}
		}
	}
	if attempt.validation.Success && reservation != nil {
		if releaseErr := reservation.Release(); releaseErr != nil {
			g.reportGovernanceLedgerFailure(releaseErr, reservation.ID(), "release_before_repeat_success")
			attempt.err = releaseErr
			attempt.validation.Success = false
			attempt.validation.Message = l10n.M("proxy.persistence_unavailable")
		} else {
			reservation = nil
		}
	}
	g.captureRepeat(executionID, template, attempt, started)
	completed := time.Now()
	result := repeat.Execution{
		ID: executionID, Success: attempt.validation.Success, StatusCode: attemptStatus(attempt), Completed: completed,
		DurationMilliseconds: completed.Sub(started).Milliseconds(),
	}
	if attempt.validation.Usage != nil {
		result.UsageTokens = attempt.validation.Usage.TotalTokens
		result.UsageAvailable = true
	}
	if !result.Success {
		result.ErrorCode = describeAttempt(attempt).ID
	}
	if attempt.buffer != nil {
		attempt.buffer.Close()
	}
	g.recordRepeatFinish(result)
	return result
}

func (g *Gateway) captureRepeat(id string, template repeat.Template, result attemptResult, started time.Time) {
	if g.captures == nil {
		return
	}
	if _, err := g.captures.BeginRequest(id, template.Method, template.Path, template.Headers, template.Body); err != nil {
		g.addRunLog("warn", "capture.request_failed", "无法捕获持续任务请求", id, 1, 0, map[string]any{"reason": err.Error()})
		return
	}
	var body io.Reader
	if result.buffer != nil {
		body, _ = result.buffer.Reader()
	}
	var headers http.Header
	if result.response != nil {
		headers = result.response.Header
	}
	_ = g.captures.RecordAttempt(id, 1, attemptStatus(result), headers, body, result.err, started)
	if closer, ok := body.(io.Closer); ok {
		_ = closer.Close()
	}
	state := string(lifecycle.StateFailed)
	if result.validation.Success {
		state = string(lifecycle.StateSuccessful)
	}
	_ = g.captures.Finish(id, state, 1)
}

func (g *Gateway) recordRepeatStart(id string) {
	g.addRunLog("info", "repeat.execution_started", "持续任务开始一轮执行", id, 1, 0, nil)
	if g.monitor == nil {
		return
	}
	g.monitor.RecordReceived()
	g.monitor.RecordAttempt()
	g.monitor.RecordEvent(monitoring.Event{Code: "repeat.execution_started", RequestID: id, Attempt: 1})
}

func (g *Gateway) recordRepeatFinish(result repeat.Execution) {
	state := string(lifecycle.StateFailed)
	level, event, message := "warn", "repeat.execution_failed", "持续任务执行失败"
	if result.Success {
		state = string(lifecycle.StateSuccessful)
		level, event, message = "info", "repeat.execution_succeeded", "持续任务执行成功"
	}
	g.addRunLog(level, event, message, result.ID, 1, result.StatusCode, map[string]any{"reasonCode": result.ErrorCode})
	if g.monitor != nil {
		g.monitor.RecordFinal(state)
		g.monitor.RecordEvent(monitoring.Event{Code: event, RequestID: result.ID, StatusCode: result.StatusCode, Attempt: 1})
	}
}
