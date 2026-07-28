package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/journal"
)

type ConfigBackup struct {
	Name          string    `json:"name"`
	ModifiedAt    time.Time `json:"modifiedAt"`
	Size          int64     `json:"sizeBytes"`
	SHA256        string    `json:"sha256,omitempty"`
	SchemaVersion int       `json:"schemaVersion,omitempty"`
	Valid         bool      `json:"valid"`
	Error         string    `json:"error,omitempty"`
}

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Path    string `json:"path,omitempty"`
	Size    int64  `json:"sizeBytes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Entries int    `json:"entries,omitempty"`
	Message string `json:"message,omitempty"`
}

type Report struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Healthy     bool      `json:"healthy"`
	Checks      []Check   `json:"checks"`
}

func Verify(configPath string, cfg config.Config) Report {
	checks := []Check{verifyConfig("config", configPath)}
	checks = append(checks, verifyLatestBackup(configPath, cfg.Server.ConfigBackupDir))
	if cfg.Persistence.Enabled {
		checks = append(checks,
			verifyJournal("requests_journal", filepath.Join(cfg.Persistence.Directory, "requests.jsonl")),
			verifyJournal("incidents_journal", filepath.Join(cfg.Persistence.Directory, "incidents.jsonl")),
		)
	}
	checks = append(checks, verifyDirectory("capture_storage", cfg.Capture.StorageDir))
	healthy := true
	for _, check := range checks {
		if check.Status == "fail" {
			healthy = false
		}
	}
	return Report{GeneratedAt: time.Now().UTC(), Healthy: healthy, Checks: checks}
}

// ConfigBackups returns metadata only; configuration contents are never exposed.
func ConfigBackups(configPath, configuredDirectory string) ([]ConfigBackup, error) {
	directory := configuredDirectory
	if directory == "" {
		directory = filepath.Join(filepath.Dir(configPath), ".relay-lifeline-backups")
	}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []ConfigBackup{}, nil
	}
	if err != nil {
		return nil, err
	}
	backups := make([]ConfigBackup, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "config-") || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, statErr := entry.Info()
		metadata := ConfigBackup{Name: entry.Name()}
		if statErr != nil {
			metadata.Error = statErr.Error()
			backups = append(backups, metadata)
			continue
		}
		metadata.ModifiedAt, metadata.Size = info.ModTime().UTC(), info.Size()
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			metadata.Error = readErr.Error()
			backups = append(backups, metadata)
			continue
		}
		digest := sha256.Sum256(data)
		metadata.SHA256 = hex.EncodeToString(digest[:])
		_, metadata.SchemaVersion, err = config.LoadWithSourceVersion(path)
		metadata.Valid = err == nil
		if err != nil {
			metadata.Error = err.Error()
		}
		backups = append(backups, metadata)
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].Name > backups[j].Name })
	return backups, nil
}

func verifyConfig(name, path string) Check {
	check := fileIdentity(name, path)
	if check.Status == "fail" {
		return check
	}
	if _, err := config.Load(path); err != nil {
		check.Status, check.Message = "fail", err.Error()
	}
	return check
}

func verifyLatestBackup(configPath, configuredDirectory string) Check {
	directory := configuredDirectory
	if directory == "" {
		directory = filepath.Join(filepath.Dir(configPath), ".relay-lifeline-backups")
	}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return Check{Name: "latest_config_backup", Status: "warn", Path: directory, Message: "no configuration backup exists yet"}
	}
	if err != nil {
		return Check{Name: "latest_config_backup", Status: "fail", Path: directory, Message: err.Error()}
	}
	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".yaml" {
			candidates = append(candidates, filepath.Join(directory, entry.Name()))
		}
	}
	if len(candidates) == 0 {
		return Check{Name: "latest_config_backup", Status: "warn", Path: directory, Message: "no configuration backup exists yet"}
	}
	sort.Strings(candidates)
	return verifyConfig("latest_config_backup", candidates[len(candidates)-1])
}

func verifyJournal(name, path string) Check {
	check := fileIdentity(name, path)
	if check.Status == "warn" {
		check.Message = "journal has not been created yet"
		return check
	}
	if check.Status == "fail" {
		return check
	}
	entries, err := journal.Verify(path)
	if err != nil {
		check.Status, check.Message = "fail", err.Error()
		return check
	}
	check.Entries = len(entries)
	return check
}

func verifyDirectory(name, path string) Check {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Check{Name: name, Status: "warn", Path: path, Message: "directory has not been created yet"}
	}
	if err != nil || !info.IsDir() {
		message := "path is not a directory"
		if err != nil {
			message = err.Error()
		}
		return Check{Name: name, Status: "fail", Path: path, Message: message}
	}
	return Check{Name: name, Status: "pass", Path: path}
}

func fileIdentity(name, path string) Check {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Check{Name: name, Status: "warn", Path: path}
	}
	if err != nil {
		return Check{Name: name, Status: "fail", Path: path, Message: err.Error()}
	}
	digest := sha256.Sum256(data)
	return Check{Name: name, Status: "pass", Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
}
