package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

type attemptResult struct {
	response   *http.Response
	buffer     *ReplayBuffer
	validation Validation
	err        error
}

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
		return attemptResult{err: err, validation: Validation{Reason: "上游 URL 无效"}}
	}
	request, err := http.NewRequestWithContext(ctx, source.Method, target, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: err, validation: Validation{Reason: "创建上游请求失败"}}
	}
	copyHeaders(request.Header, source.Header)
	request.Host = request.URL.Host
	response, err := client.Do(request)
	if err != nil {
		return attemptResult{err: err, validation: Validation{Reason: classifyTransportError(err)}}
	}
	defer response.Body.Close()
	buffer := NewReplayBuffer(int64(cfg.Stream.MemoryLimit), cfg.Stream.TempDir)
	if _, err := io.Copy(buffer, response.Body); err != nil {
		buffer.Close()
		return attemptResult{response: response, err: err, validation: Validation{Reason: "读取上游响应中断"}}
	}
	return attemptResult{response: response, buffer: buffer, validation: validateResponse(response, buffer, streaming)}
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

func classifyTransportError(err error) string {
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return "上游连接超时"
	}
	return "上游连接失败"
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

func describeAttempt(result attemptResult) string {
	if result.validation.Reason != "" {
		return result.validation.Reason
	}
	if result.err != nil {
		return fmt.Sprintf("请求失败: %T", result.err)
	}
	return "未知错误"
}
