package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
	"github.com/areasong/relay-lifeline/internal/telemetry"
	"github.com/areasong/relay-lifeline/internal/upstream"
)

type attemptResult struct {
	response     *http.Response
	buffer       *ReplayBuffer
	validation   Validation
	err          error
	phase        lifecycle.AttemptPhase
	wroteRequest bool
	targetID     string
	targetDomain string
}

var errResponseBodyIdleTimeout = errors.New("upstream response body idle timeout")

func newHTTPClient(cfg config.Config) *http.Client {
	dialer := &net.Dialer{Timeout: cfg.Upstream.ConnectTimeout.Duration, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment, DialContext: dialer.DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 32,
		IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: cfg.Upstream.ResponseHeaderTimeout.Duration,
	}
	return &http.Client{Transport: transport}
}

func runAttempt(ctx context.Context, client *http.Client, cfg config.Config, source *http.Request, body []byte, streaming bool, budget *cacheBudget) attemptResult {
	return runAttemptForBaseURL(ctx, client, cfg, cfg.Upstream.BaseURL, source, body, streaming, budget)
}

func runAttemptForTarget(ctx context.Context, client *http.Client, cfg config.Config, target upstream.Target, source *http.Request, body []byte, streaming bool, budget *cacheBudget) attemptResult {
	result := runAttemptForBaseURL(ctx, client, cfg, target.BaseURL, source, body, streaming, budget)
	result.targetID = target.ID
	result.targetDomain = target.IdempotencyDomain
	return result
}

func runAttemptForBaseURL(ctx context.Context, client *http.Client, cfg config.Config, baseURL string, source *http.Request, body []byte, streaming bool, budget *cacheBudget) attemptResult {
	target, err := buildTargetURL(baseURL, source.URL)
	if err != nil {
		return attemptResult{err: err, phase: lifecycle.PhasePrepare, validation: Validation{Message: l10n.M("proxy.upstream_url_invalid")}}
	}
	ctx, span := telemetry.Tracer("relay-lifeline/proxy").Start(ctx, "relay.upstream.http", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("http.request.method", source.Method), attribute.String("url.path", source.URL.Path)))
	defer span.End()
	request, err := http.NewRequestWithContext(ctx, source.Method, target, bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request creation failed")
		return attemptResult{err: err, phase: lifecycle.PhasePrepare, validation: Validation{Message: l10n.M("proxy.request_create_failed")}}
	}
	wroteRequest := false
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wroteRequest = true }}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	copyHeaders(request.Header, source.Header)
	telemetry.Inject(request.Context(), propagation.HeaderCarrier(request.Header))
	request.Host = request.URL.Host
	span.SetAttributes(attribute.String("server.address", request.URL.Hostname()))
	response, err := client.Do(request)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "transport failure")
		phase := lifecycle.PhaseConnect
		if wroteRequest {
			phase = lifecycle.PhaseResponseHeaders
		}
		return attemptResult{err: err, phase: phase, wroteRequest: wroteRequest, validation: Validation{Message: classifyTransportError(err)}}
	}
	defer response.Body.Close()
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	buffer := newLimitedReplayBuffer(
		int64(cfg.Stream.MemoryLimit), int64(cfg.Stream.MaxResponseBody), int64(cfg.Stream.MaxTotalCache),
		int64(cfg.Risk.MinimumFreeDisk), cfg.Stream.TempDir, budget,
	)
	if err := copyResponseBody(ctx, buffer, response.Body, cfg.Upstream.ResponseBodyIdleTimeout.Duration); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "response body failure")
		message := l10n.M("proxy.response_interrupted")
		permanent := false
		switch {
		case errors.Is(err, errResponseBodyIdleTimeout):
			message = l10n.M("proxy.response_body_timeout")
		case errors.Is(err, errResponseBodyTooLarge):
			message = l10n.M("proxy.response_body_too_large", map[string]any{"Limit": config.FormatByteSize(int64(cfg.Stream.MaxResponseBody))})
			permanent = true
		case errors.Is(err, errCacheBudgetExceeded):
			message = l10n.M("proxy.cache_budget_exceeded", map[string]any{"Limit": config.FormatByteSize(int64(cfg.Stream.MaxTotalCache))})
			permanent = true
		case errors.Is(err, errCacheDiskSpace):
			message = l10n.M("proxy.cache_disk_low")
			permanent = true
		}
		return attemptResult{response: response, buffer: buffer, err: err, phase: lifecycle.PhaseResponseBody, wroteRequest: wroteRequest, validation: Validation{Message: message, Permanent: permanent}}
	}
	validation := validateResponse(response, buffer, responseProfile(source.URL.Path), streaming)
	if validation.Success {
		span.SetStatus(codes.Ok, "")
	} else {
		span.SetStatus(codes.Error, validation.Message.ID)
	}
	return attemptResult{response: response, buffer: buffer, phase: lifecycle.PhaseProtocol, wroteRequest: wroteRequest, validation: validation}
}

type activityWriter struct {
	io.Writer
	activity chan<- struct{}
}

func (w activityWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if n > 0 {
		select {
		case w.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}

func copyResponseBody(ctx context.Context, destination io.Writer, body io.ReadCloser, idleTimeout time.Duration) error {
	activity := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(activityWriter{Writer: destination, activity: activity}, body)
		done <- err
	}()
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-activity:
			resetTimer(timer, idleTimeout)
		case <-timer.C:
			_ = body.Close()
			<-done
			return errResponseBodyIdleTimeout
		case <-ctx.Done():
			_ = body.Close()
			<-done
			return ctx.Err()
		}
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func buildTargetURL(baseURL string, incoming *url.URL) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	base.Path = path.Join(strings.TrimSuffix(base.Path, "/"), incoming.Path)
	if strings.HasSuffix(incoming.Path, "/") && !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	base.RawQuery = incoming.RawQuery
	return base.String(), nil
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		if isHopByHopHeader(key) || isClientIdentityHeader(key) || isTracePropagationHeader(key) || http.CanonicalHeaderKey(key) == "Accept-Encoding" {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
	destination.Set("X-Relay-Lifeline", "1")
}

func isTracePropagationHeader(key string) bool {
	switch strings.ToLower(key) {
	case "traceparent", "tracestate", "baggage":
		return true
	default:
		return false
	}
}

func isHopByHopHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func classifyTransportError(err error) l10n.Message {
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return l10n.M("proxy.connection_timeout")
	}
	return l10n.M("proxy.connection_failed")
}

func retryAfter(response *http.Response, caps ...time.Duration) time.Duration {
	if response == nil {
		return 0
	}
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if parsed, err := time.ParseDuration(value + "s"); err == nil {
		return capRetryAfter(parsed, caps...)
	}
	if parsed, err := http.ParseTime(value); err == nil {
		return capRetryAfter(max(time.Until(parsed), 0), caps...)
	}
	return 0
}

func capRetryAfter(value time.Duration, caps ...time.Duration) time.Duration {
	if len(caps) > 0 && caps[0] > 0 && value > caps[0] {
		return caps[0]
	}
	return value
}

func shouldRetry(cfg config.Config, result attemptResult) bool {
	if !cfg.Retry.Enabled || result.validation.Permanent {
		return false
	}
	if cfg.Retry.Mode == "all-errors" {
		return !result.validation.Success
	}
	if result.err != nil || result.response == nil {
		return true
	}
	status := result.response.StatusCode
	return status == http.StatusRequestTimeout || status == http.StatusConflict || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= 500
}

func describeAttempt(result attemptResult) l10n.Message {
	if result.validation.Message.ID != "" {
		return result.validation.Message
	}
	return l10n.M("proxy.unknown_error")
}
