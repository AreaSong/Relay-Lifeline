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
	Success bool
	Message l10n.Message
}

func validateResponse(response *http.Response, buffer *ReplayBuffer, streaming bool) Validation {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Validation{Message: l10n.M("proxy.http_error", map[string]any{"Status": response.StatusCode})}
	}
	reader, err := buffer.Reader()
	if err != nil {
		return Validation{Message: l10n.M("proxy.cache_unreadable")}
	}
	defer reader.Close()
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if streaming || contentType == "text/event-stream" {
		return validateEventStream(reader)
	}
	if contentType == "application/json" || strings.HasSuffix(contentType, "+json") {
		return validateJSON(reader)
	}
	return Validation{Success: buffer.Size() > 0, Message: l10n.M("proxy.empty_response")}
}

func validateEventStream(reader io.Reader) Validation {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	completed := false
	failed := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			completed = true
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
		switch event.Type {
		case "response.completed":
			completed = true
		case "response.failed", "error", "response.incomplete":
			failed = true
		}
		if event.Status == "failed" || event.Status == "incomplete" || len(event.Error) > 0 && string(event.Error) != "null" {
			failed = true
		}
	}
	if err := scanner.Err(); err != nil {
		return Validation{Message: l10n.M("proxy.sse_read_failed")}
	}
	if failed {
		return Validation{Message: l10n.M("proxy.sse_failed")}
	}
	if !completed {
		return Validation{Message: l10n.M("proxy.sse_incomplete")}
	}
	return Validation{Success: true}
}

func validateJSON(reader io.Reader) Validation {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Validation{Message: l10n.M("proxy.json_read_failed")}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Validation{Message: l10n.M("proxy.json_empty")}
	}
	var response struct {
		Error  json.RawMessage `json:"error"`
		Status string          `json:"status"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return Validation{Message: l10n.M("proxy.json_invalid")}
	}
	if len(response.Error) > 0 && string(response.Error) != "null" || response.Status == "failed" || response.Status == "incomplete" {
		return Validation{Message: l10n.M("proxy.json_error")}
	}
	return Validation{Success: true}
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
