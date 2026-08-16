package proxy

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/areasong/relay-lifeline/internal/state"
)

const maxClientIdentityLength = 128

var clientIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)

var clientIdentityHeaders = map[string]struct{}{
	"X-Relay-Lifeline-Client-Id": {},
	"X-Relay-Lifeline-Task-Id":   {},
	"X-Codex-Session-Id":         {},
	"X-Codex-Thread-Id":          {},
}

func clientIdentityFromHeaders(headers http.Header) state.RequestIdentity {
	return state.RequestIdentity{
		ClientID: firstClientIdentity(headers, "X-Relay-Lifeline-Client-ID", "X-Codex-Session-ID"),
		TaskID:   firstClientIdentity(headers, "X-Relay-Lifeline-Task-ID", "X-Codex-Thread-ID"),
	}
}

func firstClientIdentity(headers http.Header, names ...string) string {
	for _, name := range names {
		value := strings.TrimSpace(headers.Get(name))
		if len(value) <= maxClientIdentityLength && clientIdentityPattern.MatchString(value) {
			return value
		}
	}
	return ""
}

func isClientIdentityHeader(name string) bool {
	_, ok := clientIdentityHeaders[http.CanonicalHeaderKey(name)]
	return ok
}
