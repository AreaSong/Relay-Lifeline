package proxy

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/areasong/relay-lifeline/internal/config"
)

func TestHTTPClientNegotiatesAndDecodesGzip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.Header.Get("Accept-Encoding"), "gzip") {
			t.Errorf("上游未收到 Go Transport 管理的 gzip 协商: %q", request.Header.Get("Accept-Encoding"))
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(writer)
		_, _ = io.WriteString(compressed, `{"id":"response","status":"completed"}`)
		_ = compressed.Close()
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Upstream.BaseURL = upstream.URL
	source := httptest.NewRequest(http.MethodPost, "http://relay.local/v1/responses", strings.NewReader(`{"input":"test"}`))
	source.Header.Set("Accept-Encoding", "br")
	budget := &cacheBudget{}
	result := runAttempt(context.Background(), newHTTPClient(cfg), cfg, source, []byte(`{"input":"test"}`), false, budget)
	if result.buffer != nil {
		defer result.buffer.Close()
	}
	if !result.validation.Success || result.err != nil {
		t.Fatalf("gzip 响应未成功解压校验: validation=%+v err=%v", result.validation, result.err)
	}
	if result.response == nil || !result.response.Uncompressed || result.response.Header.Get("Content-Encoding") != "" {
		t.Fatalf("Transport 未标记自动解压: response=%+v", result.response)
	}
	reader, err := result.buffer.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != `{"id":"response","status":"completed"}` {
		t.Fatalf("解压后的缓存正文异常: %q err=%v", body, err)
	}
}

func TestCopyHeadersDropsClientCompressionNegotiation(t *testing.T) {
	source := http.Header{"Accept-Encoding": []string{"br"}, "Authorization": []string{"Bearer token"}}
	destination := http.Header{}
	copyHeaders(destination, source)
	if destination.Get("Accept-Encoding") != "" || destination.Get("Authorization") == "" {
		t.Fatalf("请求 Header 过滤异常: %#v", destination)
	}
}

func TestRunAttemptStopsAtDecodedResponseLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"response","status":"completed"}`)
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Upstream.BaseURL = upstream.URL
	cfg.Stream.MemoryLimit = 8
	cfg.Stream.MaxResponseBody = 16
	cfg.Stream.MaxTotalCache = 32
	cfg.Risk.MinimumFreeDisk = 0
	source := httptest.NewRequest(http.MethodPost, "http://relay.local/v1/responses", strings.NewReader(`{}`))
	budget := &cacheBudget{}
	result := runAttempt(context.Background(), newHTTPClient(cfg), cfg, source, []byte(`{}`), false, budget)
	if result.buffer != nil {
		defer result.buffer.Close()
	}
	if !errors.Is(result.err, errResponseBodyTooLarge) || !result.validation.Permanent || shouldRetry(cfg, result) {
		t.Fatalf("单响应上限未形成不可重试结果: validation=%+v err=%v", result.validation, result.err)
	}
}
