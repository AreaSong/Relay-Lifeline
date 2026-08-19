package timeline

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/l10n"
)

func TestHistoryCapacityAndRetention(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	store := New(func() Limits { return Limits{MaxItems: 2, Retention: time.Hour} })
	store.now = func() time.Time { return now }
	for _, id := range []string{"one", "two", "three"} {
		store.Start(id, "POST", "/v1/responses")
		store.Add(id, Event{Type: "attempt_started", Attempt: 1, Message: "开始尝试"})
		store.Finish(id, "successful")
	}
	history := store.History()
	if len(history) != 2 || history[0].ID != "three" || history[1].ID != "two" {
		t.Fatalf("历史容量或顺序异常: %+v", history)
	}
	now = now.Add(2 * time.Hour)
	if history = store.History(); len(history) != 0 {
		t.Fatalf("过期历史未清理: %+v", history)
	}
}

func TestStoredMessageCanBeLocalizedAfterCompletion(t *testing.T) {
	store := New(func() Limits { return Limits{MaxItems: 10, Retention: time.Hour} })
	store.Start("request", "POST", "/v1/responses")
	store.Add("request", Event{Type: "attempt_failed", Attempt: 1, MessageCode: "proxy.http_error", MessageDetails: map[string]any{"Status": 503}})
	store.Finish("request", "failed")
	record := store.History()[0]
	english := LocalizeRecord(record, l10n.LocaleEnglish, l10n.LocaleChinese)
	chinese := LocalizeRecord(record, l10n.LocaleChinese, l10n.LocaleEnglish)
	if english.LastError != "HTTP 503" || chinese.LastError != "HTTP 503" {
		t.Fatalf("错误消息插值异常: en=%q zh=%q", english.LastError, chinese.LastError)
	}
	if english.Events[0].Message != "Request received" || chinese.Events[0].Message != "收到请求" {
		t.Fatalf("完成后切换语言异常: en=%q zh=%q", english.Events[0].Message, chinese.Events[0].Message)
	}
	if record.Events[0].Message != "" {
		t.Fatal("本地化修改了存储记录")
	}
}

func TestWithoutErrorDetailsLeavesSourceUntouched(t *testing.T) {
	records := []Record{{
		ID: "request", LastErrorDetail: &ErrorDetail{Message: "private", Parsed: true},
		Events: []Event{{Type: "attempt_failed", ErrorDetail: &ErrorDetail{Message: "private", Parsed: true}}},
	}}
	redacted := WithoutErrorDetails(records)
	if redacted[0].LastErrorDetail != nil || redacted[0].Events[0].ErrorDetail != nil {
		t.Fatalf("错误详情未剥离: %+v", redacted)
	}
	if records[0].LastErrorDetail == nil || records[0].Events[0].ErrorDetail == nil {
		t.Fatal("剥离操作修改了源记录")
	}
}

func TestTimelineNeverStoresBusinessPayload(t *testing.T) {
	store := New(func() Limits { return Limits{MaxItems: 10, Retention: time.Hour} })
	store.Start("request", "POST", "/v1/responses")
	store.Add("request", Event{Type: "attempt_failed", Attempt: 1, StatusCode: 503, Category: "http", Message: "HTTP 503"})
	record, ok := store.Request("request")
	if !ok || len(record.Events) != 2 || record.LastError != "HTTP 503" {
		t.Fatalf("时间线异常: %+v", record)
	}
}

func TestTimelineCapPreservesReceivedEventAndReportsTruncation(t *testing.T) {
	store := New(func() Limits { return Limits{MaxItems: 10, Retention: time.Hour} })
	store.Start("request", "POST", "/v1/responses")
	for attempt := 1; attempt <= maxEventsPerRequest+9; attempt++ {
		store.Add("request", Event{Type: "attempt_started", Attempt: attempt})
	}
	record, ok := store.Request("request")
	if !ok || len(record.Events) != maxEventsPerRequest || record.Events[0].Type != "received" {
		t.Fatalf("时间线容量或首事件异常: %+v", record)
	}
	if !record.EventsTruncated || record.DroppedEvents != 10 {
		t.Fatalf("时间线截断信息异常: %+v", record)
	}
}

func TestPersistentTimelineReplaysCompletedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	eventJournal, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	limits := func() Limits { return Limits{MaxItems: 10, Retention: time.Hour} }
	store, err := NewPersistent(limits, eventJournal)
	if err != nil {
		t.Fatal(err)
	}
	store.StartWithIdentity("request", "POST", "/v1/responses", "codex-session", "codex-thread")
	store.Add("request", Event{Type: "attempt_started", Attempt: 1})
	store.Finish("request", "successful")
	if err := eventJournal.Close(); err != nil {
		t.Fatal(err)
	}

	eventJournal, err = journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer eventJournal.Close()
	replayed, err := NewPersistent(limits, eventJournal)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := replayed.Request("request")
	if !ok || record.State != "successful" || record.Attempt != 1 || len(record.Events) != 2 || record.ClientID != "codex-session" || record.TaskID != "codex-thread" {
		t.Fatalf("持久化时间线回放异常: %+v", record)
	}
}

func TestPersistentTimelineMarksInterruptedRequestOrphaned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	eventJournal, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	limits := func() Limits { return Limits{MaxItems: 10, Retention: time.Hour} }
	store, err := NewPersistent(limits, eventJournal)
	if err != nil {
		t.Fatal(err)
	}
	store.Start("interrupted", "POST", "/v1/responses")
	if err := eventJournal.Close(); err != nil {
		t.Fatal(err)
	}

	eventJournal, err = journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := NewPersistent(limits, eventJournal)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := recovered.Request("interrupted")
	if !ok || record.State != "orphaned" || record.CompletedAt.IsZero() {
		t.Fatalf("中断请求未标记为 orphaned: %+v", record)
	}
	if err := eventJournal.Close(); err != nil {
		t.Fatal(err)
	}

	eventJournal, err = journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer eventJournal.Close()
	replayed, err := NewPersistent(limits, eventJournal)
	if err != nil {
		t.Fatal(err)
	}
	record, ok = replayed.Request("interrupted")
	if !ok || record.State != "orphaned" {
		t.Fatalf("orphaned 终态未持久化: %+v", record)
	}
}

func TestPersistentTimelineProtectsActiveRequestDuringCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	eventJournal, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	limits := func() Limits { return Limits{MaxItems: 10, Retention: time.Hour} }
	store, err := NewPersistent(limits, eventJournal)
	if err != nil {
		eventJournal.Close()
		t.Fatal(err)
	}
	if err := store.Start("active", "POST", "/v1/responses"); err != nil {
		eventJournal.Close()
		t.Fatal(err)
	}
	if _, err := eventJournal.CompactWithProtection(time.Now().Add(time.Hour), store.ActiveIDs()); err != nil {
		eventJournal.Close()
		t.Fatal(err)
	}
	if err := store.Add("active", Event{Type: "attempt_started", Attempt: 1}); err != nil {
		eventJournal.Close()
		t.Fatal(err)
	}
	if err := eventJournal.Close(); err != nil {
		t.Fatal(err)
	}

	eventJournal, err = journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer eventJournal.Close()
	recovered, err := NewPersistent(limits, eventJournal)
	if err != nil {
		t.Fatalf("压实后的 active 请求无法恢复: %v", err)
	}
	record, ok := recovered.Request("active")
	if !ok || record.State != "orphaned" || len(record.Events) != 2 || record.Attempt != 1 {
		t.Fatalf("压实后的 active 请求恢复异常: %+v", record)
	}
}
