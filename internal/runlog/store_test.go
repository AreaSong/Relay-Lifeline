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
