package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
)

type Check struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Status         string         `json:"status"`
	Message        string         `json:"message"`
	NameCode       string         `json:"nameCode,omitempty"`
	MessageCode    string         `json:"messageCode,omitempty"`
	MessageDetails map[string]any `json:"messageDetails,omitempty"`
}

type Report struct {
	GeneratedAt   time.Time `json:"generatedAt"`
	Version       string    `json:"version"`
	Uptime        string    `json:"uptime"`
	UptimeSeconds int64     `json:"uptimeSeconds"`
	Healthy       bool      `json:"healthy"`
	Checks        []Check   `json:"checks"`
}

type Service struct {
	store     *config.Store
	version   string
	startedAt time.Time
}

func New(store *config.Store, version string, startedAt time.Time) *Service {
	return &Service{store: store, version: version, startedAt: startedAt}
}

func (s *Service) Run(ctx context.Context, locales ...string) Report {
	cfg := s.store.Get()
	locale, fallback := cfg.Localization.DefaultLocale, cfg.Localization.FallbackLocale
	if len(locales) > 0 && l10n.IsSupported(locales[0]) {
		locale = locales[0]
	}
	if len(locales) > 1 && l10n.IsSupported(locales[1]) {
		fallback = locales[1]
	}
	checks := []Check{passed("service", "diagnostic.service.name", "diagnostic.service.running", nil)}
	checks = append(checks, s.checkConfig(cfg), s.checkConfigFile(), s.checkAdminKey())
	checks = append(checks, s.checkUpstream(ctx, cfg), s.checkCache(cfg), s.checkDisk(cfg))
	healthy := true
	for _, check := range checks {
		if check.Status == "fail" {
			healthy = false
			break
		}
	}
	for index := range checks {
		localizeCheck(&checks[index], locale, fallback)
	}
	uptime := time.Since(s.startedAt).Round(time.Second)
	return Report{
		GeneratedAt: time.Now(), Version: s.version,
		Uptime: uptime.String(), UptimeSeconds: int64(uptime.Seconds()), Healthy: healthy, Checks: checks,
	}
}

func (s *Service) checkConfig(cfg config.Config) Check {
	if err := cfg.Validate(); err != nil {
		return failed("config", "diagnostic.config.name", "diagnostic.config.invalid", map[string]any{"Error": l10n.Default.Error(cfg.Localization.DefaultLocale, cfg.Localization.FallbackLocale, err)})
	}
	return passed("config", "diagnostic.config.name", "diagnostic.config.valid", nil)
}

func (s *Service) checkConfigFile() Check {
	path := s.store.Path()
	if path == "" {
		return warning("config_file", "diagnostic.config_file.name", "diagnostic.config_file.memory", nil)
	}
	if _, err := os.Stat(path); err != nil {
		return failed("config_file", "diagnostic.config_file.name", "diagnostic.config_file.unreadable", nil)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return failed("config_file", "diagnostic.config_file.name", "diagnostic.config_file.unwritable", nil)
	}
	file.Close()
	return passed("config_file", "diagnostic.config_file.name", "diagnostic.config_file.ready", nil)
}

func (s *Service) checkAdminKey() Check {
	if len(os.Getenv("RELAY_LIFELINE_ADMIN_KEY")) < 24 {
		return failed("admin_key", "diagnostic.admin_key.name", "diagnostic.admin_key.invalid", nil)
	}
	return passed("admin_key", "diagnostic.admin_key.name", "diagnostic.admin_key.valid", nil)
}

func (s *Service) checkUpstream(ctx context.Context, cfg config.Config) Check {
	target, err := url.Parse(cfg.Upstream.BaseURL)
	if err != nil || target.Hostname() == "" {
		return failed("upstream", "diagnostic.upstream.name", "diagnostic.upstream.invalid", nil)
	}
	port := target.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[target.Scheme]
	}
	timeout := min(cfg.Upstream.ConnectTimeout.Duration, 5*time.Second)
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(target.Hostname(), port))
	if err != nil {
		return failed("upstream", "diagnostic.upstream.name", "diagnostic.upstream.failed", nil)
	}
	connection.Close()
	return passed("upstream", "diagnostic.upstream.name", "diagnostic.upstream.ready", nil)
}

func (s *Service) checkCache(cfg config.Config) Check {
	directory := cfg.Stream.TempDir
	if directory == "" {
		directory = os.TempDir()
	}
	file, err := os.CreateTemp(directory, "relay-lifeline-diagnostic-*")
	if err != nil {
		return failed("cache", "diagnostic.cache.name", "diagnostic.cache.create_failed", nil)
	}
	name := file.Name()
	defer os.Remove(name)
	if err := verifyCacheFile(file, name); err != nil {
		var localized *l10n.Error
		if errors.As(err, &localized) {
			return failed("cache", "diagnostic.cache.name", localized.ID, localized.Data)
		}
		return failed("cache", "diagnostic.cache.name", "diagnostic.cache.read_failed", nil)
	}
	return passed("cache", "diagnostic.cache.name", "diagnostic.cache.ready", nil)
}

func verifyCacheFile(file *os.File, name string) error {
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return l10n.E("diagnostic.cache.permission_failed", err)
	}
	if _, err := file.WriteString("diagnostic"); err != nil {
		file.Close()
		return l10n.E("diagnostic.cache.write_failed", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return l10n.E("diagnostic.cache.sync_failed", err)
	}
	if err := file.Close(); err != nil {
		return l10n.E("diagnostic.cache.close_failed", err)
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0o600 {
		return l10n.E("diagnostic.cache.mode_invalid", err)
	}
	if _, err := os.ReadFile(name); err != nil {
		return l10n.E("diagnostic.cache.read_failed", err)
	}
	return nil
}

func (s *Service) checkDisk(cfg config.Config) Check {
	directory := cfg.Stream.TempDir
	if directory == "" {
		directory = os.TempDir()
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(filepath.Clean(directory), &stats); err != nil {
		return failed("disk", "diagnostic.disk.name", "diagnostic.disk.read_failed", nil)
	}
	available := int64(stats.Bavail) * int64(stats.Bsize)
	if available < int64(cfg.Risk.MinimumFreeDisk) {
		return failed("disk", "diagnostic.disk.name", "diagnostic.disk.low", map[string]any{"Minimum": config.FormatByteSize(int64(cfg.Risk.MinimumFreeDisk))})
	}
	return passed("disk", "diagnostic.disk.name", "diagnostic.disk.ready", map[string]any{"Available": formatApproxByteSize(available)})
}

func RedactedConfig(cfg config.Config) config.Config {
	cfg.Upstream.BaseURL = redactURL(cfg.Upstream.BaseURL)
	if cfg.Notifications.WebhookURL != "" {
		cfg.Notifications.WebhookURL = "[configured]"
	}
	return cfg
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid-url]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func passed(id, nameCode, messageCode string, details map[string]any) Check {
	return Check{ID: id, NameCode: nameCode, Status: "pass", MessageCode: messageCode, MessageDetails: details}
}

func warning(id, nameCode, messageCode string, details map[string]any) Check {
	return Check{ID: id, NameCode: nameCode, Status: "warn", MessageCode: messageCode, MessageDetails: details}
}

func failed(id, nameCode, messageCode string, details map[string]any) Check {
	return Check{ID: id, NameCode: nameCode, Status: "fail", MessageCode: messageCode, MessageDetails: details}
}

func localizeCheck(check *Check, locale, fallback string) {
	check.Name = l10n.Default.Text(locale, fallback, l10n.M(check.NameCode))
	check.Message = l10n.Default.Text(locale, fallback, l10n.M(check.MessageCode, check.MessageDetails))
}

func formatApproxByteSize(value int64) string {
	for _, unit := range []struct {
		name   string
		factor int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if value >= unit.factor {
			return fmt.Sprintf("%.1f%s", float64(value)/float64(unit.factor), unit.name)
		}
	}
	return fmt.Sprintf("%dB", value)
}
