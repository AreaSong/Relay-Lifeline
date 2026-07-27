package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
)

type attemptResult struct {
	response   *http.Response
	buffer     *ReplayBuffer
	validation Validation
	err        error
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

func runAttempt(ctx context.Context, client *http.Client, cfg config.Config, source *http.Request, body []byte, streaming bool) attemptResult {
	target, err := buildTargetURL(cfg.Upstream.BaseURL, source.URL)
	if err != nil {
		return attemptResult{err: err, validation: Validation{Message: l10n.M("proxy.upstream_url_invalid")}}
	}
	request, err := http.NewRequestWithContext(ctx, source.Method, target, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: err, validation: Validation{Message: l10n.M("proxy.request_create_failed")}}
	}
	copyHeaders(request.Header, source.Header)
	request.Host = request.URL.Host
	response, err := client.Do(request)
	if err != nil {
		return attemptResult{err: err, validation: Validation{Message: classifyTransportError(err)}}
	}
	defer response.Body.Close()
	buffer := NewReplayBuffer(int64(cfg.Stream.MemoryLimit), cfg.Stream.TempDir)
	if err := copyResponseBody(ctx, buffer, response.Body, cfg.Upstream.ResponseBodyIdleTimeout.Duration); err != nil {
		message := l10n.M("proxy.response_interrupted")
		if errors.Is(err, errResponseBodyIdleTimeout) {
			message = l10n.M("proxy.response_body_timeout")
		}
		return attemptResult{response: response, buffer: buffer, err: err, validation: Validation{Message: message}}
	}
	return attemptResult{response: response, buffer: buffer, validation: validateResponse(response, buffer, streaming)}
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
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
	destination.Set("X-Relay-Lifeline", "1")
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

func retryAfter(response *http.Response) time.Duration {
	if response == nil {
		return 0
	}
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if parsed, err := time.ParseDuration(value + "s"); err == nil {
		return parsed
	}
	if parsed, err := http.ParseTime(value); err == nil {
		return max(time.Until(parsed), 0)
	}
	return 0
}

func shouldRetry(cfg config.Config, result attemptResult) bool {
	if !cfg.Retry.Enabled {
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
