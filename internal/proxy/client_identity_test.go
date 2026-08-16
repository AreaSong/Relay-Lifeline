package proxy

import (
	"net/http"
	"strings"
	"testing"
)

func TestClientIdentityAcceptsOnlyBoundedDeclaredHeaders(t *testing.T) {
	headers := http.Header{
		"X-Codex-Session-Id": []string{"session-123"},
		"X-Codex-Thread-Id":  []string{"01a006c0-ce62-7eb1-b78b-dca9a87e0b66"},
	}
	identity := clientIdentityFromHeaders(headers)
	if identity.ClientID != "session-123" || identity.TaskID != "01a006c0-ce62-7eb1-b78b-dca9a87e0b66" {
		t.Fatalf("Codex 标识提取异常: %+v", identity)
	}

	headers.Set("X-Relay-Lifeline-Client-ID", "client with spaces")
	headers.Set("X-Relay-Lifeline-Task-ID", strings.Repeat("a", maxClientIdentityLength+1))
	identity = clientIdentityFromHeaders(headers)
	if identity.ClientID != "session-123" || identity.TaskID != "01a006c0-ce62-7eb1-b78b-dca9a87e0b66" {
		t.Fatalf("无效显式标识不应覆盖有效 Codex 标识: %+v", identity)
	}
}

func TestCopyHeadersKeepsClientIdentityLocal(t *testing.T) {
	source := http.Header{
		"Authorization":              []string{"Bearer safe-for-test"},
		"X-Codex-Thread-Id":          []string{"thread-123"},
		"X-Relay-Lifeline-Client-Id": []string{"client-123"},
	}
	destination := make(http.Header)
	copyHeaders(destination, source)
	if destination.Get("Authorization") == "" || destination.Get("X-Relay-Lifeline") != "1" {
		t.Fatalf("常规协议 Header 未透传: %+v", destination)
	}
	if destination.Get("X-Codex-Thread-ID") != "" || destination.Get("X-Relay-Lifeline-Client-ID") != "" {
		t.Fatalf("本地关联标识不应泄露到上游: %+v", destination)
	}
}
