package recovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/journal"
)

func TestConfigBackupsReturnsMetadataWithoutContents(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	backupDirectory := filepath.Join(directory, "backups")
	if err := os.MkdirAll(backupDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(backupDirectory, "config-20260728T000000Z.yaml")
	if err := cfg.Save(backupPath); err != nil {
		t.Fatal(err)
	}
	backups, err := ConfigBackups(configPath, backupDirectory)
	if err != nil || len(backups) != 1 {
		t.Fatalf("备份元数据异常: %+v err=%v", backups, err)
	}
	item := backups[0]
	if item.Name != filepath.Base(backupPath) || !item.Valid || item.SchemaVersion != config.CurrentSchemaVersion || item.SHA256 == "" || item.Size == 0 {
		t.Fatalf("备份元数据不完整: %+v", item)
	}
	encoded := item.Name + item.SHA256 + item.Error
	if strings.Contains(encoded, cfg.Upstream.BaseURL) {
		t.Fatal("备份元数据泄露配置正文")
	}
}

func TestVerifyChecksBackupsAndJournalChains(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Default()
	cfg.Server.ConfigBackupDir = filepath.Join(directory, "backups")
	cfg.Persistence.Directory = filepath.Join(directory, "events")
	cfg.Capture.StorageDir = filepath.Join(directory, "captures")
	if err := os.MkdirAll(cfg.Server.ConfigBackupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Capture.StorageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(filepath.Join(cfg.Server.ConfigBackupDir, "config-001.yaml")); err != nil {
		t.Fatal(err)
	}
	requests, err := journal.Open(filepath.Join(cfg.Persistence.Directory, "requests.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requests.Append("request", "start", map[string]bool{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if err := requests.Close(); err != nil {
		t.Fatal(err)
	}
	report := Verify(configPath, cfg)
	if !report.Healthy || len(report.Checks) != 5 || report.Checks[1].Status != "pass" || report.Checks[2].Entries != 1 || report.Checks[3].Status != "warn" {
		t.Fatalf("恢复检查异常: %+v", report)
	}
}

func TestVerifyRejectsCorruptedJournal(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Default()
	cfg.Persistence.Directory = directory
	cfg.Capture.StorageDir = directory
	configPath := filepath.Join(directory, "config.yaml")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "requests.jsonl"), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Verify(configPath, cfg)
	if report.Healthy {
		t.Fatalf("损坏日志不应通过恢复检查: %+v", report)
	}
}
