package config

import (
	"os"
	"path/filepath"
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
	if cfg.Server.Listen != "127.0.0.1:8318" || cfg.Retry.MinInterval.Duration != time.Second {
		t.Fatalf("配置合并异常: %+v", cfg)
	}
	if cfg.History.MaxItems != 500 || cfg.Observability.ErrorDetails != "safe" || cfg.Observability.MaxErrorDetail != 2<<10 || cfg.Risk.WarningAttempts != 10 || cfg.Notifications.DeliveryAttempts != 3 {
		t.Fatalf("v0.2 默认配置未合并: %+v", cfg)
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
