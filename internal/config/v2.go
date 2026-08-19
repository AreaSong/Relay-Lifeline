package config

import (
	"net"
	"net/url"
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
	TrackUncertainDelivery    bool     `yaml:"track-uncertain-delivery" json:"trackUncertainDelivery"`
	PreserveIdempotencyKey    bool     `yaml:"preserve-idempotency-key" json:"preserveIdempotencyKey"`
	GenerateIdempotencyKey    bool     `yaml:"generate-idempotency-key" json:"generateIdempotencyKey"`
	AllowUncertainRetry       bool     `yaml:"allow-uncertain-retry" json:"allowUncertainRetry"`
	AllowCrossDomainFailover  bool     `yaml:"allow-cross-domain-failover" json:"allowCrossDomainFailover"`
	UncertainResolutionTarget Duration `yaml:"uncertain-resolution-target" json:"uncertainResolutionTarget"`
	MaxRequestDuration        Duration `yaml:"max-request-duration" json:"maxRequestDuration"`
	ClientDisconnectPolicy    string   `yaml:"client-disconnect-policy" json:"clientDisconnectPolicy"`
}

type ManagementSecurityConfig struct {
	LocalAccessEnabled     bool       `yaml:"local-access-enabled" json:"localAccessEnabled"`
	LoginFailuresPerMinute int        `yaml:"login-failures-per-minute" json:"loginFailuresPerMinute"`
	LoginCooldown          Duration   `yaml:"login-cooldown" json:"loginCooldown"`
	SessionIdleTimeout     Duration   `yaml:"session-idle-timeout" json:"sessionIdleTimeout"`
	SessionMaxLifetime     Duration   `yaml:"session-max-lifetime" json:"sessionMaxLifetime"`
	OIDC                   OIDCConfig `yaml:"oidc" json:"oidc"`
}

type OIDCConfig struct {
	Enabled           bool     `yaml:"enabled" json:"enabled"`
	IssuerURL         string   `yaml:"issuer-url" json:"issuerUrl"`
	ClientID          string   `yaml:"client-id" json:"clientId"`
	RedirectURL       string   `yaml:"redirect-url" json:"redirectUrl"`
	Scopes            []string `yaml:"scopes" json:"scopes"`
	SigningAlgorithms []string `yaml:"signing-algorithms" json:"signingAlgorithms"`
	RoleClaim         string   `yaml:"role-claim" json:"roleClaim"`
	ViewerValues      []string `yaml:"viewer-values" json:"viewerValues"`
	OperatorValues    []string `yaml:"operator-values" json:"operatorValues"`
	SensitiveValues   []string `yaml:"sensitive-values" json:"sensitiveValues"`
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
			AllowUncertainRetry:       false,
			UncertainResolutionTarget: duration(15 * time.Minute),
			MaxRequestDuration:        duration(0), ClientDisconnectPolicy: "cancel",
		}, ManagementSecurityConfig{
			LocalAccessEnabled: true, LoginFailuresPerMinute: 5, LoginCooldown: duration(30 * time.Second),
			SessionIdleTimeout: duration(30 * time.Minute), SessionMaxLifetime: duration(8 * time.Hour), OIDC: defaultOIDCConfig(),
		}, MetricsExportConfig{Path: "/metrics"}
}

func defaultOIDCConfig() OIDCConfig {
	return OIDCConfig{RoleClaim: "groups", SigningAlgorithms: []string{"RS256"}}
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
	if c.Lifecycle.MaxRequestDuration.Duration < 0 || c.Lifecycle.UncertainResolutionTarget.Duration < 0 || c.Lifecycle.ClientDisconnectPolicy != "cancel" && c.Lifecycle.ClientDisconnectPolicy != "finish-attempt" {
		problems = append(problems, l10n.E("config.lifecycle.invalid", nil))
	}
	if c.ManagementSecurity.LoginFailuresPerMinute < 1 || c.ManagementSecurity.LoginFailuresPerMinute > 100 || c.ManagementSecurity.LoginCooldown.Duration <= 0 || c.ManagementSecurity.SessionIdleTimeout.Duration <= 0 || c.ManagementSecurity.SessionMaxLifetime.Duration <= 0 {
		problems = append(problems, l10n.E("config.management_security.invalid", nil))
	}
	if !c.ManagementSecurity.LocalAccessEnabled && !c.ManagementSecurity.OIDC.Enabled {
		problems = append(problems, l10n.E("config.management_security.authentication_required", nil))
	}
	problems = append(problems, validateOIDCConfig(c.ManagementSecurity.OIDC)...)
	if c.MetricsExport.Path == "" || !strings.HasPrefix(c.MetricsExport.Path, "/") || strings.HasPrefix(c.MetricsExport.Path, "/admin") || c.MetricsExport.Path == "/" {
		problems = append(problems, l10n.E("config.metrics_export.path_invalid", nil))
	}
	return problems
}

func (c LifecycleConfig) EffectiveUncertainResolutionTarget() time.Duration {
	if c.UncertainResolutionTarget.Duration > 0 {
		return c.UncertainResolutionTarget.Duration
	}
	return 15 * time.Minute
}

func validateOIDCConfig(cfg OIDCConfig) []error {
	if !cfg.Enabled {
		return nil
	}
	var problems []error
	if !secureOIDCURL(cfg.IssuerURL, false) || !secureOIDCURL(cfg.RedirectURL, true) || strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.RoleClaim) == "" {
		problems = append(problems, l10n.E("config.management_security.oidc_invalid", nil))
	}
	allValues := make(map[string]struct{})
	for _, values := range [][]string{cfg.ViewerValues, cfg.OperatorValues, cfg.SensitiveValues} {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				problems = append(problems, l10n.E("config.management_security.oidc_mapping_invalid", nil))
				continue
			}
			if _, exists := allValues[value]; exists {
				problems = append(problems, l10n.E("config.management_security.oidc_mapping_invalid", nil))
			}
			allValues[value] = struct{}{}
		}
	}
	if len(allValues) == 0 {
		problems = append(problems, l10n.E("config.management_security.oidc_mapping_invalid", nil))
	}
	seenScopes := make(map[string]struct{})
	for _, scope := range cfg.Scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || strings.ContainsAny(scope, " \t\r\n") {
			problems = append(problems, l10n.E("config.management_security.oidc_scope_invalid", nil))
			continue
		}
		if _, exists := seenScopes[scope]; exists {
			problems = append(problems, l10n.E("config.management_security.oidc_scope_invalid", nil))
		}
		seenScopes[scope] = struct{}{}
	}
	allowedAlgorithms := map[string]bool{"RS256": true, "RS384": true, "RS512": true, "PS256": true, "PS384": true, "PS512": true, "ES256": true, "ES384": true, "ES512": true}
	if len(cfg.SigningAlgorithms) == 0 {
		problems = append(problems, l10n.E("config.management_security.oidc_algorithm_invalid", nil))
	}
	seenAlgorithms := make(map[string]struct{})
	for _, algorithm := range cfg.SigningAlgorithms {
		if !allowedAlgorithms[algorithm] {
			problems = append(problems, l10n.E("config.management_security.oidc_algorithm_invalid", nil))
		}
		if _, exists := seenAlgorithms[algorithm]; exists {
			problems = append(problems, l10n.E("config.management_security.oidc_algorithm_invalid", nil))
		}
		seenAlgorithms[algorithm] = struct{}{}
	}
	return problems
}

func secureOIDCURL(raw string, callback bool) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Fragment != "" || parsed.RawQuery != "" {
		return false
	}
	if callback && parsed.Path != "/admin/api/session/oidc/callback" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := parsed.Hostname()
	return parsed.Scheme == "http" && (host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
