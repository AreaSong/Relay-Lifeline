package config

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
)

type PersistenceConfig struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	Directory  string   `yaml:"directory" json:"directory"`
	Retention  Duration `yaml:"retention" json:"retention"`
	SyncWrites bool     `yaml:"sync-writes" json:"syncWrites"`
}

type IncidentConfig struct {
	Enabled              bool     `yaml:"enabled" json:"enabled"`
	CorrelationWindow    Duration `yaml:"correlation-window" json:"correlationWindow"`
	RecoveryStableWindow Duration `yaml:"recovery-stable-window" json:"recoveryStableWindow"`
	Retention            Duration `yaml:"retention" json:"retention"`
	MaxItems             int      `yaml:"max-items" json:"maxItems"`
}

type LifecycleConfig struct {
	TrackUncertainDelivery bool     `yaml:"track-uncertain-delivery" json:"trackUncertainDelivery"`
	PreserveIdempotencyKey bool     `yaml:"preserve-idempotency-key" json:"preserveIdempotencyKey"`
	GenerateIdempotencyKey bool     `yaml:"generate-idempotency-key" json:"generateIdempotencyKey"`
	MaxRequestDuration     Duration `yaml:"max-request-duration" json:"maxRequestDuration"`
	ClientDisconnectPolicy string   `yaml:"client-disconnect-policy" json:"clientDisconnectPolicy"`
}

type ManagementSecurityConfig struct {
	LoginFailuresPerMinute int      `yaml:"login-failures-per-minute" json:"loginFailuresPerMinute"`
	LoginCooldown          Duration `yaml:"login-cooldown" json:"loginCooldown"`
	SessionIdleTimeout     Duration `yaml:"session-idle-timeout" json:"sessionIdleTimeout"`
}

type MetricsExportConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Path    string `yaml:"path" json:"path"`
}

func defaultV2Config() (PersistenceConfig, IncidentConfig, LifecycleConfig, ManagementSecurityConfig, MetricsExportConfig) {
	return PersistenceConfig{
			Enabled: true, Directory: "/var/lib/relay-lifeline/events", Retention: duration(14 * 24 * time.Hour), SyncWrites: true,
		}, IncidentConfig{
			Enabled: true, CorrelationWindow: duration(30 * time.Second), RecoveryStableWindow: duration(2 * time.Minute),
			Retention: duration(30 * 24 * time.Hour), MaxItems: 1000,
		}, LifecycleConfig{
			TrackUncertainDelivery: true, PreserveIdempotencyKey: true,
			MaxRequestDuration: duration(0), ClientDisconnectPolicy: "cancel",
		}, ManagementSecurityConfig{
			LoginFailuresPerMinute: 5, LoginCooldown: duration(30 * time.Second), SessionIdleTimeout: duration(30 * time.Minute),
		}, MetricsExportConfig{Path: "/metrics"}
}

func validateV2Config(c Config) []error {
	var problems []error
	if c.Persistence.Enabled && (strings.TrimSpace(c.Persistence.Directory) == "" || !filepath.IsAbs(c.Persistence.Directory)) {
		problems = append(problems, l10n.E("config.persistence.directory_invalid", nil))
	}
	if c.Persistence.Retention.Duration <= 0 || c.Persistence.Retention.Duration > 365*24*time.Hour {
		problems = append(problems, l10n.E("config.persistence.retention_invalid", nil))
	}
	if c.Incidents.CorrelationWindow.Duration <= 0 || c.Incidents.RecoveryStableWindow.Duration <= 0 || c.Incidents.Retention.Duration <= 0 || c.Incidents.MaxItems < 1 || c.Incidents.MaxItems > 100000 {
		problems = append(problems, l10n.E("config.incidents.invalid", nil))
	}
	if c.Lifecycle.MaxRequestDuration.Duration < 0 || c.Lifecycle.ClientDisconnectPolicy != "cancel" && c.Lifecycle.ClientDisconnectPolicy != "finish-attempt" {
		problems = append(problems, l10n.E("config.lifecycle.invalid", nil))
	}
	if c.ManagementSecurity.LoginFailuresPerMinute < 1 || c.ManagementSecurity.LoginFailuresPerMinute > 100 || c.ManagementSecurity.LoginCooldown.Duration <= 0 || c.ManagementSecurity.SessionIdleTimeout.Duration <= 0 {
		problems = append(problems, l10n.E("config.management_security.invalid", nil))
	}
	if c.MetricsExport.Path == "" || !strings.HasPrefix(c.MetricsExport.Path, "/") || strings.HasPrefix(c.MetricsExport.Path, "/admin") || c.MetricsExport.Path == "/" {
		problems = append(problems, l10n.E("config.metrics_export.path_invalid", nil))
	}
	return problems
}
