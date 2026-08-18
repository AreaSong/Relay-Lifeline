package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/areasong/relay-lifeline/internal/l10n"
)

type Validation struct {
	Success   bool
	Permanent bool
	Message   l10n.Message
	Usage     *TokenUsage
}

type TokenUsage struct {
	InputTokens  int64 `json:"inputTokens,omitempty"`
	OutputTokens int64 `json:"outputTokens,omitempty"`
	TotalTokens  int64 `json:"totalTokens"`
}

type protocolProfile int

const (
	protocolGenericJSON protocolProfile = iota
	protocolResponses
	protocolChatCompletions
)

func responseProfile(requestPath string) protocolProfile {
	requestPath = strings.TrimSuffix(requestPath, "/")
	switch {
	case strings.HasSuffix(requestPath, "/responses"):
		return protocolResponses
	case strings.HasSuffix(requestPath, "/chat/completions"):
		return protocolChatCompletions
	default:
		return protocolGenericJSON
	}
}

func validateResponse(response *http.Response, buffer *ReplayBuffer, profile protocolProfile, streaming bool) Validation {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Validation{Message: l10n.M("proxy.http_error", map[string]any{"Status": response.StatusCode})}
	}
	reader, err := buffer.Reader()
	if err != nil {
		return Validation{Message: l10n.M("proxy.cache_unreadable")}
	}
	defer reader.Close()
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if streaming {
		if contentType != "text/event-stream" {
			return Validation{Permanent: true, Message: l10n.M("proxy.content_type_unsupported")}
		}
		return validateEventStream(reader, profile)
	}
	if contentType != "application/json" && !strings.HasSuffix(contentType, "+json") {
		return Validation{Permanent: true, Message: l10n.M("proxy.content_type_unsupported")}
	}
	return validateJSON(reader, profile)
}

func validateEventStream(reader io.Reader, profile protocolProfile) Validation {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	responsesCompleted := false
	done := false
	failed := false
	var usage *TokenUsage
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			continue
		}
		var event struct {
			Type   string          `json:"type"`
			Error  json.RawMessage `json:"error"`
			Status string          `json:"status"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		if current := tokenUsageFromJSON([]byte(data)); current != nil {
			usage = current
		}
		switch event.Type {
		case "response.completed":
			responsesCompleted = true
		case "response.failed", "error", "response.incomplete":
			failed = true
		}
		if event.Status == "failed" || event.Status == "incomplete" || len(event.Error) > 0 && string(event.Error) != "null" {
			failed = true
		}
	}
	if err := scanner.Err(); err != nil {
		return Validation{Message: l10n.M("proxy.sse_read_failed"), Usage: usage}
	}
	if failed {
		return Validation{Message: l10n.M("proxy.sse_failed"), Usage: usage}
	}
	completed := responsesCompleted || done
	if profile == protocolResponses {
		completed = responsesCompleted
	} else if profile == protocolChatCompletions {
		completed = done
	}
	if !completed {
		return Validation{Message: l10n.M("proxy.sse_incomplete"), Usage: usage}
	}
	return Validation{Success: true, Usage: usage}
}

func validateJSON(reader io.Reader, profile protocolProfile) Validation {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Validation{Message: l10n.M("proxy.json_read_failed")}
	}
	usage := tokenUsageFromJSON(data)
	if len(bytes.TrimSpace(data)) == 0 {
		return Validation{Message: l10n.M("proxy.json_empty"), Usage: usage}
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(data, &response); err != nil || response == nil {
		return Validation{Message: l10n.M("proxy.json_invalid"), Usage: usage}
	}
	if len(response) == 0 {
		return Validation{Message: l10n.M("proxy.json_incomplete"), Usage: usage}
	}
	errorValue := response["error"]
	var status string
	_ = json.Unmarshal(response["status"], &status)
	if len(errorValue) > 0 && string(errorValue) != "null" || status == "failed" || status == "incomplete" {
		return Validation{Message: l10n.M("proxy.json_error"), Usage: usage}
	}
	if profile == protocolResponses && status != "completed" {
		return Validation{Message: l10n.M("proxy.json_incomplete"), Usage: usage}
	}
	if profile == protocolChatCompletions {
		var choices []json.RawMessage
		if raw := response["choices"]; len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &choices) != nil {
			return Validation{Message: l10n.M("proxy.chat_invalid"), Usage: usage}
		}
	}
	return Validation{Success: true, Usage: usage}
}

func tokenUsageFromJSON(data []byte) *TokenUsage {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil || envelope == nil {
		return nil
	}
	if usage, ok := parseUsageObject(envelope["usage"]); ok {
		return usage
	}
	if nested, ok := envelope["response"]; ok {
		return tokenUsageFromJSON(nested)
	}
	return nil
}

func parseUsageObject(raw json.RawMessage) (*TokenUsage, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	total, ok := usageInteger(object, "total_tokens", "totalTokens")
	if !ok || total < 0 {
		return nil, false
	}
	input, _ := usageInteger(object, "input_tokens", "prompt_tokens", "inputTokens", "promptTokens")
	output, _ := usageInteger(object, "output_tokens", "completion_tokens", "outputTokens", "completionTokens")
	if input < 0 || output < 0 {
		return nil, false
	}
	return &TokenUsage{InputTokens: input, OutputTokens: output, TotalTokens: total}, true
}

func usageInteger(object map[string]json.RawMessage, keys ...string) (int64, bool) {
	for _, key := range keys {
		raw, ok := object[key]
		if !ok {
			continue
		}
		var value int64
		if json.Unmarshal(raw, &value) == nil {
			return value, true
		}
	}
	return 0, false
}

func requestWantsStream(body []byte, header http.Header) bool {
	if strings.Contains(header.Get("Accept"), "text/event-stream") {
		return true
	}
	var request struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &request) == nil && request.Stream
}
