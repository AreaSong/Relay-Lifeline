package capture

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`),
	regexp.MustCompile(`\bgh[opusr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|secret|password)(\s*[=:]\s*)[^\s,;&]+`),
}

func FilterBody(data []byte, contentType string) []byte {
	trimmed := bytes.TrimSpace(data)
	if strings.Contains(strings.ToLower(contentType), "json") || json.Valid(trimmed) {
		var value any
		if json.Unmarshal(trimmed, &value) == nil {
			value = filterJSON(value, "")
			if formatted, err := json.MarshalIndent(value, "", "  "); err == nil {
				return formatted
			}
		}
	}
	return redactText(data)
}

func filterJSON(value any, key string) any {
	if sensitiveName(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			typed[childKey] = filterJSON(child, childKey)
		}
		return typed
	case []any:
		for index := range typed {
			typed[index] = filterJSON(typed[index], key)
		}
		return typed
	case string:
		return string(redactText([]byte(typed)))
	default:
		return value
	}
}

func sensitiveName(value string) bool {
	lower := strings.ToLower(value)
	return lower == "authorization" || lower == "cookie" || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey")
}

func redactText(data []byte) []byte {
	result := append([]byte(nil), data...)
	for _, pattern := range secretPatterns {
		result = pattern.ReplaceAll(result, []byte("[REDACTED]"))
	}
	return result
}
