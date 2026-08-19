package journal

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
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

func TestJournalCompactWithProtectionRetainsExpiredEntity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	store, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Append("active", "start", map[string]string{"state": "queued"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("expired", "start", map[string]string{"state": "queued"}); err != nil {
		t.Fatal(err)
	}

	removed, err := store.CompactWithProtection(time.Now().Add(time.Hour), map[string]struct{}{"active": struct{}{}})
	if err != nil || removed != 1 {
		t.Fatalf("带保护集合的压实结果异常: removed=%d err=%v", removed, err)
	}
	entries := store.Entries()
	if len(entries) != 1 || entries[0].EntityID != "active" {
		t.Fatalf("保护实体未保留: %+v", entries)
	}
	if _, err := store.Append("active", "event", map[string]string{"state": "running"}); err != nil {
		t.Fatalf("保护实体压实后追加失败: %v", err)
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

func TestJournalIntegrityAnchorDetectsRewrittenValidChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	key := []byte("01234567890123456789012345678901")
	store, err := OpenWithIntegrity(path, true, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("request-1", "request.started", map[string]string{"path": "/v1/responses"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Payload = json.RawMessage(`{"path":"/rewritten"}`)
	entries = rebuildChain(entries)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		line, _ := json.Marshal(entry)
		if _, err := file.Write(append(line, '\n')); err != nil {
			file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWithIntegrity(path, true, key); err == nil {
		t.Fatal("重写合法 hash chain 后应被 HMAC anchor 拒绝")
	}
}

func TestJournalIntegrityAnchorSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	key := []byte("01234567890123456789012345678901")
	store, err := OpenWithIntegrity(path, true, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("request-1", "start", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithIntegrity(path, true, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Append("request-1", "finish", nil); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyWithIntegrityRejectsRewrittenValidChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	key := []byte("01234567890123456789012345678901")
	store, err := OpenWithIntegrity(path, true, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("request-1", "start", map[string]string{"value": "original"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Payload = json.RawMessage(`{"value":"rewritten"}`)
	entries = rebuildChain(entries)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		line, _ := json.Marshal(entry)
		_, _ = file.Write(append(line, '\n'))
	}
	_ = file.Close()
	if _, err := VerifyWithIntegrity(path, key); err == nil {
		t.Fatal("offline integrity verification accepted rewritten chain")
	}
}

func TestJournalReportsRuntimeStatsAndHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	store, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("request-1", "start", map[string]bool{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Compact(time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	stats := store.Stats()
	if stats.Entries != 1 || stats.SizeBytes == 0 || stats.ReplayDuration < 0 || stats.LastCompactionAt.IsZero() || !stats.CompactionHealthy {
		t.Fatalf("日志统计异常: %+v", stats)
	}
	if err := store.Health(); err != nil {
		t.Fatalf("日志健康检查失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Health(); err == nil {
		t.Fatal("已关闭日志不应继续报告健康")
	}
}

func TestJournalDegradesOnShortWriteAndRejectsFurtherAppends(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "requests.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetHooks(Hooks{Write: func(_ *os.File, data []byte) (int, error) { return len(data) - 1, nil }})
	if _, err := store.Append("request-1", "start", map[string]bool{"ok": true}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("应返回短写错误: %v", err)
	}
	if status := store.Status(); status.State != StateDegraded || status.FailedStage != "write" || status.FailureCount != 1 {
		t.Fatalf("短写状态异常: %+v", status)
	}
	if _, err := store.Append("request-2", "start", nil); err == nil {
		t.Fatal("降级 Journal 不应继续接受写入")
	}
}

func TestJournalClassifiesENOSPCAsWriteFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "requests.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetHooks(Hooks{Write: func(_ *os.File, _ []byte) (int, error) { return 0, syscall.ENOSPC }})
	if _, err := store.Append("request-1", "start", nil); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("应保留 ENOSPC 错误: %v", err)
	}
	if status := store.Status(); status.FailedStage != "write" {
		t.Fatalf("ENOSPC 应归类为 write: %+v", status)
	}
}

func TestJournalClassifiesSyncFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "requests.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetHooks(Hooks{Sync: func(*os.File) error { return io.ErrClosedPipe }})
	if _, err := store.Append("request-1", "start", nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("应保留 Sync 错误: %v", err)
	}
	if status := store.Status(); status.FailedStage != "sync" {
		t.Fatalf("Sync 错误分类异常: %+v", status)
	}
}
