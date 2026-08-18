package proxy

import (
	"context"
	"net/http"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

type retryStopReason string

const (
	retryStopNone              retryStopReason = ""
	retryStopDenied            retryStopReason = "denied"
	retryStopExpired           retryStopReason = "expired"
	retryStopAttemptsExhausted retryStopReason = "attempts_exhausted"
)

func (g *Gateway) awaitRetry(
	ctx context.Context,
	requestID string,
	retryNow, policyChanged <-chan struct{},
	result attemptResult,
	attempt int,
	started time.Time,
	reason l10n.Message,
	statusCode int,
) (string, retryStopReason, error) {
	rescheduled := false
	for {
		cfg := g.store.Get()
		policy, hasPolicy := g.registry.ActivateRetryPolicy(requestID, attempt)
		if stop := retryPolicyStop(cfg, result, attempt, policy, hasPolicy); stop != retryStopNone {
			return "", stop, nil
		}
		delay := g.retryDelay(cfg, result.response)
		mode := state.RetryScheduleInherit
		if hasPolicy {
			mode = policy.Schedule.Mode
			delay = g.policyDelay(policy, cfg, result.response, attempt)
			remaining := time.Until(policy.Deadline)
			if remaining <= 0 {
				return "", retryStopExpired, nil
			}
			delay = min(delay, remaining)
		}

		eventType := "waiting"
		messageCode := "timeline.waiting"
		if rescheduled {
			eventType = "retry_rescheduled"
			messageCode = "timeline.retry_rescheduled"
		}
		g.addRunLog("info", "retry.scheduled", "已安排再次请求", requestID, attempt, statusCode, map[string]any{
			"waitMilliseconds": delay.Milliseconds(), "scheduleMode": mode,
		})
		g.registry.UpdateMessage(requestID, "waiting", attempt, reason, time.Now().Add(delay))
		g.recordLoad()
		g.registry.RecordEvent(requestID, timeline.Event{
			Type: eventType, Attempt: attempt, MessageCode: messageCode,
			WaitMilliseconds: delay.Milliseconds(), MessageDetails: map[string]any{"Mode": string(mode)},
		})

		resumeReason, err := waitForRetry(
			ctx, retryNow, policyChanged, delay,
			cfg.Risk.WarningAfter.Duration-time.Since(started),
			func() { g.publishAlerts(g.risk.EvaluateLongRunning(requestID, attempt, started, g.store.Get().Risk)) },
		)
		if err != nil {
			return "", retryStopNone, err
		}
		if resumeReason == "policy_updated" {
			rescheduled = true
			continue
		}
		if current, ok := g.registry.RetryPolicy(requestID); ok && current.Active() && !time.Now().Before(current.Deadline) {
			return "", retryStopExpired, nil
		}
		return resumeReason, retryStopNone, nil
	}
}

func retryPolicyStop(cfg config.Config, result attemptResult, attempt int, policy state.RetryPolicy, active bool) retryStopReason {
	if active {
		if !time.Now().Before(policy.Deadline) {
			return retryStopExpired
		}
		if policy.Exhausted(attempt) {
			return retryStopAttemptsExhausted
		}
		return retryStopNone
	}
	if !shouldRetry(cfg, result) || cfg.Retry.MaxAttempts > 0 && attempt >= cfg.Retry.MaxAttempts {
		return retryStopDenied
	}
	return retryStopNone
}

func (g *Gateway) policyDelay(policy state.RetryPolicy, cfg config.Config, response *http.Response, attempt int) time.Duration {
	var delay time.Duration
	switch policy.Schedule.Mode {
	case state.RetryScheduleImmediate:
		delay = 0
	case state.RetryScheduleFixed:
		delay = policy.Schedule.Interval
	case state.RetryScheduleRandom:
		delay = g.randomDelay(policy.Schedule.Minimum, policy.Schedule.Maximum)
	case state.RetryScheduleExponential:
		delay = exponentialDelay(policy.Schedule.Base, policy.Schedule.Maximum, max(attempt-policy.BaselineAttempt, 0))
		delay = g.randomDelay(max(policy.Schedule.Base, delay/2), delay)
	default:
		delay = g.randomDelay(cfg.Retry.MinInterval.Duration, cfg.Retry.MaxInterval.Duration)
	}
	if policy.HonorRetryAfter {
		delay = max(delay, retryAfter(response))
	}
	return delay
}

func exponentialDelay(base, maximum time.Duration, index int) time.Duration {
	delay := base
	for step := 0; step < index && delay < maximum; step++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	return min(delay, maximum)
}

func (g *Gateway) retryDelay(cfg config.Config, response *http.Response) time.Duration {
	delay := g.randomDelay(cfg.Retry.MinInterval.Duration, cfg.Retry.MaxInterval.Duration)
	if cfg.Retry.HonorRetryAfter {
		delay = max(delay, retryAfter(response))
	}
	return delay
}

func (g *Gateway) randomDelay(minimum, maximum time.Duration) time.Duration {
	if maximum <= minimum {
		return minimum
	}
	g.randomMu.Lock()
	delay := minimum + time.Duration(g.random.Int63n(int64(maximum-minimum)+1))
	g.randomMu.Unlock()
	return delay
}

func waitForRetry(
	ctx context.Context,
	retryNow, policyChanged <-chan struct{},
	delay, riskAfter time.Duration,
	onRisk func(),
) (string, error) {
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
		case <-policyChanged:
			return "policy_updated", nil
		case <-timer.C:
			return "timeline.retry_timer", nil
		case <-riskChannel:
			onRisk()
			riskChannel = nil
		}
	}
}
