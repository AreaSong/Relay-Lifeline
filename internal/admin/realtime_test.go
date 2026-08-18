package admin

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestRealtimeFeedPublishesOnlyChangesAndReplaysAfterCursor(t *testing.T) {
	feed := newRealtimeFeed()
	if err := feed.publish("status", map[string]any{"active": 1}); err != nil {
		t.Fatal(err)
	}
	if err := feed.publish("status", map[string]any{"active": 1}); err != nil {
		t.Fatal(err)
	}
	if feed.latest() != 1 {
		t.Fatalf("相同载荷不应产生新事件: %d", feed.latest())
	}
	if err := feed.publish("status", map[string]any{"active": 2}); err != nil {
		t.Fatal(err)
	}
	page := feed.after(1)
	if page.Gap || len(page.Events) != 1 || page.Events[0].Sequence != 2 || page.Events[0].Version != realtimeProtocolVersion {
		t.Fatalf("游标回放异常: %+v", page)
	}
}

func TestRealtimeFeedReportsCursorGap(t *testing.T) {
	feed := newRealtimeFeed()
	for index := 0; index < realtimeEventCapacity+2; index++ {
		if err := feed.publish("status", map[string]any{"value": index}); err != nil {
			t.Fatal(err)
		}
	}
	page := feed.after(1)
	if !page.Gap || page.Latest != realtimeEventCapacity+2 || len(page.Events) != 0 {
		t.Fatalf("过旧游标应要求重置: %+v", page)
	}
}

func TestRealtimeCursorSupportsQueryAndLastEventID(t *testing.T) {
	request := httptest.NewRequest("GET", "/admin/api/stream?after=42", nil)
	cursor, supplied, valid := realtimeCursor(request)
	if cursor != 42 || !supplied || !valid {
		t.Fatalf("查询游标异常: cursor=%d supplied=%v valid=%v", cursor, supplied, valid)
	}
	request = httptest.NewRequest("GET", "/admin/api/stream", nil)
	request.Header.Set("Last-Event-ID", fmt.Sprint(9))
	cursor, supplied, valid = realtimeCursor(request)
	if cursor != 9 || !supplied || !valid {
		t.Fatalf("Last-Event-ID 游标异常: cursor=%d supplied=%v valid=%v", cursor, supplied, valid)
	}
}
