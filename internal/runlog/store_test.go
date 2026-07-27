package runlog

import (
	"testing"
	"time"
)

func TestStoreFiltersAndPrunes(t *testing.T) {
	now := time.Now()
	store := New(func() Limits { return Limits{MaxItems: 2, Retention: time.Hour} })
	store.now = func() time.Time { return now }
	store.Add(Entry{Level: "info", Event: "request.received", RequestID: "one"})
	store.Add(Entry{Level: "warn", Event: "upstream.failed", RequestID: "one"})
	store.Add(Entry{Level: "info", Event: "request.received", RequestID: "two"})
	if entries := store.List(0, "", "", ""); len(entries) != 2 || entries[0].ID != 2 {
		t.Fatalf("容量裁剪异常: %+v", entries)
	}
	if entries := store.List(0, "warn", "", "one"); len(entries) != 1 || entries[0].Event != "upstream.failed" {
		t.Fatalf("筛选异常: %+v", entries)
	}
	if entries := store.List(2, "", "", ""); len(entries) != 1 || entries[0].ID != 3 {
		t.Fatalf("增量读取异常: %+v", entries)
	}
}

func TestPageReportsRetentionGapAndBounds(t *testing.T) {
	now := time.Now()
	store := New(func() Limits { return Limits{MaxItems: 3, Retention: time.Hour} })
	store.now = func() time.Time { return now }
	for index := 0; index < 5; index++ {
		store.Add(Entry{Level: "info", Event: "request.received"})
	}
	page := store.Page(0, 2, "", "", "")
	if !page.HasGap || !page.HasMore || page.OldestAfter != 2 || page.NextAfter != 4 || len(page.Entries) != 2 {
		t.Fatalf("日志分页或缺口信息异常: %+v", page)
	}
	tail := store.Page(page.NextAfter, 2, "", "", "")
	if tail.HasGap || tail.HasMore || len(tail.Entries) != 1 || tail.Entries[0].ID != 5 {
		t.Fatalf("日志续页异常: %+v", tail)
	}
}

func TestTailReturnsMostRecentMatchingEntries(t *testing.T) {
	store := New(func() Limits { return Limits{MaxItems: 10, Retention: time.Hour} })
	for index := 0; index < 5; index++ {
		store.Add(Entry{Level: "info", Event: "request.received"})
	}
	page := store.Tail(2, "info", "", "")
	if !page.HasMore || !page.HasGap || page.NextAfter != 5 || len(page.Entries) != 2 || page.Entries[0].ID != 4 {
		t.Fatalf("日志尾页异常: %+v", page)
	}
}
