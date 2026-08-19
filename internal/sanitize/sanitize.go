package sanitize

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	urlPattern         = regexp.MustCompile(`https?://[^\s<>"']+`)
	bearerPattern      = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=%-]+`)
	keyPattern         = regexp.MustCompile(`\b(?:sk|rk|pk|sess|key)-[A-Za-z0-9_-]{8,}\b`)
	jwtPattern         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	providerKeyPattern = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[A-Z0-9]{16})\b`)
	assignmentPattern  = regexp.MustCompile(`(?i)\b(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|token|authorization|client[_ -]?secret|password|secret)\b\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
)

func URL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid-url]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func Text(value string) string {
	value = strings.ToValidUTF8(value, "?")
	value = urlPattern.ReplaceAllStringFunc(value, URL)
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = keyPattern.ReplaceAllString(value, "[REDACTED]")
	value = jwtPattern.ReplaceAllString(value, "[REDACTED]")
	value = providerKeyPattern.ReplaceAllString(value, "[REDACTED]")
	return assignmentPattern.ReplaceAllString(value, "[REDACTED]")
}
