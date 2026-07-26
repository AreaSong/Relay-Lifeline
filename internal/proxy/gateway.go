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

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/state"
)

type Gateway struct {
	store      *config.Store
	registry   *state.Registry
	controller *state.Controller
	notifier   *notify.Notifier
	logger     *slog.Logger
	client     *http.Client
	limiter    Limiter
	retryGate  RetryGate
	randomMu   sync.Mutex
	random     *rand.Rand
}

func NewGateway(store *config.Store, registry *state.Registry, controller *state.Controller, notifier *notify.Notifier, logger *slog.Logger) *Gateway {
	return &Gateway{
		store: store, registry: registry, controller: controller, notifier: notifier, logger: logger,
		client: newHTTPClient(store.Get()), random: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (g *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	cfg := g.store.Get()
	body, err := readRequestBody(writer, request, int64(cfg.Server.MaxRequestBody))
	if err != nil {
		return
	}
	streaming := requestWantsStream(body, request.Header)
	ctx, cancel := context.WithCancel(request.Context())
	requestID, retryNow := g.registry.Add(request.Method, request.URL.Path, cancel)
	succeeded := false
	defer func() {
		cancel()
		g.registry.Remove(requestID, succeeded)
	}()

	downstream := startDownstream(writer, streaming, cfg.Stream.HeartbeatInterval.Duration)
	defer downstream.stopHeartbeat()
	if err := g.limiter.Acquire(ctx, func() (int, int) {
		current := g.store.Get().Queue
		return current.MaxActive, current.MaxWaiting
	}); err != nil {
		downstream.fail(err.Error())
		return
	}
	defer g.limiter.Release()

	started := time.Now()
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
		g.registry.Update(requestID, "requesting", attempt, "", time.Time{})
		result := runAttempt(ctx, g.client, cfg, request, body, streaming)
		if result.validation.Success {
			g.registry.SetUpstream(true, "")
			if err := downstream.deliver(result.buffer); err != nil {
				result.buffer.Close()
				return
			}
			result.buffer.Close()
			succeeded = true
			g.registry.Update(requestID, "completed", attempt, "", time.Time{})
			if g.registry.WasNotified(requestID) && cfg.Notifications.NotifyOnRecovery {
				g.notifier.Send(notify.Event{Type: "recovered", RequestID: requestID, Attempts: attempt, Elapsed: time.Since(started), Message: "上游已恢复"})
			}
			g.logger.Info("请求成功", "request_id", requestID, "attempt", attempt, "elapsed", time.Since(started).Round(time.Millisecond).String())
			return
		}
		if result.buffer != nil {
			result.buffer.Close()
		}
		g.registry.RecordFailure()
		reason := describeAttempt(result)
		g.registry.SetUpstream(false, reason)
		g.logger.Warn("上游请求失败", "request_id", requestID, "attempt", attempt, "reason", reason)
		if !shouldRetry(cfg, result) || cfg.Retry.MaxAttempts > 0 && attempt >= cfg.Retry.MaxAttempts {
			downstream.fail(reason)
			return
		}

		delay := g.retryDelay(cfg, result.response)
		nextRetry := time.Now().Add(delay)
		g.registry.Update(requestID, "waiting", attempt, reason, nextRetry)
		if time.Since(started) >= cfg.Notifications.StalledAfter.Duration && g.registry.MarkNotified(requestID) {
			g.notifier.Send(notify.Event{Type: "stalled", RequestID: requestID, Attempts: attempt, Elapsed: time.Since(started), Message: reason})
		}
		if err := waitForRetry(ctx, retryNow, delay); err != nil {
			return
		}
	}
}

func readRequestBody(writer http.ResponseWriter, request *http.Request, limit int64) ([]byte, error) {
	defer request.Body.Close()
	reader := io.LimitReader(request.Body, limit+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		http.Error(writer, "无法读取请求", http.StatusBadRequest)
		return nil, err
	}
	if int64(len(body)) > limit {
		http.Error(writer, "请求体过大", http.StatusRequestEntityTooLarge)
		return nil, errors.New("请求体过大")
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

func waitForRetry(ctx context.Context, retryNow <-chan struct{}, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-retryNow:
		return nil
	case <-timer.C:
		return nil
	}
}

func (g *Gateway) Registry() *state.Registry     { return g.registry }
func (g *Gateway) Controller() *state.Controller { return g.controller }

func (g *Gateway) String() string {
	return fmt.Sprintf("Relay Lifeline -> %s", g.store.Get().Upstream.BaseURL)
}
