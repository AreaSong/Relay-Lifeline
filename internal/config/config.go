package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
	"gopkg.in/yaml.v3"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return l10n.E("config.duration_yaml_invalid", err, map[string]any{"Value": value.Value})
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return l10n.E("config.duration_json_string", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return l10n.E("config.duration_invalid", err, map[string]any{"Value": value})
	}
	d.Duration = parsed
	return nil
}

type ByteSize int64

func (b *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := ParseByteSize(value.Value)
	if err != nil {
		return err
	}
	*b = ByteSize(parsed)
	return nil
}

func (b ByteSize) MarshalYAML() (any, error) {
	return FormatByteSize(int64(b)), nil
}

func (b ByteSize) MarshalJSON() ([]byte, error) { return json.Marshal(FormatByteSize(int64(b))) }

func (b *ByteSize) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(bytes.TrimSpace(data), &value); err != nil {
		return l10n.E("config.bytes_json_string", err)
	}
	parsed, err := ParseByteSize(value)
	if err != nil {
		return err
	}
	*b = ByteSize(parsed)
	return nil
}

type Config struct {
	Server        ServerConfig        `yaml:"server" json:"server"`
	Upstream      UpstreamConfig      `yaml:"upstream" json:"upstream"`
	Retry         RetryConfig         `yaml:"retry" json:"retry"`
	Stream        StreamConfig        `yaml:"stream" json:"stream"`
	Queue         QueueConfig         `yaml:"queue" json:"queue"`
	History       HistoryConfig       `yaml:"history" json:"history"`
	Observability ObservabilityConfig `yaml:"observability" json:"observability"`
	Risk          RiskConfig          `yaml:"risk" json:"risk"`
	Localization  LocalizationConfig  `yaml:"localization" json:"localization"`
	Notifications NotificationConfig  `yaml:"notifications" json:"notifications"`
	Logging       LoggingConfig       `yaml:"logging" json:"logging"`
}

type ServerConfig struct {
	Listen            string   `yaml:"listen" json:"listen"`
	AdminEnabled      bool     `yaml:"admin-enabled" json:"adminEnabled"`
	ReadHeaderTimeout Duration `yaml:"read-header-timeout" json:"readHeaderTimeout"`
	ShutdownTimeout   Duration `yaml:"shutdown-timeout" json:"shutdownTimeout"`
	MaxRequestBody    ByteSize `yaml:"max-request-body" json:"maxRequestBody"`
}

type UpstreamConfig struct {
	BaseURL               string   `yaml:"base-url" json:"baseUrl"`
	ConnectTimeout        Duration `yaml:"connect-timeout" json:"connectTimeout"`
	ResponseHeaderTimeout Duration `yaml:"response-header-timeout" json:"responseHeaderTimeout"`
}

type RetryConfig struct {
	Enabled         bool     `yaml:"enabled" json:"enabled"`
	Mode            string   `yaml:"mode" json:"mode"`
	MinInterval     Duration `yaml:"min-interval" json:"minInterval"`
	MaxInterval     Duration `yaml:"max-interval" json:"maxInterval"`
	MaxAttempts     int      `yaml:"max-attempts" json:"maxAttempts"`
	HonorRetryAfter bool     `yaml:"honor-retry-after" json:"honorRetryAfter"`
}

type StreamConfig struct {
	HeartbeatInterval Duration `yaml:"heartbeat-interval" json:"heartbeatInterval"`
	MemoryLimit       ByteSize `yaml:"memory-limit" json:"memoryLimit"`
	TempDir           string   `yaml:"temp-dir" json:"tempDir"`
}

type QueueConfig struct {
	MaxActive       int      `yaml:"max-active" json:"maxActive"`
	MaxWaiting      int      `yaml:"max-waiting" json:"maxWaiting"`
	RecoverySpacing Duration `yaml:"recovery-spacing" json:"recoverySpacing"`
}

type HistoryConfig struct {
	MaxItems  int      `yaml:"max-items" json:"maxItems"`
	Retention Duration `yaml:"retention" json:"retention"`
}

type ObservabilityConfig struct {
	ErrorDetails   string   `yaml:"error-details" json:"errorDetails"`
	MaxErrorDetail ByteSize `yaml:"max-error-detail" json:"maxErrorDetail"`
}

type RiskConfig struct {
	WarningAfter        Duration `yaml:"warning-after" json:"warningAfter"`
	WarningAttempts     int      `yaml:"warning-attempts" json:"warningAttempts"`
	AuthErrorAttempts   int      `yaml:"auth-error-attempts" json:"authErrorAttempts"`
	QueueWarningPercent int      `yaml:"queue-warning-percent" json:"queueWarningPercent"`
	MinimumFreeDisk     ByteSize `yaml:"minimum-free-disk" json:"minimumFreeDisk"`
}

type LocalizationConfig struct {
	DefaultLocale  string `yaml:"default-locale" json:"defaultLocale"`
	FallbackLocale string `yaml:"fallback-locale" json:"fallbackLocale"`
}

type NotificationConfig struct {
	StalledAfter     Duration `yaml:"stalled-after" json:"stalledAfter"`
	NotifyOnRecovery bool     `yaml:"notify-on-recovery" json:"notifyOnRecovery"`
	WebhookURL       string   `yaml:"webhook-url" json:"webhookUrl"`
	DeliveryAttempts int      `yaml:"delivery-attempts" json:"deliveryAttempts"`
	DeliveryBackoff  Duration `yaml:"delivery-backoff" json:"deliveryBackoff"`
	EventTypes       []string `yaml:"event-types" json:"eventTypes"`
	Locale           string   `yaml:"locale" json:"locale"`
}

type LoggingConfig struct {
	Level            string `yaml:"level" json:"level"`
	Locale           string `yaml:"locale" json:"locale"`
	LogRequestBody   bool   `yaml:"log-request-body" json:"logRequestBody"`
	LogResponseBody  bool   `yaml:"log-response-body" json:"logResponseBody"`
	LogAuthorization bool   `yaml:"log-authorization" json:"logAuthorization"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{
			Listen: "127.0.0.1:8318", AdminEnabled: true,
			ReadHeaderTimeout: duration(10 * time.Second),
			ShutdownTimeout:   duration(15 * time.Second),
			MaxRequestBody:    ByteSize(32 << 20),
		},
		Upstream: UpstreamConfig{
			BaseURL:               "http://cli-proxy-api:8317",
			ConnectTimeout:        duration(10 * time.Second),
			ResponseHeaderTimeout: duration(30 * time.Second),
		},
		Retry: RetryConfig{
			Enabled: true, Mode: "all-errors",
			MinInterval: duration(60 * time.Second), MaxInterval: duration(120 * time.Second),
			HonorRetryAfter: true,
		},
		Stream: StreamConfig{
			HeartbeatInterval: duration(15 * time.Second), MemoryLimit: ByteSize(64 << 20),
		},
		Queue: QueueConfig{
			MaxActive: 8, MaxWaiting: 100, RecoverySpacing: duration(2 * time.Second),
		},
		History:       HistoryConfig{MaxItems: 500, Retention: duration(24 * time.Hour)},
		Observability: ObservabilityConfig{ErrorDetails: "safe", MaxErrorDetail: ByteSize(2 << 10)},
		Risk: RiskConfig{
			WarningAfter: duration(15 * time.Minute), WarningAttempts: 10,
			AuthErrorAttempts: 3, QueueWarningPercent: 80, MinimumFreeDisk: ByteSize(512 << 20),
		},
		Localization: LocalizationConfig{DefaultLocale: l10n.LocaleChinese, FallbackLocale: l10n.LocaleEnglish},
		Notifications: NotificationConfig{
			StalledAfter: duration(10 * time.Minute), NotifyOnRecovery: true,
			DeliveryAttempts: 3, DeliveryBackoff: duration(5 * time.Second),
			EventTypes: []string{"stalled", "recovered", "long_running", "many_attempts", "auth_errors", "queue_pressure", "disk_pressure"},
			Locale:     l10n.LocaleChinese,
		},
		Logging: LoggingConfig{Level: "info", Locale: l10n.LocaleChinese},
	}
}

func duration(value time.Duration) Duration { return Duration{Duration: value} }

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, l10n.E("config.read_failed", err, map[string]any{"Error": err.Error()})
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, l10n.E("config.parse_failed", err, map[string]any{"Error": err.Error()})
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var problems []error
	if strings.TrimSpace(c.Server.Listen) == "" {
		problems = append(problems, l10n.E("config.server.listen_required", nil))
	}
	parsed, err := url.Parse(c.Upstream.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		problems = append(problems, l10n.E("config.upstream.url_invalid", nil))
	} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
		problems = append(problems, l10n.E("config.upstream.scheme_invalid", nil))
	}
	if c.Upstream.ConnectTimeout.Duration <= 0 || c.Upstream.ResponseHeaderTimeout.Duration <= 0 {
		problems = append(problems, l10n.E("config.upstream.timeout_invalid", nil))
	}
	if c.Retry.Mode != "all-errors" && c.Retry.Mode != "transient-errors" {
		problems = append(problems, l10n.E("config.retry.mode_invalid", nil))
	}
	if c.Retry.MinInterval.Duration <= 0 || c.Retry.MaxInterval.Duration < c.Retry.MinInterval.Duration {
		problems = append(problems, l10n.E("config.retry.interval_invalid", nil))
	}
	if c.Retry.MaxAttempts < 0 {
		problems = append(problems, l10n.E("config.retry.max_attempts", nil))
	}
	if c.Stream.HeartbeatInterval.Duration <= 0 || c.Stream.MemoryLimit < 1<<20 {
		problems = append(problems, l10n.E("config.stream.invalid", nil))
	}
	if c.Server.MaxRequestBody < 1<<20 {
		problems = append(problems, l10n.E("config.server.body_limit", nil))
	}
	if c.Queue.MaxActive < 1 || c.Queue.MaxWaiting < 0 || c.Queue.RecoverySpacing.Duration < 0 {
		problems = append(problems, l10n.E("config.queue.invalid", nil))
	}
	if c.History.MaxItems < 1 || c.History.Retention.Duration <= 0 {
		problems = append(problems, l10n.E("config.history.invalid", nil))
	}
	if c.Observability.ErrorDetails != "off" && c.Observability.ErrorDetails != "safe" {
		problems = append(problems, l10n.E("config.observability.mode_invalid", nil))
	}
	if c.Observability.MaxErrorDetail < 256 || c.Observability.MaxErrorDetail > 64<<10 {
		problems = append(problems, l10n.E("config.observability.limit_invalid", nil))
	}
	if c.Risk.WarningAfter.Duration <= 0 || c.Risk.WarningAttempts < 1 || c.Risk.AuthErrorAttempts < 1 {
		problems = append(problems, l10n.E("config.risk.threshold_invalid", nil))
	}
	if c.Risk.QueueWarningPercent < 1 || c.Risk.QueueWarningPercent > 100 || c.Risk.MinimumFreeDisk < 1<<20 {
		problems = append(problems, l10n.E("config.risk.capacity_invalid", nil))
	}
	if c.Notifications.DeliveryAttempts < 1 || c.Notifications.DeliveryAttempts > 10 || c.Notifications.DeliveryBackoff.Duration <= 0 {
		problems = append(problems, l10n.E("config.notification.delivery_invalid", nil))
	}
	validEvents := map[string]bool{
		"stalled": true, "recovered": true, "long_running": true, "many_attempts": true,
		"auth_errors": true, "queue_pressure": true, "disk_pressure": true,
	}
	for _, eventType := range c.Notifications.EventTypes {
		if !validEvents[eventType] {
			problems = append(problems, l10n.E("config.notification.event_invalid", nil, map[string]any{"Event": eventType}))
		}
	}
	if c.Notifications.WebhookURL != "" {
		webhook, parseErr := url.Parse(c.Notifications.WebhookURL)
		if parseErr != nil || (webhook.Scheme != "http" && webhook.Scheme != "https") {
			problems = append(problems, l10n.E("config.notification.url_invalid", nil))
		}
	}
	if c.Logging.LogRequestBody || c.Logging.LogResponseBody || c.Logging.LogAuthorization {
		problems = append(problems, l10n.E("config.logging.sensitive", nil))
	}
	locales := []struct {
		field string
		value string
	}{
		{"localization.default-locale", c.Localization.DefaultLocale},
		{"localization.fallback-locale", c.Localization.FallbackLocale},
		{"logging.locale", c.Logging.Locale},
		{"notifications.locale", c.Notifications.Locale},
	}
	for _, locale := range locales {
		if !l10n.IsSupported(locale.value) {
			problems = append(problems, l10n.E("config.locale.invalid", nil, map[string]any{"Field": locale.field}))
		}
	}
	return errors.Join(problems...)
}

func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return l10n.E("config.parse_failed", err, map[string]any{"Error": err.Error()})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return l10n.E("config.write_failed", err, map[string]any{"Error": err.Error()})
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".relay-lifeline-*.yaml")
	if err != nil {
		return writeBoundFile(path, data, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		// 单文件 Docker bind mount 不允许 rename，退化为就地安全写入。
		return writeBoundFile(path, data, err)
	}
	return nil
}

func writeBoundFile(path string, data []byte, cause error) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return l10n.E("config.write_failed", errors.Join(cause, err), map[string]any{"Error": err.Error()})
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return l10n.E("config.write_failed", err, map[string]any{"Error": err.Error()})
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func NewStore(path string, cfg Config) *Store { return &Store{path: path, cfg: cfg} }

func (s *Store) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) Update(cfg Config, persist bool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if persist {
		if err := cfg.Save(s.path); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

func (s *Store) Reload() error {
	cfg, err := Load(s.path)
	if err != nil {
		return err
	}
	return s.Update(cfg, false)
}

func ParseByteSize(raw string) (int64, error) {
	value := strings.TrimSpace(strings.ToUpper(raw))
	units := []struct {
		suffix string
		factor int64
	}{{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"GB", 1_000_000_000}, {"MB", 1_000_000}, {"KB", 1_000}, {"B", 1}}
	for _, unit := range units {
		if strings.HasSuffix(value, unit.suffix) {
			number := strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
			parsed, err := strconv.ParseFloat(number, 64)
			if err != nil || parsed < 0 {
				return 0, l10n.E("config.bytes_invalid", err, map[string]any{"Value": raw})
			}
			return int64(parsed * float64(unit.factor)), nil
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, l10n.E("config.bytes_invalid", err, map[string]any{"Value": raw})
	}
	return parsed, nil
}

func FormatByteSize(value int64) string {
	for _, unit := range []struct {
		name   string
		factor int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if value >= unit.factor && value%unit.factor == 0 {
			return fmt.Sprintf("%d%s", value/unit.factor, unit.name)
		}
	}
	return fmt.Sprintf("%dB", value)
}
