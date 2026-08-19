package proxy

import (
	"bytes"
	"net/http"
	"testing"
)

func FuzzValidateJSON(f *testing.F) {
	f.Add([]byte(`{"status":"completed"}`))
	f.Add([]byte(`{"choices":[]}`))
	f.Add([]byte(`{"id":"ok"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024*1024 {
			data = data[:1024*1024]
		}
		for _, profile := range []protocolProfile{protocolGenericJSON, protocolResponses, protocolChatCompletions} {
			result := validateJSON(bytes.NewReader(data), profile)
			if result.Usage != nil && (result.Usage.InputTokens < 0 || result.Usage.OutputTokens < 0 || result.Usage.TotalTokens < 0) {
				t.Fatalf("negative usage returned: %+v", result.Usage)
			}
		}
	})
}

func FuzzValidateEventStream(f *testing.F) {
	f.Add([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	f.Add([]byte("data: [DONE]\n\n"))
	f.Add([]byte(": keep-alive\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024*1024 {
			data = data[:1024*1024]
		}
		for _, profile := range []protocolProfile{protocolGenericJSON, protocolResponses, protocolChatCompletions} {
			result := validateEventStream(bytes.NewReader(data), profile)
			if result.Usage != nil && (result.Usage.InputTokens < 0 || result.Usage.OutputTokens < 0 || result.Usage.TotalTokens < 0) {
				t.Fatalf("negative usage returned: %+v", result.Usage)
			}
		}
	})
}

func FuzzRequestWantsStream(f *testing.F) {
	f.Add([]byte(`{"stream":true}`), "")
	f.Add([]byte(`{"stream":false}`), "text/event-stream")
	f.Fuzz(func(t *testing.T, body []byte, accept string) {
		if len(body) > 1024*1024 {
			body = body[:1024*1024]
		}
		header := http.Header{}
		header.Set("Accept", accept)
		_ = requestWantsStream(body, header)
	})
}
