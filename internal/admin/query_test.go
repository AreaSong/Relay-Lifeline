package admin

import (
	"net/url"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

func TestHistoryQueryUsesStableCursorAndFilters(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	records := []timeline.Record{
		{ID: "three", Method: "POST", Path: "/v1/responses", State: "failed", StartedAt: base.Add(3 * time.Minute), CompletedAt: base.Add(4 * time.Minute)},
		{ID: "two", Method: "POST", Path: "/v1/chat/completions", State: "successful", StartedAt: base.Add(2 * time.Minute), CompletedAt: base.Add(3 * time.Minute)},
		{ID: "one", Method: "GET", Path: "/health", State: "successful", StartedAt: base, CompletedAt: base.Add(time.Minute)},
	}
	query, err := parseListQuery(url.Values{"limit": {"1"}, "state": {"successful"}}, 200)
	if err != nil {
		t.Fatal(err)
	}
	first := queryHistory(records, query)
	if len(first.Items) != 1 || first.Items[0].ID != "two" || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("历史首页异常: %+v", first)
	}
	query, err = parseListQuery(url.Values{"limit": {"1"}, "state": {"successful"}, "cursor": {first.NextCursor}}, 200)
	if err != nil {
		t.Fatal(err)
	}
	second := queryHistory(records, query)
	if len(second.Items) != 1 || second.Items[0].ID != "one" || second.HasMore {
		t.Fatalf("历史下一页异常: %+v", second)
	}
}

func TestIncidentQuerySearchesAffectedRequests(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	items := []incident.Incident{
		{ID: "latest", State: "open", StartedAt: base.Add(time.Minute), AffectedRequests: []string{"request-abc"}},
		{ID: "old", State: "resolved", StartedAt: base, AffectedRequests: []string{"request-other"}},
	}
	query, err := parseListQuery(url.Values{"q": {"abc"}, "state": {"open"}}, 200)
	if err != nil {
		t.Fatal(err)
	}
	page := queryIncidents(items, query)
	if len(page.Items) != 1 || page.Items[0].ID != "latest" {
		t.Fatalf("事故筛选异常: %+v", page)
	}
}

func TestListQueryRejectsInvalidCursorAndRange(t *testing.T) {
	if _, err := parseListQuery(url.Values{"cursor": {"not-base64"}}, 200); err == nil {
		t.Fatal("应拒绝无效游标")
	}
	if _, err := parseListQuery(url.Values{"from": {"2026-08-19T00:00:00Z"}, "to": {"2026-08-18T00:00:00Z"}}, 200); err == nil {
		t.Fatal("应拒绝反向时间范围")
	}
}

func TestBuildIncidentTimelineMergesLifecycleAndRequestEvents(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	recovery, resolved := base.Add(3*time.Minute), base.Add(4*time.Minute)
	item := incident.Incident{ID: "incident", StartedAt: base, RecoveryStarted: &recovery, ResolvedAt: &resolved}
	records := []timeline.Record{{ID: "request", Events: []timeline.Event{
		{Time: base.Add(-time.Second), Type: "before", Message: "skip"},
		{Time: base.Add(time.Minute), Type: "attempt_failed", Attempt: 1, StatusCode: 503, Message: "failed"},
		{Time: resolved.Add(time.Second), Type: "after", Message: "skip"},
	}}}
	events := buildIncidentTimeline(item, records, func(value string) string { return value })
	if len(events) != 4 || events[0].Type != "incident_opened" || events[1].RequestID != "request" || events[2].Type != "incident_recovering" || events[3].Type != "incident_resolved" {
		t.Fatalf("事故时间线异常: %+v", events)
	}
}
