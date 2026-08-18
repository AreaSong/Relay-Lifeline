package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLoadMergesDefaultsAndValidates(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	content := []byte("upstream:\n  base-url: http://127.0.0.1:8317\nretry:\n  min-interval: 1s\n  max-interval: 2s\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:8318" || cfg.Retry.MinInterval.Duration != time.Second || cfg.Upstream.ResponseBodyIdleTimeout.Duration != 90*time.Second {
		t.Fatalf("配置合并异常: %+v", cfg)
	}
	if cfg.History.MaxItems != 500 || cfg.Observability.ErrorDetails != "safe" || cfg.Observability.MaxErrorDetail != 2<<10 || cfg.Risk.WarningAttempts != 10 || cfg.Notifications.DeliveryAttempts != 3 {
		t.Fatalf("v0.2 默认配置未合并: %+v", cfg)
	}
	if cfg.Stream.MaxResponseBody != 512<<20 || cfg.Stream.MaxTotalCache != 2<<30 {
		t.Fatalf("响应缓存保护默认值未合并: %+v", cfg.Stream)
	}
}

func TestExampleConfigurationsLoad(t *testing.T) {
	for _, name := range []string{"config.example.yaml", "config.docker.example.yaml"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatalf("示例配置无法加载: %v", err)
			}
			if cfg.Localization.DefaultLocale != "zh-CN" || cfg.Localization.FallbackLocale != "en-US" || cfg.Logging.Locale != "zh-CN" || cfg.Notifications.Locale != "zh-CN" {
				t.Fatalf("示例语言配置不完整: %+v", cfg)
			}
		})
	}
}

func TestConfigValidatesSafeErrorDetailSettings(t *testing.T) {
	cfg := Default()
	cfg.Observability.ErrorDetails = "raw"
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝 raw 错误详情模式")
	}
	cfg = Default()
	cfg.Observability.MaxErrorDetail = 128
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝过小的错误详情上限")
	}
}

func TestConfigRejectsUnknownNotificationEvent(t *testing.T) {
	cfg := Default()
	cfg.Notifications.EventTypes = append(cfg.Notifications.EventTypes, "unknown-event")
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝未知通知事件")
	}
}

func TestConfigRejectsSensitiveLogging(t *testing.T) {
	tests := []struct {
		name   string
		enable func(*LoggingConfig)
	}{
		{name: "请求体", enable: func(cfg *LoggingConfig) { cfg.LogRequestBody = true }},
		{name: "响应体", enable: func(cfg *LoggingConfig) { cfg.LogResponseBody = true }},
		{name: "Authorization", enable: func(cfg *LoggingConfig) { cfg.LogAuthorization = true }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.enable(&cfg.Logging)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("应拒绝记录%s", test.name)
			}
		})
	}
}

func TestConfigRejectsUnsupportedLocales(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
	}{
		{name: "默认语言", set: func(cfg *Config) { cfg.Localization.DefaultLocale = "fr-FR" }},
		{name: "回退语言", set: func(cfg *Config) { cfg.Localization.FallbackLocale = "fr-FR" }},
		{name: "日志语言", set: func(cfg *Config) { cfg.Logging.Locale = "fr-FR" }},
		{name: "通知语言", set: func(cfg *Config) { cfg.Notifications.Locale = "fr-FR" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.set(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("应拒绝不支持的语言")
			}
		})
	}
}

func TestByteSizeRoundTrip(t *testing.T) {
	value, err := ParseByteSize("64MiB")
	if err != nil || value != 64<<20 {
		t.Fatalf("解析失败: %d %v", value, err)
	}
	if formatted := FormatByteSize(value); formatted != "64MiB" {
		t.Fatalf("格式化失败: %s", formatted)
	}
}

func TestSaveFallsBackWhenDirectoryIsReadOnly(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	cfg := Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Retry.Mode != "all-errors" {
		t.Fatalf("回退写入失败: %v", err)
	}
}

func TestMigrateAddsCurrentSchemaAndRejectsFutureSchema(t *testing.T) {
	cfg := Default()
	cfg.SchemaVersion = 0
	migrated, err := Migrate(cfg)
	if err != nil || migrated.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("旧配置迁移失败: version=%d err=%v", migrated.SchemaVersion, err)
	}

	cfg.SchemaVersion = CurrentSchemaVersion + 1
	if _, err := Migrate(cfg); err == nil {
		t.Fatal("应拒绝未来版本的配置 schema")
	}
}

func TestMigrateSchema2AddsResponseCacheLimits(t *testing.T) {
	cfg := Default()
	cfg.SchemaVersion = 2
	cfg.Stream.MaxResponseBody = 0
	cfg.Stream.MaxTotalCache = 0
	migrated, err := Migrate(cfg)
	if err != nil || migrated.SchemaVersion != CurrentSchemaVersion || migrated.Stream.MaxResponseBody != 512<<20 || migrated.Stream.MaxTotalCache != 2<<30 {
		t.Fatalf("schema 2 缓存限制迁移异常: %+v err=%v", migrated.Stream, err)
	}
}

func TestConfigValidatesResponseCacheLimits(t *testing.T) {
	cfg := Default()
	cfg.Stream.MaxResponseBody = cfg.Stream.MemoryLimit - 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝小于内存阈值的单响应上限")
	}
	cfg = Default()
	cfg.Stream.MaxTotalCache = cfg.Stream.MaxResponseBody - 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝小于单响应上限的总缓存预算")
	}
	cfg = Default()
	cfg.Stream.TempDir = "relative/cache"
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝相对缓存目录")
	}
}

func TestLoadWithSourceVersionReportsMigrationOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte("schema-version: 1\nupstream:\n  base-url: http://127.0.0.1:8317\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, sourceVersion, err := LoadWithSourceVersion(path)
	if err != nil || sourceVersion != 1 || cfg.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("源 schema 识别异常: source=%d current=%d err=%v", sourceVersion, cfg.SchemaVersion, err)
	}
}

func TestPlanChangesSeparatesHotReloadAndRestartSections(t *testing.T) {
	before := Default()
	after := before
	after.Retry.MaxAttempts = 3
	after.Server.MaxRequestBody = 64 << 20
	plan := PlanChanges(before, after)
	if plan.RestartRequired || !slices.Equal(plan.ChangedSections, []string{"server", "retry"}) || !slices.Equal(plan.HotReloadSections, []string{"server", "retry"}) {
		t.Fatalf("热更新分类异常: %+v", plan)
	}

	after.Upstream.BaseURL = "http://127.0.0.1:8317"
	after.Capture.StorageDir = filepath.Join(t.TempDir(), "captures")
	plan = PlanChanges(before, after)
	if !plan.RestartRequired || !slices.Contains(plan.RestartSections, "upstream") || !slices.Contains(plan.RestartSections, "capture") {
		t.Fatalf("重启分类异常: %+v", plan)
	}
}

func TestStoreBacksUpConfigurationWithRestrictedPermissionsAndRetention(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	backupDirectory := filepath.Join(directory, "backups")
	cfg := Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	store := NewStore(path, cfg)
	for iteration := 0; iteration < 11; iteration++ {
		cfg.Retry.MaxAttempts = iteration + 1
		cfg.Server.ConfigBackupDir = backupDirectory
		result, updateErr := store.UpdateWithResult(cfg, true)
		if updateErr != nil {
			t.Fatal(updateErr)
		}
		if iteration == 0 {
			backup, readErr := os.ReadFile(result.BackupPath)
			if readErr != nil || !slices.Equal(backup, original) {
				t.Fatalf("首个备份内容异常: %v", readErr)
			}
		}
	}

	entries, err := os.ReadDir(backupDirectory)
	if err != nil || len(entries) != 10 {
		t.Fatalf("备份轮换异常: count=%d err=%v", len(entries), err)
	}
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("备份权限异常: %s mode=%v err=%v", entry.Name(), info.Mode().Perm(), statErr)
		}
	}
}
