package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/repeat"
)

func (g *Gateway) ExecuteRepeat(ctx context.Context, template repeat.Template, idempotency, executionID string) repeat.Execution {
	completed := time.Now()
	result := repeat.Execution{ID: executionID, Completed: completed}
	cfg := g.store.Get()
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
	return g.runRepeatAttempt(ctx, cfg, template, idempotency, executionID)
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
	started := time.Now()
	g.recordRepeatStart(executionID)
	attempt := runAttempt(ctx, g.client, cfg, source, template.Body, template.Streaming)
	g.captureRepeat(executionID, template, attempt, started)
	result := repeat.Execution{ID: executionID, Success: attempt.validation.Success, StatusCode: attemptStatus(attempt), Completed: time.Now()}
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
