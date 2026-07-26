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

	"gopkg.in/yaml.v3"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("无效时间 %q: %w", value.Value, err)
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
		return errors.New("时间必须是字符串")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("无效时间 %q: %w", value, err)
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
		return errors.New("容量必须是字符串")
	}
	parsed, err := ParseByteSize(value)
	if err != nil {
		return err
	}
	*b = ByteSize(parsed)
	return nil
}

type Config struct {
	Server        ServerConfig       `yaml:"server" json:"server"`
	Upstream      UpstreamConfig     `yaml:"upstream" json:"upstream"`
	Retry         RetryConfig        `yaml:"retry" json:"retry"`
	Stream        StreamConfig       `yaml:"stream" json:"stream"`
	Queue         QueueConfig        `yaml:"queue" json:"queue"`
	Notifications NotificationConfig `yaml:"notifications" json:"notifications"`
	Logging       LoggingConfig      `yaml:"logging" json:"logging"`
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

type NotificationConfig struct {
	StalledAfter     Duration `yaml:"stalled-after" json:"stalledAfter"`
	NotifyOnRecovery bool     `yaml:"notify-on-recovery" json:"notifyOnRecovery"`
	WebhookURL       string   `yaml:"webhook-url" json:"webhookUrl"`
}

type LoggingConfig struct {
	Level            string `yaml:"level" json:"level"`
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
		Notifications: NotificationConfig{
			StalledAfter: duration(10 * time.Minute), NotifyOnRecovery: true,
		},
		Logging: LoggingConfig{Level: "info"},
	}
}

func duration(value time.Duration) Duration { return Duration{Duration: value} }

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置: %w", err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("解析配置: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var problems []error
	if strings.TrimSpace(c.Server.Listen) == "" {
		problems = append(problems, errors.New("server.listen 不能为空"))
	}
	parsed, err := url.Parse(c.Upstream.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		problems = append(problems, errors.New("upstream.base-url 必须是有效的 HTTP(S) URL"))
	} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
		problems = append(problems, errors.New("upstream.base-url 仅支持 http 或 https"))
	}
	if c.Upstream.ConnectTimeout.Duration <= 0 || c.Upstream.ResponseHeaderTimeout.Duration <= 0 {
		problems = append(problems, errors.New("上游超时必须大于 0"))
	}
	if c.Retry.Mode != "all-errors" && c.Retry.Mode != "transient-errors" {
		problems = append(problems, errors.New("retry.mode 只能是 all-errors 或 transient-errors"))
	}
	if c.Retry.MinInterval.Duration <= 0 || c.Retry.MaxInterval.Duration < c.Retry.MinInterval.Duration {
		problems = append(problems, errors.New("重试间隔必须大于 0，且最大值不能小于最小值"))
	}
	if c.Retry.MaxAttempts < 0 {
		problems = append(problems, errors.New("retry.max-attempts 不能小于 0"))
	}
	if c.Stream.HeartbeatInterval.Duration <= 0 || c.Stream.MemoryLimit < 1<<20 {
		problems = append(problems, errors.New("心跳必须大于 0，缓存至少为 1MiB"))
	}
	if c.Server.MaxRequestBody < 1<<20 {
		problems = append(problems, errors.New("请求体上限至少为 1MiB"))
	}
	if c.Queue.MaxActive < 1 || c.Queue.MaxWaiting < 0 || c.Queue.RecoverySpacing.Duration < 0 {
		problems = append(problems, errors.New("队列参数无效"))
	}
	if c.Notifications.WebhookURL != "" {
		webhook, parseErr := url.Parse(c.Notifications.WebhookURL)
		if parseErr != nil || (webhook.Scheme != "http" && webhook.Scheme != "https") {
			problems = append(problems, errors.New("notifications.webhook-url 必须是 HTTP(S) URL"))
		}
	}
	if c.Logging.LogRequestBody || c.Logging.LogResponseBody || c.Logging.LogAuthorization {
		problems = append(problems, errors.New("出于安全原因，不允许记录请求体、响应体或 Authorization"))
	}
	return errors.Join(problems...)
}

func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
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
		return fmt.Errorf("替换配置: %w", errors.Join(cause, err))
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("写入绑定配置: %w", err)
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
				return 0, fmt.Errorf("无效容量 %q", raw)
			}
			return int64(parsed * float64(unit.factor)), nil
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("无效容量 %q", raw)
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
