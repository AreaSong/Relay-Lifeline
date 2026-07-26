package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

type Validation struct {
	Success bool
	Reason  string
}

func validateResponse(response *http.Response, buffer *ReplayBuffer, streaming bool) Validation {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Validation{Reason: fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	reader, err := buffer.Reader()
	if err != nil {
		return Validation{Reason: "无法读取响应缓存"}
	}
	defer reader.Close()
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if streaming || contentType == "text/event-stream" {
		return validateEventStream(reader)
	}
	if contentType == "application/json" || strings.HasSuffix(contentType, "+json") {
		return validateJSON(reader)
	}
	return Validation{Success: buffer.Size() > 0, Reason: "空响应"}
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
		return Validation{Reason: "SSE 读取失败"}
	}
	if failed {
		return Validation{Reason: "SSE 返回失败事件"}
	}
	if !completed {
		return Validation{Reason: "SSE 缺少完成事件"}
	}
	return Validation{Success: true}
}

func validateJSON(reader io.Reader) Validation {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Validation{Reason: "JSON 读取失败"}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Validation{Reason: "JSON 响应为空"}
	}
	var response struct {
		Error  json.RawMessage `json:"error"`
		Status string          `json:"status"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return Validation{Reason: "JSON 无法解析"}
	}
	if len(response.Error) > 0 && string(response.Error) != "null" || response.Status == "failed" || response.Status == "incomplete" {
		return Validation{Reason: "JSON 返回错误状态"}
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
