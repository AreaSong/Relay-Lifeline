package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJournalPersistsAndVerifiesHashChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	store, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("request-1", "request.started", map[string]string{"method": "POST"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("request-1", "request.finished", map[string]string{"outcome": "successful"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := Verify(path)
	if err != nil || len(entries) != 2 || entries[1].PreviousHash != entries[0].Hash {
		t.Fatalf("日志校验异常: entries=%d err=%v", len(entries), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("日志权限异常: mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestJournalCompactRetainsWholeRecentEntities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	store, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour).UTC()
	recentTime := time.Now().UTC()
	store.entries = []Entry{
		{SchemaVersion: SchemaVersion, Sequence: 1, Time: oldTime, EntityID: "expired", Type: "start"},
		{SchemaVersion: SchemaVersion, Sequence: 2, Time: oldTime, EntityID: "retained", Type: "start"},
		{SchemaVersion: SchemaVersion, Sequence: 3, Time: recentTime, EntityID: "retained", Type: "finish"},
	}
	store.entries = rebuildChain(store.entries)
	store.sequence = 3
	store.lastHash = store.entries[2].Hash
	if err := store.file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	for _, entry := range store.entries {
		line, _ := json.Marshal(entry)
		if _, err := store.file.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := store.Compact(time.Now().Add(-24 * time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("压实结果异常: removed=%d err=%v", removed, err)
	}
	if _, err := store.Append("new", "start", map[string]string{"state": "queued"}); err != nil {
		t.Fatalf("压实后追加失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := Verify(path)
	if err != nil || len(entries) != 3 || entries[0].EntityID != "retained" || entries[0].Sequence != 1 || entries[2].EntityID != "new" || entries[2].PreviousHash != entries[1].Hash {
		t.Fatalf("压实后日志异常: entries=%+v err=%v", entries, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("压实后权限异常: mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestJournalRejectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	store, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("request-1", "request.started", map[string]string{"path": "/v1/responses"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path); err == nil {
		t.Fatal("篡改后的日志不应通过校验")
	}
}
