package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

var (
	urlTokenPattern    = regexp.MustCompile(`https?://[^\s<>"']+`)
	bearerPattern      = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=%-]+`)
	keyPattern         = regexp.MustCompile(`\b(?:sk|rk|pk|sess|key)-[A-Za-z0-9_-]{8,}\b`)
	jwtPattern         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	providerKeyPattern = regexp.MustCompile(
		`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[A-Z0-9]{16})\b`,
	)
	assignmentPattern = regexp.MustCompile(
		`(?i)\b(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|authorization|client[_ -]?secret|password|secret)\b\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`,
	)
)

func extractSafeErrorDetail(cfg config.ObservabilityConfig, result attemptResult, streaming bool) *timeline.ErrorDetail {
	if cfg.ErrorDetails != "safe" || result.response == nil || result.buffer == nil {
		return nil
	}
	detail := &timeline.ErrorDetail{
		ResponseBytes: result.buffer.Size(),
		RetryAfter:    result.response.Header.Get("Retry-After"),
	}
	detail.UpstreamRequestID = upstreamRequestID(result.response.Header)

	reader, err := result.buffer.Reader()
	if err == nil {
		defer reader.Close()
		contentType, _, _ := mime.ParseMediaType(result.response.Header.Get("Content-Type"))
		switch {
		case contentType == "application/json" || strings.HasSuffix(contentType, "+json"):
			detail.Parsed = extractJSONError(reader, detail)
		case streaming || contentType == "text/event-stream":
			detail.Parsed = extractSSEError(reader, detail)
		}
	}
	limitErrorDetail(detail, int(cfg.MaxErrorDetail))
	return detail
}

func extractJSONError(reader io.Reader, detail *timeline.ErrorDetail) bool {
	data, err := io.ReadAll(reader)
	if err != nil {
		return false
	}
	return extractEnvelope(data, detail, true)
}

func extractSSEError(reader io.Reader, detail *timeline.ErrorDetail) bool {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	parsed := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := bytes.TrimSpace([]byte(strings.TrimPrefix(line, "data:")))
		var event struct {
			Type     string          `json:"type"`
			Status   string          `json:"status"`
			Error    json.RawMessage `json:"error"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil || !isFailureEvent(event.Type, event.Status, event.Error) {
			continue
		}
		parsed = extractEnvelope(data, detail, false) || parsed
		var eventFields struct {
			Message json.RawMessage `json:"message"`
			Code    json.RawMessage `json:"code"`
		}
		if json.Unmarshal(data, &eventFields) == nil {
			parsed = setMissing(&detail.Message, scalar(eventFields.Message)) || parsed
			parsed = setMissing(&detail.Code, scalar(eventFields.Code)) || parsed
		}
		if len(event.Response) > 0 {
			parsed = extractEnvelope(event.Response, detail, false) || parsed
		}
	}
	return parsed
}

func isFailureEvent(eventType, status string, rawError json.RawMessage) bool {
	if eventType == "response.failed" || eventType == "response.incomplete" || eventType == "error" {
		return true
	}
	if status == "failed" || status == "incomplete" {
		return true
	}
	return len(rawError) > 0 && string(rawError) != "null"
}

func extractEnvelope(data []byte, detail *timeline.ErrorDetail, allowTopLevel bool) bool {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message json.RawMessage `json:"message"`
		Type    json.RawMessage `json:"type"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return false
	}
	parsed := extractErrorValue(envelope.Error, detail)
	if allowTopLevel {
		parsed = setMissing(&detail.Message, scalar(envelope.Message)) || parsed
		parsed = setMissing(&detail.Type, scalar(envelope.Type)) || parsed
		parsed = setMissing(&detail.Code, scalar(envelope.Code)) || parsed
	}
	return parsed
}

func extractErrorValue(raw json.RawMessage, detail *timeline.ErrorDetail) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	if message := scalar(raw); message != "" {
		return setMissing(&detail.Message, message)
	}
	var value struct {
		Message json.RawMessage `json:"message"`
		Type    json.RawMessage `json:"type"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	parsed := setMissing(&detail.Message, scalar(value.Message))
	parsed = setMissing(&detail.Type, scalar(value.Type)) || parsed
	parsed = setMissing(&detail.Code, scalar(value.Code)) || parsed
	return parsed
}

func scalar(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var number json.Number
	if decoder.Decode(&number) == nil {
		return number.String()
	}
	return ""
}

func setMissing(target *string, value string) bool {
	if *target != "" || value == "" {
		return false
	}
	*target = value
	return true
}

func upstreamRequestID(header http.Header) string {
	for _, name := range []string{"OpenAI-Request-ID", "X-Request-ID", "Request-ID", "X-Correlation-ID"} {
		if value := header.Get(name); value != "" {
			return value
		}
	}
	return ""
}

func limitErrorDetail(detail *timeline.ErrorDetail, maximum int) {
	remaining := maximum
	for _, target := range []*string{&detail.Message, &detail.Type, &detail.Code, &detail.UpstreamRequestID, &detail.RetryAfter} {
		value := sanitizeDetail(*target)
		*target = truncateUTF8(value, remaining)
		remaining -= len(*target)
		if remaining < 0 {
			remaining = 0
		}
	}
}

func sanitizeDetail(value string) string {
	value = strings.ToValidUTF8(value, "?")
	value = urlTokenPattern.ReplaceAllStringFunc(value, redactDetailURL)
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = keyPattern.ReplaceAllString(value, "[REDACTED]")
	value = jwtPattern.ReplaceAllString(value, "[REDACTED]")
	value = providerKeyPattern.ReplaceAllString(value, "[REDACTED]")
	value = assignmentPattern.ReplaceAllString(value, "[REDACTED]")
	return strings.Join(strings.Fields(value), " ")
}

func redactDetailURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "[REDACTED_URL]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func truncateUTF8(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if len(value) <= maximum {
		return value
	}
	if maximum <= 3 {
		return strings.Repeat(".", maximum)
	}
	end := maximum - 3
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "..."
}
