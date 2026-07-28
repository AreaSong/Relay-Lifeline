package incident

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/journal"
)

func TestIncidentCorrelatesFailuresAndRequiresStableRecovery(t *testing.T) {
	cfg := config.Default().Incidents
	cfg.RecoveryStableWindow.Duration = 15 * time.Millisecond
	store, err := New(func() config.IncidentConfig { return cfg }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.RecordFailure("one", "server", 503)
	store.RecordFailure("two", "transport", 0)
	store.RecordSuccess()
	store.RecordFailure("two", "server", 502)
	time.Sleep(25 * time.Millisecond)
	items := store.List()
	if len(items) != 1 || items[0].State != "open" || items[0].FailedAttempts != 3 || len(items[0].AffectedRequests) != 2 {
		t.Fatalf("事故关联或恢复闸门异常: %+v", items)
	}
	store.RecordSuccess()
	time.Sleep(25 * time.Millisecond)
	items = store.List()
	if items[0].State != "resolved" || items[0].ResolvedAt == nil {
		t.Fatalf("稳定恢复后未关闭事故: %+v", items[0])
	}
}

func TestIncidentSnapshotsReplayFromJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incidents.jsonl")
	eventJournal, err := journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Incidents
	store, err := New(func() config.IncidentConfig { return cfg }, eventJournal)
	if err != nil {
		t.Fatal(err)
	}
	store.RecordFailure("request", "auth", 401)
	store.Close()
	if err := eventJournal.Close(); err != nil {
		t.Fatal(err)
	}
	eventJournal, err = journal.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer eventJournal.Close()
	replayed, err := New(func() config.IncidentConfig { return cfg }, eventJournal)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	items := replayed.List()
	if len(items) != 1 || items[0].Categories["auth"] != 1 || items[0].StatusCodes[401] != 1 {
		t.Fatalf("事故日志回放异常: %+v", items)
	}
}
