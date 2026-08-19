package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/egress"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"gopkg.in/yaml.v3"
)

const CurrentSchemaVersion = 5

var ErrRevisionConflict = errors.New("configuration desired revision conflict")

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
	SchemaVersion      int                      `yaml:"schema-version" json:"schemaVersion"`
	Server             ServerConfig             `yaml:"server" json:"server"`
	Upstream           UpstreamConfig           `yaml:"upstream" json:"upstream"`
	Upstreams          UpstreamPoolConfig       `yaml:"upstreams" json:"upstreams"`
	Egress             EgressConfig             `yaml:"egress" json:"egress"`
	Retry              RetryConfig              `yaml:"retry" json:"retry"`
	Stream             StreamConfig             `yaml:"stream" json:"stream"`
	Queue              QueueConfig              `yaml:"queue" json:"queue"`
	History            HistoryConfig            `yaml:"history" json:"history"`
	Observability      ObservabilityConfig      `yaml:"observability" json:"observability"`
	Capture            CaptureConfig            `yaml:"capture" json:"capture"`
	Risk               RiskConfig               `yaml:"risk" json:"risk"`
	Localization       LocalizationConfig       `yaml:"localization" json:"localization"`
	Notifications      NotificationConfig       `yaml:"notifications" json:"notifications"`
	Logging            LoggingConfig            `yaml:"logging" json:"logging"`
	Persistence        PersistenceConfig        `yaml:"persistence" json:"persistence"`
	Incidents          IncidentConfig           `yaml:"incidents" json:"incidents"`
	Lifecycle          LifecycleConfig          `yaml:"lifecycle" json:"lifecycle"`
	ManagementSecurity ManagementSecurityConfig `yaml:"management-security" json:"managementSecurity"`
	MetricsExport      MetricsExportConfig      `yaml:"metrics-export" json:"metricsExport"`
	Governance         GovernanceConfig         `yaml:"governance" json:"governance"`
	SLO                SLOConfig                `yaml:"slo" json:"slo"`
	TrafficPolicy      TrafficPolicyConfig      `yaml:"traffic-policy" json:"trafficPolicy"`
}

type EgressConfig struct {
	DenyPrivateNetworks bool     `yaml:"deny-private-networks" json:"denyPrivateNetworks"`
	AllowedHosts        []string `yaml:"allowed-hosts" json:"allowedHosts"`
}

type SLOConfig struct {
	Enabled               bool     `yaml:"enabled" json:"enabled"`
	AvailabilityTarget    float64  `yaml:"availability-target" json:"availabilityTarget"`
	RecoveryLatencyTarget Duration `yaml:"recovery-latency-target" json:"recoveryLatencyTarget"`
	Window                Duration `yaml:"window" json:"window"`
}

type TrafficPolicyConfig struct {
	Enabled       bool                  `yaml:"enabled" json:"enabled"`
	Mode          string                `yaml:"mode" json:"mode"`
	ReleaseStage  string                `yaml:"release-stage" json:"releaseStage"`
	CanaryPercent int                   `yaml:"canary-percent" json:"canaryPercent"`
	Revision      string                `yaml:"revision" json:"revision,omitempty"`
	Rules         []TrafficPolicyRule   `yaml:"rules" json:"rules"`
	Shadow        ShadowTrafficConfig   `yaml:"shadow" json:"shadow"`
	Adaptive      AdaptiveRoutingConfig `yaml:"adaptive" json:"adaptive"`
}

type TrafficPolicyRule struct {
	ID              string `yaml:"id" json:"id"`
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	Priority        int    `yaml:"priority" json:"priority"`
	Method          string `yaml:"method" json:"method"`
	PathPrefix      string `yaml:"path-prefix" json:"pathPrefix"`
	Model           string `yaml:"model" json:"model"`
	PrincipalPrefix string `yaml:"principal-prefix" json:"principalPrefix"`
	Action          string `yaml:"action" json:"action"`
	TargetID        string `yaml:"target-id" json:"targetId"`
}

type ShadowTrafficConfig struct {
	Enabled                 bool     `yaml:"enabled" json:"enabled"`
	TargetID                string   `yaml:"target-id" json:"targetId"`
	SamplePercent           int      `yaml:"sample-percent" json:"samplePercent"`
	MaxConcurrent           int      `yaml:"max-concurrent" json:"maxConcurrent"`
	MaxRequestBody          ByteSize `yaml:"max-request-body" json:"maxRequestBody"`
	RequestBudgetPerHour    int      `yaml:"request-budget-per-hour" json:"requestBudgetPerHour"`
	CostBudgetMicrosPerHour int64    `yaml:"cost-budget-micros-per-hour" json:"costBudgetMicrosPerHour"`
	CostReservationMicros   int64    `yaml:"cost-reservation-micros" json:"costReservationMicros"`
	RequireIdempotency      bool     `yaml:"require-idempotency" json:"requireIdempotency"`
}

type AdaptiveRoutingConfig struct {
	Enabled              bool     `yaml:"enabled" json:"enabled"`
	ErrorBudgetFloor     float64  `yaml:"error-budget-floor" json:"errorBudgetFloor"`
	MinimumObservations  int      `yaml:"minimum-observations" json:"minimumObservations"`
	MaximumLatencyMillis int64    `yaml:"maximum-latency-milliseconds" json:"maximumLatencyMilliseconds"`
	LatencyWeight        float64  `yaml:"latency-weight" json:"latencyWeight"`
	ErrorRateWeight      float64  `yaml:"error-rate-weight" json:"errorRateWeight"`
	CostWeight           float64  `yaml:"cost-weight" json:"costWeight"`
	CapabilityWeight     float64  `yaml:"capability-weight" json:"capabilityWeight"`
	SwitchCooldown       Duration `yaml:"switch-cooldown" json:"switchCooldown"`
	FallbackTargetID     string   `yaml:"fallback-target-id" json:"fallbackTargetId"`
	AutoStopBurnRate     float64  `yaml:"auto-stop-burn-rate" json:"autoStopBurnRate"`
	AutoStopFailureRate  float64  `yaml:"auto-stop-failure-rate" json:"autoStopFailureRate"`
}

type ServerConfig struct {
	Listen                     string   `yaml:"listen" json:"listen"`
	AdminEnabled               bool     `yaml:"admin-enabled" json:"adminEnabled"`
	ConfigBackupDir            string   `yaml:"config-backup-dir" json:"configBackupDir"`
	ReadHeaderTimeout          Duration `yaml:"read-header-timeout" json:"readHeaderTimeout"`
	ReadBodyTimeout            Duration `yaml:"read-body-timeout" json:"readBodyTimeout"`
	IdleTimeout                Duration `yaml:"idle-timeout" json:"idleTimeout"`
	DownstreamWriteIdleTimeout Duration `yaml:"downstream-write-idle-timeout" json:"downstreamWriteIdleTimeout"`
	ShutdownTimeout            Duration `yaml:"shutdown-timeout" json:"shutdownTimeout"`
	MaxHeaderBytes             int      `yaml:"max-header-bytes" json:"maxHeaderBytes"`
	MaxRequestBody             ByteSize `yaml:"max-request-body" json:"maxRequestBody"`
}

type UpstreamConfig struct {
	BaseURL                 string   `yaml:"base-url" json:"baseUrl"`
	ConnectTimeout          Duration `yaml:"connect-timeout" json:"connectTimeout"`
	ResponseHeaderTimeout   Duration `yaml:"response-header-timeout" json:"responseHeaderTimeout"`
	ResponseBodyIdleTimeout Duration `yaml:"response-body-idle-timeout" json:"responseBodyIdleTimeout"`
}

// UpstreamPoolConfig describes equivalent replicas of one logical Relay. The
// legacy Upstream field remains authoritative when Targets is empty.
type UpstreamPoolConfig struct {
	Strategy string                 `yaml:"strategy" json:"strategy"`
	Targets  []UpstreamTargetConfig `yaml:"targets" json:"targets"`
	Health   UpstreamHealthConfig   `yaml:"health" json:"health"`
	Circuit  UpstreamCircuitConfig  `yaml:"circuit" json:"circuit"`
}

type UpstreamTargetConfig struct {
	ID                string  `yaml:"id" json:"id"`
	BaseURL           string  `yaml:"base-url" json:"baseUrl"`
	Priority          int     `yaml:"priority" json:"priority"`
	Weight            int     `yaml:"weight" json:"weight"`
	MaxActive         int     `yaml:"max-active" json:"maxActive"`
	IdempotencyDomain string  `yaml:"idempotency-domain" json:"idempotencyDomain"`
	CostMicrosPer1K   int64   `yaml:"cost-micros-per-1k" json:"costMicrosPer1K"`
	CapabilityScore   float64 `yaml:"capability-score" json:"capabilityScore"`
}

type UpstreamHealthConfig struct {
	Mode string `yaml:"mode" json:"mode"`
}

type UpstreamCircuitConfig struct {
	Enabled         bool     `yaml:"enabled" json:"enabled"`
	MinimumRequests int      `yaml:"minimum-requests" json:"minimumRequests"`
	FailurePercent  int      `yaml:"failure-percent" json:"failurePercent"`
	OpenDuration    Duration `yaml:"open-duration" json:"openDuration"`
	HalfOpenMax     int      `yaml:"half-open-max" json:"halfOpenMax"`
}

type RetryConfig struct {
	Enabled         bool     `yaml:"enabled" json:"enabled"`
	Mode            string   `yaml:"mode" json:"mode"`
	MinInterval     Duration `yaml:"min-interval" json:"minInterval"`
	MaxInterval     Duration `yaml:"max-interval" json:"maxInterval"`
	MaxAttempts     int      `yaml:"max-attempts" json:"maxAttempts"`
	MaxElapsed      Duration `yaml:"max-elapsed" json:"maxElapsed"`
	RetryAfterCap   Duration `yaml:"retry-after-cap" json:"retryAfterCap"`
	HonorRetryAfter bool     `yaml:"honor-retry-after" json:"honorRetryAfter"`
}

type StreamConfig struct {
	HeartbeatInterval Duration `yaml:"heartbeat-interval" json:"heartbeatInterval"`
	MemoryLimit       ByteSize `yaml:"memory-limit" json:"memoryLimit"`
	MaxResponseBody   ByteSize `yaml:"max-response-body" json:"maxResponseBody"`
	MaxTotalCache     ByteSize `yaml:"max-total-cache" json:"maxTotalCache"`
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
	ErrorDetails   string          `yaml:"error-details" json:"errorDetails"`
	MaxErrorDetail ByteSize        `yaml:"max-error-detail" json:"maxErrorDetail"`
	Telemetry      TelemetryConfig `yaml:"telemetry" json:"telemetry"`
}

type TelemetryConfig struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	Protocol       string   `yaml:"protocol" json:"protocol"`
	Endpoint       string   `yaml:"endpoint" json:"endpoint"`
	Insecure       bool     `yaml:"insecure" json:"insecure"`
	SampleRatio    float64  `yaml:"sample-ratio" json:"sampleRatio"`
	ServiceName    string   `yaml:"service-name" json:"serviceName"`
	Environment    string   `yaml:"environment" json:"environment"`
	ExportTimeout  Duration `yaml:"export-timeout" json:"exportTimeout"`
	MetricInterval Duration `yaml:"metric-interval" json:"metricInterval"`
}

type CaptureConfig struct {
	Enabled               bool     `yaml:"enabled" json:"enabled"`
	StorageDir            string   `yaml:"storage-dir" json:"storageDir"`
	Retention             Duration `yaml:"retention" json:"retention"`
	DefaultRequestLimit   int      `yaml:"default-request-limit" json:"defaultRequestLimit"`
	ActivationTimeout     Duration `yaml:"activation-timeout" json:"activationTimeout"`
	MaxBodySize           ByteSize `yaml:"max-body-size" json:"maxBodySize"`
	MaxTotalSize          ByteSize `yaml:"max-total-size" json:"maxTotalSize"`
	MaxAttemptsPerRequest int      `yaml:"max-attempts-per-request" json:"maxAttemptsPerRequest"`
	MinimumFreeDisk       ByteSize `yaml:"minimum-free-disk" json:"minimumFreeDisk"`
	LogMaxItems           int      `yaml:"log-max-items" json:"logMaxItems"`
	LogRetention          Duration `yaml:"log-retention" json:"logRetention"`
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

type GovernanceConfig struct {
	Mode                     string                   `yaml:"mode" json:"mode"`
	UnknownUsagePolicy       string                   `yaml:"unknown-usage-policy" json:"unknownUsagePolicy"`
	MaxConcurrent            int                      `yaml:"max-concurrent" json:"maxConcurrent"`
	RequestsPerMinute        int                      `yaml:"requests-per-minute" json:"requestsPerMinute"`
	TokenLimit               int64                    `yaml:"token-limit" json:"tokenLimit"`
	CostLimitMicros          int64                    `yaml:"cost-limit-micros" json:"costLimitMicros"`
	TokenReservation         int64                    `yaml:"token-reservation" json:"tokenReservation"`
	CostReservationMicros    int64                    `yaml:"cost-reservation-micros" json:"costReservationMicros"`
	ReservationMinTokens     int64                    `yaml:"reservation-min-tokens" json:"reservationMinTokens"`
	ReservationMaxTokens     int64                    `yaml:"reservation-max-tokens" json:"reservationMaxTokens"`
	ReservationMinCostMicros int64                    `yaml:"reservation-min-cost-micros" json:"reservationMinCostMicros"`
	ReservationMaxCostMicros int64                    `yaml:"reservation-max-cost-micros" json:"reservationMaxCostMicros"`
	SoftThresholdPercent     int                      `yaml:"soft-threshold-percent" json:"softThresholdPercent"`
	ForecastWindow           Duration                 `yaml:"forecast-window" json:"forecastWindow"`
	Prices                   []ModelPriceConfig       `yaml:"prices" json:"prices"`
	Budgets                  []GovernanceBudgetConfig `yaml:"budgets" json:"budgets"`
}

// GovernanceBudgetConfig adds an optional scoped budget. Scope is one of
// principal, tenant, model, or upstream; Key is matched against the
// corresponding admission context value. Empty Key is rejected so a typo
// cannot silently create a global budget.
type GovernanceBudgetConfig struct {
	Scope             string `yaml:"scope" json:"scope"`
	Key               string `yaml:"key" json:"key"`
	MaxConcurrent     int    `yaml:"max-concurrent" json:"maxConcurrent"`
	RequestsPerMinute int    `yaml:"requests-per-minute" json:"requestsPerMinute"`
	TokenLimit        int64  `yaml:"token-limit" json:"tokenLimit"`
	CostLimitMicros   int64  `yaml:"cost-limit-micros" json:"costLimitMicros"`
}

type ModelPriceConfig struct {
	Model                string `yaml:"model" json:"model"`
	InputMicrosPerToken  int64  `yaml:"input-micros-per-token" json:"inputMicrosPerToken"`
	OutputMicrosPerToken int64  `yaml:"output-micros-per-token" json:"outputMicrosPerToken"`
}

func Default() Config {
	persistence, incidents, lifecycle, managementSecurity, metricsExport := defaultV2Config()
	return Config{
		SchemaVersion: CurrentSchemaVersion,
		Server: ServerConfig{
			Listen: "127.0.0.1:8318", AdminEnabled: true,
			ReadHeaderTimeout:          duration(10 * time.Second),
			ReadBodyTimeout:            duration(60 * time.Second),
			IdleTimeout:                duration(90 * time.Second),
			DownstreamWriteIdleTimeout: duration(30 * time.Second),
			ShutdownTimeout:            duration(3 * time.Minute),
			MaxHeaderBytes:             1 << 20,
			MaxRequestBody:             ByteSize(32 << 20),
		},
		Upstream: UpstreamConfig{
			BaseURL:                 "http://cli-proxy-api:8317",
			ConnectTimeout:          duration(10 * time.Second),
			ResponseHeaderTimeout:   duration(30 * time.Second),
			ResponseBodyIdleTimeout: duration(90 * time.Second),
		},
		Upstreams: UpstreamPoolConfig{Strategy: "primary-only", Health: UpstreamHealthConfig{Mode: "passive"}, Circuit: UpstreamCircuitConfig{OpenDuration: duration(30 * time.Second), HalfOpenMax: 1}},
		Egress:    EgressConfig{DenyPrivateNetworks: true, AllowedHosts: []string{"cli-proxy-api"}},
		Retry: RetryConfig{
			Enabled: true, Mode: "all-errors",
			MinInterval: duration(60 * time.Second), MaxInterval: duration(120 * time.Second),
			RetryAfterCap:   duration(10 * time.Minute),
			HonorRetryAfter: true,
		},
		Stream: StreamConfig{
			HeartbeatInterval: duration(15 * time.Second), MemoryLimit: ByteSize(64 << 20),
			MaxResponseBody: ByteSize(512 << 20), MaxTotalCache: ByteSize(2 << 30),
		},
		Queue: QueueConfig{
			MaxActive: 8, MaxWaiting: 100, RecoverySpacing: duration(2 * time.Second),
		},
		History: HistoryConfig{MaxItems: 500, Retention: duration(24 * time.Hour)},
		Observability: ObservabilityConfig{
			ErrorDetails: "safe", MaxErrorDetail: ByteSize(2 << 10),
			Telemetry: TelemetryConfig{Protocol: "grpc", SampleRatio: 1, ServiceName: "relay-lifeline", ExportTimeout: duration(10 * time.Second), MetricInterval: duration(time.Minute)},
		},
		Capture: CaptureConfig{
			StorageDir: "/var/lib/relay-lifeline/captures", Retention: duration(72 * time.Hour),
			DefaultRequestLimit: 3, ActivationTimeout: duration(10 * time.Minute),
			MaxBodySize: ByteSize(64 << 20), MaxTotalSize: ByteSize(1 << 30),
			MaxAttemptsPerRequest: 20, MinimumFreeDisk: ByteSize(1 << 30),
			LogMaxItems: 2000, LogRetention: duration(time.Hour),
		},
		Risk: RiskConfig{
			WarningAfter: duration(15 * time.Minute), WarningAttempts: 10,
			AuthErrorAttempts: 3, QueueWarningPercent: 80, MinimumFreeDisk: ByteSize(512 << 20),
		},
		Localization: LocalizationConfig{DefaultLocale: l10n.LocaleChinese, FallbackLocale: l10n.LocaleEnglish},
		Notifications: NotificationConfig{
			StalledAfter: duration(10 * time.Minute), NotifyOnRecovery: true,
			DeliveryAttempts: 3, DeliveryBackoff: duration(5 * time.Second),
			EventTypes: []string{"stalled", "recovered", "long_running", "many_attempts", "auth_errors", "queue_pressure", "disk_pressure", "governance_ledger_failed", "uncertain_delivery", "uncertain_slo_breach", "uncertain_resolved"},
			Locale:     l10n.LocaleChinese,
		},
		Logging:     LoggingConfig{Level: "info", Locale: l10n.LocaleChinese},
		Persistence: persistence, Incidents: incidents, Lifecycle: lifecycle,
		ManagementSecurity: managementSecurity, MetricsExport: metricsExport,
		Governance: GovernanceConfig{Mode: "observe", UnknownUsagePolicy: "observe", ReservationMinTokens: 256, ReservationMaxTokens: 4096, SoftThresholdPercent: 80, ForecastWindow: duration(time.Hour)},
		SLO:        SLOConfig{Enabled: true, AvailabilityTarget: 0.99, RecoveryLatencyTarget: duration(30 * time.Second), Window: duration(24 * time.Hour)},
		TrafficPolicy: TrafficPolicyConfig{
			Mode: "observe", ReleaseStage: "full",
			Shadow:   ShadowTrafficConfig{MaxConcurrent: 1, MaxRequestBody: ByteSize(1 << 20), CostReservationMicros: 1000, RequireIdempotency: true},
			Adaptive: AdaptiveRoutingConfig{ErrorBudgetFloor: 0.5, MinimumObservations: 10, MaximumLatencyMillis: 30_000, LatencyWeight: 0.5, ErrorRateWeight: 0.3, CostWeight: 0.15, CapabilityWeight: 0.05, SwitchCooldown: duration(2 * time.Minute), AutoStopBurnRate: 2, AutoStopFailureRate: 0.25},
		},
	}
}

func duration(value time.Duration) Duration { return Duration{Duration: value} }

func Load(path string) (Config, error) {
	cfg, _, err := LoadWithSourceVersion(path)
	return cfg, err
}

func LoadWithSourceVersion(path string) (Config, int, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, 0, l10n.E("config.read_failed", err, map[string]any{"Error": err.Error()})
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, 0, l10n.E("config.parse_failed", err, map[string]any{"Error": err.Error()})
	}
	sourceVersion := cfg.SchemaVersion
	migrated, err := Migrate(cfg)
	if err != nil {
		return Config{}, sourceVersion, err
	}
	cfg = migrated
	if err := cfg.Validate(); err != nil {
		return Config{}, sourceVersion, err
	}
	return cfg, sourceVersion, nil
}

func (c Config) Validate() error {
	var problems []error
	if c.SLO.AvailabilityTarget < 0 || c.SLO.AvailabilityTarget > 1 || c.SLO.RecoveryLatencyTarget.Duration <= 0 || c.SLO.Window.Duration < time.Hour || c.SLO.Window.Duration > 30*24*time.Hour {
		problems = append(problems, fmt.Errorf("slo targets/window are invalid"))
	}
	problems = append(problems, validateTrafficPolicy(c)...)
	if c.SchemaVersion != CurrentSchemaVersion {
		problems = append(problems, l10n.E("config.schema.unsupported", nil, map[string]any{"Version": c.SchemaVersion, "Current": CurrentSchemaVersion}))
	}
	if strings.TrimSpace(c.Server.Listen) == "" {
		problems = append(problems, l10n.E("config.server.listen_required", nil))
	}
	if c.Server.ReadHeaderTimeout.Duration <= 0 || c.Server.ShutdownTimeout.Duration <= 0 || c.Server.ReadBodyTimeout.Duration < 0 || c.Server.IdleTimeout.Duration < 0 || c.Server.DownstreamWriteIdleTimeout.Duration < 0 || c.Server.MaxHeaderBytes < 4<<10 || c.Server.MaxHeaderBytes > 16<<20 {
		problems = append(problems, l10n.E("config.server.timeout_invalid", nil))
	}
	parsed, err := url.Parse(c.Upstream.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		problems = append(problems, l10n.E("config.upstream.url_invalid", nil))
	} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
		problems = append(problems, l10n.E("config.upstream.scheme_invalid", nil))
	}
	if c.Upstream.ConnectTimeout.Duration <= 0 || c.Upstream.ResponseHeaderTimeout.Duration <= 0 || c.Upstream.ResponseBodyIdleTimeout.Duration <= 0 {
		problems = append(problems, l10n.E("config.upstream.timeout_invalid", nil))
	}
	if c.Upstreams.Strategy != "primary-only" && c.Upstreams.Strategy != "weighted-priority" {
		problems = append(problems, fmt.Errorf("upstreams.strategy must be primary-only or weighted-priority"))
	}
	if err := egress.ValidatePatterns(c.Egress.AllowedHosts); err != nil {
		problems = append(problems, err)
	}
	if c.Egress.DenyPrivateNetworks && len(c.Egress.AllowedHosts) == 0 {
		problems = append(problems, fmt.Errorf("egress.allowed-hosts is required when private networks are denied"))
	}
	if c.Upstreams.Health.Mode != "" && c.Upstreams.Health.Mode != "passive" {
		problems = append(problems, fmt.Errorf("upstreams.health.mode must be passive"))
	}
	if len(c.Upstreams.Targets) > 0 {
		seen := make(map[string]struct{}, len(c.Upstreams.Targets))
		for _, target := range c.Upstreams.Targets {
			parsed, parseErr := url.Parse(target.BaseURL)
			if strings.TrimSpace(target.ID) == "" || target.Weight < 0 || target.MaxActive < 0 || parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				problems = append(problems, fmt.Errorf("invalid upstream target %q", target.ID))
			}
			if _, exists := seen[target.ID]; exists {
				problems = append(problems, fmt.Errorf("duplicate upstream target %q", target.ID))
			}
			seen[target.ID] = struct{}{}
		}
		if c.Upstreams.Strategy == "weighted-priority" {
			for _, target := range c.Upstreams.Targets {
				if target.Weight == 0 {
					problems = append(problems, fmt.Errorf("upstream target %q must have positive weight", target.ID))
				}
			}
		}
	}
	if c.Retry.Mode != "all-errors" && c.Retry.Mode != "transient-errors" {
		problems = append(problems, l10n.E("config.retry.mode_invalid", nil))
	}
	if c.Retry.MinInterval.Duration <= 0 || c.Retry.MaxInterval.Duration < c.Retry.MinInterval.Duration || c.Retry.MaxElapsed.Duration < 0 || c.Retry.RetryAfterCap.Duration < 0 {
		problems = append(problems, l10n.E("config.retry.interval_invalid", nil))
	}
	if c.Governance.Mode != "observe" && c.Governance.Mode != "enforce" {
		problems = append(problems, fmt.Errorf("governance.mode must be observe or enforce"))
	}
	if c.Governance.UnknownUsagePolicy != "observe" && c.Governance.UnknownUsagePolicy != "deny" {
		problems = append(problems, fmt.Errorf("governance.unknown-usage-policy must be observe or deny"))
	}
	if c.Governance.MaxConcurrent < 0 || c.Governance.RequestsPerMinute < 0 || c.Governance.TokenLimit < 0 || c.Governance.CostLimitMicros < 0 || c.Governance.TokenReservation < 0 || c.Governance.CostReservationMicros < 0 || c.Governance.ReservationMinTokens < 0 || c.Governance.ReservationMaxTokens < 0 || c.Governance.ReservationMinCostMicros < 0 || c.Governance.ReservationMaxCostMicros < 0 || c.Governance.SoftThresholdPercent < 0 || c.Governance.SoftThresholdPercent > 100 || c.Governance.ForecastWindow.Duration < 0 {
		problems = append(problems, fmt.Errorf("governance limits must be non-negative"))
	}
	if c.Governance.ReservationMaxTokens > 0 && c.Governance.ReservationMinTokens > c.Governance.ReservationMaxTokens {
		problems = append(problems, fmt.Errorf("governance reservation token bounds are invalid"))
	}
	if c.Governance.ReservationMaxCostMicros > 0 && c.Governance.ReservationMinCostMicros > c.Governance.ReservationMaxCostMicros {
		problems = append(problems, fmt.Errorf("governance reservation cost bounds are invalid"))
	}
	seenModels := make(map[string]struct{}, len(c.Governance.Prices))
	for _, price := range c.Governance.Prices {
		if strings.TrimSpace(price.Model) == "" || price.InputMicrosPerToken < 0 || price.OutputMicrosPerToken < 0 {
			problems = append(problems, fmt.Errorf("invalid governance price for model %q", price.Model))
		}
		if _, exists := seenModels[price.Model]; exists {
			problems = append(problems, fmt.Errorf("duplicate governance price for model %q", price.Model))
		}
		seenModels[price.Model] = struct{}{}
	}
	seenBudgets := make(map[string]struct{}, len(c.Governance.Budgets))
	for _, budget := range c.Governance.Budgets {
		if budget.Scope != "principal" && budget.Scope != "tenant" && budget.Scope != "model" && budget.Scope != "upstream" || strings.TrimSpace(budget.Key) == "" || budget.MaxConcurrent < 0 || budget.RequestsPerMinute < 0 || budget.TokenLimit < 0 || budget.CostLimitMicros < 0 {
			problems = append(problems, fmt.Errorf("invalid governance budget %q/%q", budget.Scope, budget.Key))
		}
		id := budget.Scope + "\x00" + budget.Key
		if _, exists := seenBudgets[id]; exists {
			problems = append(problems, fmt.Errorf("duplicate governance budget %q/%q", budget.Scope, budget.Key))
		}
		seenBudgets[id] = struct{}{}
	}
	if c.Retry.MaxAttempts < 0 {
		problems = append(problems, l10n.E("config.retry.max_attempts", nil))
	}
	if c.Stream.HeartbeatInterval.Duration <= 0 || c.Stream.MemoryLimit < 1<<20 ||
		c.Stream.MaxResponseBody < c.Stream.MemoryLimit || c.Stream.MaxTotalCache < c.Stream.MaxResponseBody ||
		c.Stream.TempDir != "" && !filepath.IsAbs(c.Stream.TempDir) {
		problems = append(problems, l10n.E("config.stream.invalid", nil))
	}
	if c.Server.MaxRequestBody < 1<<20 {
		problems = append(problems, l10n.E("config.server.body_limit", nil))
	}
	if c.Server.ConfigBackupDir != "" && !filepath.IsAbs(c.Server.ConfigBackupDir) {
		problems = append(problems, l10n.E("config.server.backup_dir", nil))
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
	telemetry := c.Observability.Telemetry
	if telemetry.Protocol != "grpc" && telemetry.Protocol != "http/protobuf" && telemetry.Protocol != "stdout" {
		problems = append(problems, fmt.Errorf("observability.telemetry.protocol must be grpc, http/protobuf, or stdout"))
	}
	if telemetry.SampleRatio < 0 || telemetry.SampleRatio > 1 {
		problems = append(problems, fmt.Errorf("observability.telemetry.sample-ratio must be between 0 and 1"))
	}
	if strings.TrimSpace(telemetry.ServiceName) == "" || telemetry.ExportTimeout.Duration <= 0 || telemetry.MetricInterval.Duration <= 0 {
		problems = append(problems, fmt.Errorf("observability.telemetry service-name and durations must be valid"))
	}
	if telemetry.Enabled && telemetry.Protocol != "stdout" && strings.TrimSpace(telemetry.Endpoint) == "" {
		problems = append(problems, fmt.Errorf("observability.telemetry.endpoint is required when telemetry is enabled"))
	}
	if telemetry.Enabled && telemetry.Protocol == "http/protobuf" {
		endpoint, err := url.ParseRequestURI(telemetry.Endpoint)
		if err != nil || endpoint.Host == "" || endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			problems = append(problems, fmt.Errorf("observability.telemetry.endpoint must be an HTTP(S) URL for http/protobuf"))
		}
	}
	if telemetry.Enabled && telemetry.Protocol == "grpc" && strings.Contains(telemetry.Endpoint, "://") {
		problems = append(problems, fmt.Errorf("observability.telemetry.endpoint must be host:port for grpc"))
	}
	if c.Observability.MaxErrorDetail < 256 || c.Observability.MaxErrorDetail > 64<<10 {
		problems = append(problems, l10n.E("config.observability.limit_invalid", nil))
	}
	if strings.TrimSpace(c.Capture.StorageDir) == "" || !filepath.IsAbs(c.Capture.StorageDir) {
		problems = append(problems, l10n.E("config.capture.storage_invalid", nil))
	}
	if c.Capture.Retention.Duration <= 0 || c.Capture.Retention.Duration > 30*24*time.Hour || c.Capture.ActivationTimeout.Duration <= 0 || c.Capture.ActivationTimeout.Duration > time.Hour {
		problems = append(problems, l10n.E("config.capture.duration_invalid", nil))
	}
	if c.Capture.DefaultRequestLimit < 1 || c.Capture.DefaultRequestLimit > 100 || c.Capture.MaxAttemptsPerRequest < 1 || c.Capture.MaxAttemptsPerRequest > 1000 {
		problems = append(problems, l10n.E("config.capture.count_invalid", nil))
	}
	if c.Capture.MaxBodySize < 1<<20 || c.Capture.MaxBodySize > 1<<30 || c.Capture.MaxTotalSize < c.Capture.MaxBodySize || c.Capture.MinimumFreeDisk < 64<<20 {
		problems = append(problems, l10n.E("config.capture.capacity_invalid", nil))
	}
	if c.Capture.LogMaxItems < 100 || c.Capture.LogMaxItems > 100000 || c.Capture.LogRetention.Duration <= 0 || c.Capture.LogRetention.Duration > 7*24*time.Hour {
		problems = append(problems, l10n.E("config.capture.log_invalid", nil))
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
		"auth_errors": true, "queue_pressure": true, "disk_pressure": true, "governance_ledger_failed": true,
		"uncertain_delivery": true, "uncertain_slo_breach": true, "uncertain_resolved": true,
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
	problems = append(problems, validateV2Config(c)...)
	return errors.Join(problems...)
}

func Migrate(cfg Config) (Config, error) {
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = 1
	}
	if cfg.SchemaVersion == 1 {
		defaults := Default()
		cfg.SchemaVersion = 2
		cfg.Persistence = defaults.Persistence
		cfg.Incidents = defaults.Incidents
		cfg.Lifecycle = defaults.Lifecycle
		cfg.ManagementSecurity = defaults.ManagementSecurity
		cfg.MetricsExport = defaults.MetricsExport
	}
	if cfg.SchemaVersion == 2 {
		defaults := Default()
		cfg.SchemaVersion = 3
		cfg.Stream.MaxResponseBody = defaults.Stream.MaxResponseBody
		cfg.Stream.MaxTotalCache = defaults.Stream.MaxTotalCache
	}
	if cfg.SchemaVersion == 3 {
		// Schema 4 introduces runtime-state tracking and keeps the existing
		// single-upstream behavior as the compatibility default.
		cfg.SchemaVersion = 4
	}
	if cfg.SchemaVersion == 4 {
		cfg.SchemaVersion = 5
		cfg.ManagementSecurity.LocalAccessEnabled = true
		cfg.ManagementSecurity.SessionMaxLifetime = duration(8 * time.Hour)
		cfg.ManagementSecurity.OIDC = defaultOIDCConfig()
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		return Config{}, l10n.E("config.schema.unsupported", nil, map[string]any{"Version": cfg.SchemaVersion, "Current": CurrentSchemaVersion})
	}
	return cfg, nil
}

type ChangePlan struct {
	SchemaVersion     int           `json:"schemaVersion"`
	ChangedSections   []string      `json:"changedSections"`
	HotReloadSections []string      `json:"hotReloadSections"`
	RestartSections   []string      `json:"restartSections"`
	RestartRequired   bool          `json:"restartRequired"`
	Fields            []FieldChange `json:"fields,omitempty"`
}

type FieldChange struct {
	Path      string `json:"path"`
	ApplyMode string `json:"applyMode"`
}

func PlanChanges(before, after Config) ChangePlan {
	changed := make([]string, 0)
	hot := make([]string, 0)
	restart := make([]string, 0)
	appendSection := func(name string, differs, needsRestart bool) {
		if !differs {
			return
		}
		changed = append(changed, name)
		if needsRestart {
			restart = append(restart, name)
		} else {
			hot = append(hot, name)
		}
	}
	serverRestart := before.Server.Listen != after.Server.Listen || before.Server.AdminEnabled != after.Server.AdminEnabled || before.Server.ReadHeaderTimeout != after.Server.ReadHeaderTimeout || before.Server.ShutdownTimeout != after.Server.ShutdownTimeout
	appendSection("server", before.Server != after.Server, serverRestart)
	appendSection("upstream", before.Upstream != after.Upstream, false)
	appendSection("upstreams", !upstreamPoolEqual(before.Upstreams, after.Upstreams), false)
	appendSection("egress", !egressEqual(before.Egress, after.Egress), false)
	appendSection("retry", before.Retry != after.Retry, false)
	appendSection("stream", before.Stream != after.Stream, false)
	appendSection("queue", before.Queue != after.Queue, false)
	appendSection("history", before.History != after.History, false)
	telemetryChanged := before.Observability.Telemetry != after.Observability.Telemetry
	appendSection("observability", before.Observability != after.Observability, telemetryChanged)
	appendSection("capture", before.Capture != after.Capture, before.Capture.StorageDir != after.Capture.StorageDir)
	appendSection("risk", before.Risk != after.Risk, false)
	appendSection("localization", before.Localization != after.Localization, false)
	appendSection("notifications", !notificationEqual(before.Notifications, after.Notifications), false)
	appendSection("logging", before.Logging != after.Logging, before.Logging.Level != after.Logging.Level)
	appendSection("persistence", before.Persistence != after.Persistence, true)
	appendSection("incidents", before.Incidents != after.Incidents, false)
	appendSection("lifecycle", before.Lifecycle != after.Lifecycle, false)
	authenticationChanged := before.ManagementSecurity.LocalAccessEnabled != after.ManagementSecurity.LocalAccessEnabled || !oidcEqual(before.ManagementSecurity.OIDC, after.ManagementSecurity.OIDC)
	appendSection("management-security", !managementSecurityEqual(before.ManagementSecurity, after.ManagementSecurity), authenticationChanged)
	appendSection("metrics-export", before.MetricsExport != after.MetricsExport, true)
	appendSection("governance", !governanceEqual(before.Governance, after.Governance), false)
	appendSection("traffic-policy", !trafficPolicyEqual(before.TrafficPolicy, after.TrafficPolicy), false)
	appendSection("slo", before.SLO != after.SLO, false)
	return ChangePlan{SchemaVersion: CurrentSchemaVersion, ChangedSections: changed, HotReloadSections: hot, RestartSections: restart, RestartRequired: len(restart) > 0, Fields: fieldChanges(before, after)}
}

func fieldChanges(before, after Config) []FieldChange {
	changes := make([]FieldChange, 0)
	add := func(path, mode string, differs bool) {
		if differs {
			changes = append(changes, FieldChange{Path: path, ApplyMode: mode})
		}
	}
	add("server.listen", "restart", before.Server.Listen != after.Server.Listen)
	add("server.admin-enabled", "restart", before.Server.AdminEnabled != after.Server.AdminEnabled)
	add("server.read-header-timeout", "restart", before.Server.ReadHeaderTimeout != after.Server.ReadHeaderTimeout)
	add("server.shutdown-timeout", "restart", before.Server.ShutdownTimeout != after.Server.ShutdownTimeout)
	add("server.read-body-timeout", "hot", before.Server.ReadBodyTimeout != after.Server.ReadBodyTimeout)
	add("server.idle-timeout", "hot", before.Server.IdleTimeout != after.Server.IdleTimeout)
	add("server.downstream-write-idle-timeout", "hot", before.Server.DownstreamWriteIdleTimeout != after.Server.DownstreamWriteIdleTimeout)
	add("server.max-header-bytes", "hot", before.Server.MaxHeaderBytes != after.Server.MaxHeaderBytes)
	add("server.max-request-body", "hot", before.Server.MaxRequestBody != after.Server.MaxRequestBody)
	add("upstream", "hot", before.Upstream != after.Upstream)
	add("upstreams", "hot", !upstreamPoolEqual(before.Upstreams, after.Upstreams))
	add("egress", "hot", !egressEqual(before.Egress, after.Egress))
	add("capture.storage-dir", "restart", before.Capture.StorageDir != after.Capture.StorageDir)
	add("logging.level", "restart", before.Logging.Level != after.Logging.Level)
	add("persistence", "restart", before.Persistence != after.Persistence)
	add("metrics-export", "restart", before.MetricsExport != after.MetricsExport)
	add("observability.telemetry", "restart", before.Observability.Telemetry != after.Observability.Telemetry)
	add("management-security.authentication", "restart", before.ManagementSecurity.LocalAccessEnabled != after.ManagementSecurity.LocalAccessEnabled || !oidcEqual(before.ManagementSecurity.OIDC, after.ManagementSecurity.OIDC))
	add("traffic-policy", "hot", !trafficPolicyEqual(before.TrafficPolicy, after.TrafficPolicy))
	add("slo", "hot", before.SLO != after.SLO)
	return changes
}

func notificationEqual(left, right NotificationConfig) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func upstreamPoolEqual(left, right UpstreamPoolConfig) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func egressEqual(left, right EgressConfig) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func trafficPolicyEqual(left, right TrafficPolicyConfig) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func validateTrafficPolicy(c Config) []error {
	cfg := c.TrafficPolicy
	var problems []error
	if cfg.Mode != "observe" && cfg.Mode != "enforce" {
		problems = append(problems, fmt.Errorf("traffic-policy.mode must be observe or enforce"))
	}
	if cfg.ReleaseStage != "draft" && cfg.ReleaseStage != "shadow" && cfg.ReleaseStage != "canary" && cfg.ReleaseStage != "full" {
		problems = append(problems, fmt.Errorf("traffic-policy.release-stage must be draft, shadow, canary, or full"))
	}
	if cfg.CanaryPercent < 0 || cfg.CanaryPercent > 100 || cfg.ReleaseStage == "canary" && cfg.CanaryPercent == 0 {
		problems = append(problems, fmt.Errorf("traffic-policy.canary-percent is invalid"))
	}
	targets := make(map[string]bool)
	if len(c.Upstreams.Targets) == 0 {
		targets["primary"] = true
	}
	for _, target := range c.Upstreams.Targets {
		if target.CostMicrosPer1K < 0 || target.CapabilityScore < 0 || target.CapabilityScore > 1 {
			problems = append(problems, fmt.Errorf("invalid upstream target economics or capability %q", target.ID))
		}
		targets[target.ID] = true
	}
	seen := make(map[string]struct{}, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if strings.TrimSpace(rule.ID) == "" || len(rule.ID) > 64 || rule.Priority < 0 || rule.Method != "" && rule.Method != strings.ToUpper(rule.Method) || rule.PathPrefix != "" && !strings.HasPrefix(rule.PathPrefix, "/") || rule.Action != "route" && rule.Action != "deny" {
			problems = append(problems, fmt.Errorf("invalid traffic policy rule %q", rule.ID))
		}
		if _, exists := seen[rule.ID]; exists {
			problems = append(problems, fmt.Errorf("duplicate traffic policy rule %q", rule.ID))
		}
		seen[rule.ID] = struct{}{}
		if rule.Action == "route" && !targets[rule.TargetID] {
			problems = append(problems, fmt.Errorf("traffic policy rule %q references unknown target %q", rule.ID, rule.TargetID))
		}
	}
	shadow := cfg.Shadow
	if shadow.SamplePercent < 0 || shadow.SamplePercent > 100 || shadow.MaxConcurrent < 1 || shadow.MaxRequestBody < 1<<10 || shadow.RequestBudgetPerHour < 0 || shadow.CostBudgetMicrosPerHour < 0 || shadow.CostReservationMicros < 0 {
		problems = append(problems, fmt.Errorf("traffic-policy.shadow limits are invalid"))
	}
	if shadow.Enabled && (!targets[shadow.TargetID] || shadow.SamplePercent == 0 || shadow.RequestBudgetPerHour == 0 || shadow.CostBudgetMicrosPerHour == 0 || shadow.CostReservationMicros == 0) {
		problems = append(problems, fmt.Errorf("traffic-policy.shadow requires a target, sampling, request budget, and cost budget"))
	}
	adaptive := cfg.Adaptive
	weights := adaptive.LatencyWeight + adaptive.ErrorRateWeight + adaptive.CostWeight + adaptive.CapabilityWeight
	if adaptive.ErrorBudgetFloor < 0 || adaptive.ErrorBudgetFloor > 1 || adaptive.MinimumObservations < 1 || adaptive.MaximumLatencyMillis < 1 || adaptive.SwitchCooldown.Duration < 0 || adaptive.AutoStopBurnRate < 0 || adaptive.AutoStopFailureRate < 0 || adaptive.AutoStopFailureRate > 1 || weights <= 0 {
		problems = append(problems, fmt.Errorf("traffic-policy.adaptive limits are invalid"))
	}
	if adaptive.FallbackTargetID != "" && !targets[adaptive.FallbackTargetID] {
		problems = append(problems, fmt.Errorf("traffic-policy.adaptive references unknown fallback target %q", adaptive.FallbackTargetID))
	}
	return problems
}

// ValidateTrafficPolicyConfig validates a policy against the supplied target
// IDs without requiring callers to construct a complete runtime Config.
func ValidateTrafficPolicyConfig(policy TrafficPolicyConfig, targetIDs []string) error {
	base := Default()
	base.TrafficPolicy = policy
	base.Upstreams.Targets = make([]UpstreamTargetConfig, 0, len(targetIDs))
	for _, id := range targetIDs {
		base.Upstreams.Targets = append(base.Upstreams.Targets, UpstreamTargetConfig{ID: id, BaseURL: "https://policy.invalid", Weight: 1, IdempotencyDomain: "policy"})
	}
	return errors.Join(validateTrafficPolicy(base)...)
}

func governanceEqual(left, right GovernanceConfig) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func oidcEqual(left, right OIDCConfig) bool {
	return left.Enabled == right.Enabled && left.IssuerURL == right.IssuerURL && left.ClientID == right.ClientID && left.RedirectURL == right.RedirectURL &&
		left.RoleClaim == right.RoleClaim && slices.Equal(left.Scopes, right.Scopes) && slices.Equal(left.SigningAlgorithms, right.SigningAlgorithms) &&
		slices.Equal(left.ViewerValues, right.ViewerValues) && slices.Equal(left.OperatorValues, right.OperatorValues) && slices.Equal(left.SensitiveValues, right.SensitiveValues)
}

func managementSecurityEqual(left, right ManagementSecurityConfig) bool {
	return left.LocalAccessEnabled == right.LocalAccessEnabled && left.LoginFailuresPerMinute == right.LoginFailuresPerMinute &&
		left.LoginCooldown == right.LoginCooldown && left.SessionIdleTimeout == right.SessionIdleTimeout && left.SessionMaxLifetime == right.SessionMaxLifetime &&
		oidcEqual(left.OIDC, right.OIDC)
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
	mu              sync.RWMutex
	updateMu        sync.Mutex
	path            string
	cfg             Config // active configuration; kept for compatibility with Get callers
	desired         Config
	activeRevision  string
	desiredRevision string
	pendingRestart  ChangePlan
	lastBackup      string
	listeners       map[uint64]func(Config)
	nextListenerID  uint64
}

type RuntimeState struct {
	Active          Config     `json:"active"`
	Desired         Config     `json:"desired"`
	ActiveRevision  string     `json:"activeRevision"`
	DesiredRevision string     `json:"desiredRevision"`
	PendingRestart  ChangePlan `json:"pendingRestart"`
}

func NewStore(path string, cfg Config) *Store {
	active := cloneConfig(cfg)
	activeRevision := revision(active)
	return &Store{path: path, cfg: active, desired: cloneConfig(active), activeRevision: activeRevision, desiredRevision: activeRevision, listeners: make(map[uint64]func(Config))}
}

func (s *Store) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.cfg)
}

func (s *Store) Active() Config { return s.Get() }

func (s *Store) Desired() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.desired)
}

func (s *Store) State() RuntimeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return RuntimeState{
		Active: cloneConfig(s.cfg), Desired: cloneConfig(s.desired),
		ActiveRevision: s.activeRevision, DesiredRevision: s.desiredRevision,
		PendingRestart: cloneChangePlan(s.pendingRestart),
	}
}

func (s *Store) Update(cfg Config, persist bool) error {
	_, err := s.UpdateWithResult(cfg, persist)
	if err != nil {
		return err
	}
	// Update is the legacy programmatic API. Preserve its historical
	// behavior by applying restart-scoped fields immediately; the admin API
	// uses UpdateWithResult and exposes PendingRestart instead.
	s.mu.Lock()
	s.cfg = cloneConfig(s.desired)
	s.activeRevision = s.desiredRevision
	s.pendingRestart = ChangePlan{SchemaVersion: CurrentSchemaVersion}
	s.mu.Unlock()
	s.notify(s.Get())
	return err
}

type UpdateResult struct {
	BackupPath      string     `json:"backupPath,omitempty"`
	ActiveRevision  string     `json:"activeRevision"`
	DesiredRevision string     `json:"desiredRevision"`
	PendingRestart  ChangePlan `json:"pendingRestart"`
}

func (s *Store) UpdateWithResult(cfg Config, persist bool) (UpdateResult, error) {
	return s.updateWithResult(cfg, persist, "")
}

// UpdateWithResultIfRevision applies a configuration only when the desired
// revision still matches expectedRevision. It closes the read/modify/write
// race between policy release handlers and ordinary configuration saves.
func (s *Store) UpdateWithResultIfRevision(cfg Config, persist bool, expectedRevision string) (UpdateResult, error) {
	return s.updateWithResult(cfg, persist, expectedRevision)
}

func (s *Store) updateWithResult(cfg Config, persist bool, expectedRevision string) (UpdateResult, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if expectedRevision != "" {
		s.mu.RLock()
		currentRevision := s.desiredRevision
		s.mu.RUnlock()
		if currentRevision != expectedRevision {
			return UpdateResult{}, ErrRevisionConflict
		}
	}
	migrated, err := Migrate(cfg)
	if err != nil {
		return UpdateResult{}, err
	}
	cfg = migrated
	if err := cfg.Validate(); err != nil {
		return UpdateResult{}, err
	}
	s.mu.RLock()
	active := cloneConfig(s.cfg)
	s.mu.RUnlock()
	plan := PlanChanges(active, cfg)
	result := UpdateResult{DesiredRevision: revision(cfg), PendingRestart: plan}
	if persist {
		backupPath, err := backupCurrentConfig(s.path, cfg.Server.ConfigBackupDir)
		if err != nil {
			return UpdateResult{}, err
		}
		result.BackupPath = backupPath
		if err := cfg.Save(s.path); err != nil {
			return UpdateResult{}, err
		}
	}
	active = applyHotReload(active, cfg, plan)
	s.mu.Lock()
	s.cfg = cloneConfig(active)
	s.desired = cloneConfig(cfg)
	s.activeRevision = revision(active)
	s.desiredRevision = revision(cfg)
	s.pendingRestart = cloneChangePlan(plan)
	s.lastBackup = result.BackupPath
	result.ActiveRevision = s.activeRevision
	result.DesiredRevision = s.desiredRevision
	result.PendingRestart = cloneChangePlan(s.pendingRestart)
	s.mu.Unlock()
	s.notify(active)
	return result, nil
}

func (s *Store) Subscribe(listener func(Config)) func() {
	if listener == nil {
		return func() {}
	}
	s.mu.Lock()
	s.nextListenerID++
	id := s.nextListenerID
	s.listeners[id] = listener
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

func (s *Store) notify(cfg Config) {
	s.mu.RLock()
	listeners := make([]func(Config), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.RUnlock()
	for _, listener := range listeners {
		listener(cloneConfig(cfg))
	}
}

func (s *Store) LastBackup() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastBackup
}

func (s *Store) Reload() error {
	cfg, err := Load(s.path)
	if err != nil {
		return err
	}
	_, err = s.UpdateWithResult(cfg, false)
	return err
}

func applyHotReload(active, desired Config, plan ChangePlan) Config {
	result := cloneConfig(desired)
	for _, field := range plan.Fields {
		if field.ApplyMode != "restart" {
			continue
		}
		switch field.Path {
		case "server.listen":
			result.Server.Listen = active.Server.Listen
		case "server.admin-enabled":
			result.Server.AdminEnabled = active.Server.AdminEnabled
		case "server.read-header-timeout":
			result.Server.ReadHeaderTimeout = active.Server.ReadHeaderTimeout
		case "server.shutdown-timeout":
			result.Server.ShutdownTimeout = active.Server.ShutdownTimeout
		case "capture.storage-dir":
			result.Capture.StorageDir = active.Capture.StorageDir
		case "logging.level":
			result.Logging.Level = active.Logging.Level
		case "persistence":
			result.Persistence = active.Persistence
		case "metrics-export":
			result.MetricsExport = active.MetricsExport
		case "observability.telemetry":
			result.Observability.Telemetry = active.Observability.Telemetry
		case "management-security.authentication":
			result.ManagementSecurity.LocalAccessEnabled = active.ManagementSecurity.LocalAccessEnabled
			result.ManagementSecurity.OIDC = active.ManagementSecurity.OIDC
		}
	}
	return result
}

func revision(cfg Config) string {
	data, _ := json.Marshal(cfg)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])[:16]
}

func cloneConfig(cfg Config) Config {
	data, err := json.Marshal(cfg)
	if err != nil {
		return cfg
	}
	var result Config
	if err := json.Unmarshal(data, &result); err != nil {
		return cfg
	}
	return result
}

func cloneChangePlan(plan ChangePlan) ChangePlan {
	plan.ChangedSections = append([]string(nil), plan.ChangedSections...)
	plan.HotReloadSections = append([]string(nil), plan.HotReloadSections...)
	plan.RestartSections = append([]string(nil), plan.RestartSections...)
	plan.Fields = append([]FieldChange(nil), plan.Fields...)
	return plan
}

func backupCurrentConfig(path, configuredDirectory string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", l10n.E("config.read_failed", err, map[string]any{"Error": err.Error()})
	}
	directory := configuredDirectory
	if directory == "" {
		directory = filepath.Join(filepath.Dir(path), ".relay-lifeline-backups")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", l10n.E("config.backup.failed", err, map[string]any{"Error": err.Error()})
	}
	backupPath := filepath.Join(directory, "config-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".yaml")
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return "", l10n.E("config.backup.failed", err, map[string]any{"Error": err.Error()})
	}
	if err := os.Chmod(backupPath, 0o600); err != nil {
		return "", l10n.E("config.backup.failed", err, map[string]any{"Error": err.Error()})
	}
	if err := pruneConfigBackups(directory, 10); err != nil {
		return "", err
	}
	return backupPath, nil
}

func pruneConfigBackups(directory string, keep int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return l10n.E("config.backup.failed", err, map[string]any{"Error": err.Error()})
	}
	var backups []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "config-") && strings.HasSuffix(entry.Name(), ".yaml") {
			backups = append(backups, entry.Name())
		}
	}
	for len(backups) > keep {
		if err := os.Remove(filepath.Join(directory, backups[0])); err != nil {
			return l10n.E("config.backup.failed", err, map[string]any{"Error": err.Error()})
		}
		backups = backups[1:]
	}
	return nil
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
