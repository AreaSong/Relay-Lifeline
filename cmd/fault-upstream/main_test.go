package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFaultServerFollowsSequenceAndSticksOnLastScenario(t *testing.T) {
	server := &faultServer{scenarios: []string{"503", "truncated-json", "success"}, delay: time.Second}
	for index, expected := range []struct {
		status int
		body   string
	}{{503, "fault drill HTTP 503"}, {200, `"fault_drill"`}, {200, `"completed"`}, {200, `"completed"`}} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"test"}`)))
		if recorder.Code != expected.status || !strings.Contains(recorder.Body.String(), expected.body) {
			t.Fatalf("第 %d 个演练响应异常: status=%d body=%s", index+1, recorder.Code, recorder.Body.String())
		}
	}
}

func TestFaultServerStructuredEventsAndSlowScenarios(t *testing.T) {
	var events bytes.Buffer
	server := &faultServer{name: "primary", scenarios: []string{"slow-upload", "slow-sse", "success-sse"}, delay: time.Second, chunkDelay: time.Millisecond, readChunk: 2, events: &events}
	for _, body := range []string{`{"input":"test"}`, `{"stream":true}`, `{"stream":true}`} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "stable-key")
		server.ServeHTTP(recorder, request)
		if !strings.Contains(recorder.Body.String(), "completed") {
			t.Fatalf("慢速场景未完成: %s", recorder.Body.String())
		}
	}
	scanner := bufio.NewScanner(&events)
	count := 0
	for scanner.Scan() {
		var event faultEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		count++
		if event.Request != count || event.Name != "primary" || event.IdempotencyKey != "stable-key" || event.BodyBytes == 0 {
			t.Fatalf("结构化事件异常: %+v", event)
		}
	}
	if count != 3 || scanner.Err() != nil {
		t.Fatalf("结构化事件数量异常: count=%d err=%v", count, scanner.Err())
	}
}

func TestFaultServerStalledSSEObservesCancellation(t *testing.T) {
	var events bytes.Buffer
	server := &faultServer{scenarios: []string{"stalled-sse"}, delay: time.Minute, events: &events}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"stream":true}`)).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stalled SSE 未响应取消")
	}
	var event faultEvent
	if err := json.Unmarshal(bytes.TrimSpace(events.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if !event.ContextCanceled || event.Scenario != "stalled-sse" {
		t.Fatalf("取消事件异常: %+v", event)
	}
}

func TestSplitScenariosNormalizesInput(t *testing.T) {
	values := splitScenarios(" 401, TIMEOUT ,, success ")
	if strings.Join(values, ",") != "401,timeout,success" {
		t.Fatalf("场景解析异常: %v", values)
	}
}
