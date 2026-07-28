package main

import (
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

func TestSplitScenariosNormalizesInput(t *testing.T) {
	values := splitScenarios(" 401, TIMEOUT ,, success ")
	if strings.Join(values, ",") != "401,timeout,success" {
		t.Fatalf("场景解析异常: %v", values)
	}
}
