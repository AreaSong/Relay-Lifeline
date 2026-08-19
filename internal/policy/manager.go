package policy

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

const auditLimit = 500

type Input struct {
	Method               string         `json:"method"`
	Path                 string         `json:"path"`
	Model                string         `json:"model"`
	Principal            string         `json:"principal"`
	RequestID            string         `json:"requestId,omitempty"`
	IdempotencyKey       string         `json:"idempotencyKey,omitempty"`
	BodyBytes            int64          `json:"bodyBytes,omitempty"`
	SLOHealthy           bool           `json:"sloHealthy"`
	ErrorBudgetRemaining float64        `json:"errorBudgetRemaining"`
	ErrorBudgetBurnRate  float64        `json:"errorBudgetBurnRate"`
	FailureRate          float64        `json:"failureRate"`
	Targets              []TargetSignal `json:"targets,omitempty"`
}

type TargetSignal struct {
	ID              string  `json:"id"`
	CircuitState    string  `json:"circuitState"`
	Observations    int     `json:"observations"`
	LatencyMs       int64   `json:"latencyMilliseconds"`
	ErrorRate       float64 `json:"errorRate"`
	RateLimitRate   float64 `json:"rateLimitRate"`
	CostMicrosPer1K int64   `json:"costMicrosPer1K"`
	CapabilityScore float64 `json:"capabilityScore"`
}

type Decision struct {
	ID                  uint64    `json:"id"`
	EvaluatedAt         time.Time `json:"evaluatedAt"`
	DryRun              bool      `json:"dryRun"`
	Enabled             bool      `json:"enabled"`
	Mode                string    `json:"mode"`
	MatchedRuleID       string    `json:"matchedRuleId,omitempty"`
	Action              string    `json:"action"`
	TargetID            string    `json:"targetId,omitempty"`
	RecommendedTargetID string    `json:"recommendedTargetId,omitempty"`
	Denied              bool      `json:"denied"`
	Enforced            bool      `json:"enforced"`
	Reason              string    `json:"reason"`
	Adaptive            bool      `json:"adaptive"`
	ShadowTargetID      string    `json:"shadowTargetId,omitempty"`
	ShadowEligible      bool      `json:"shadowEligible"`
	RequestID           string    `json:"requestId,omitempty"`
	CanarySelected      bool      `json:"canarySelected"`
	Fallback            bool      `json:"fallback"`
	AdaptiveScore       float64   `json:"adaptiveScore,omitempty"`
	Explanation         []string  `json:"explanation,omitempty"`
}

type Status struct {
	Enabled                  bool       `json:"enabled"`
	Mode                     string     `json:"mode"`
	Rules                    int        `json:"rules"`
	Decisions                uint64     `json:"decisions"`
	Denied                   uint64     `json:"denied"`
	Routed                   uint64     `json:"routed"`
	Adaptive                 uint64     `json:"adaptive"`
	ShadowPlanned            uint64     `json:"shadowPlanned"`
	ShadowActive             int        `json:"shadowActive"`
	ShadowSent               uint64     `json:"shadowSent"`
	ShadowSkipped            uint64     `json:"shadowSkipped"`
	ShadowFailed             uint64     `json:"shadowFailed"`
	ShadowReservedCostMicros int64      `json:"shadowReservedCostMicros"`
	ShadowActualCostMicros   int64      `json:"shadowActualCostMicros"`
	AdaptiveStopped          bool       `json:"adaptiveStopped"`
	AdaptiveStopReason       string     `json:"adaptiveStopReason,omitempty"`
	AdaptiveSwitches         uint64     `json:"adaptiveSwitches"`
	AdaptiveLastTargetID     string     `json:"adaptiveLastTargetId,omitempty"`
	AdaptiveLastScore        float64    `json:"adaptiveLastScore,omitempty"`
	Recent                   []Decision `json:"recent"`
}

type Manager struct {
	mu                     sync.RWMutex
	config                 config.TrafficPolicyConfig
	nextID                 uint64
	decisions              uint64
	denied                 uint64
	routed                 uint64
	adaptive               uint64
	shadow                 uint64
	shadowActive           int
	shadowSent             uint64
	shadowSkipped          uint64
	shadowFailed           uint64
	shadowWindow           time.Time
	shadowWindowRequests   int
	shadowWindowCostMicros int64
	shadowActualCostMicros int64
	recent                 []Decision
	now                    func() time.Time
	adaptiveStopped        bool
	adaptiveStopReason     string
	adaptiveSwitches       uint64
	adaptiveLastTargetID   string
	adaptiveLastScore      float64
	adaptiveLastAt         time.Time
}

type ShadowLease struct {
	manager *Manager
	once    sync.Once
}

func New(cfg config.TrafficPolicyConfig) *Manager {
	return &Manager{config: cloneConfig(cfg), now: time.Now}
}

func (m *Manager) Apply(cfg config.TrafficPolicyConfig) {
	m.mu.Lock()
	changed := policyRevision(m.config) != policyRevision(cfg)
	m.config = cloneConfig(cfg)
	// Only a real policy revision change acknowledges an adaptive circuit stop.
	if changed {
		m.adaptiveStopped, m.adaptiveStopReason = false, ""
	}
	m.mu.Unlock()
}

func (m *Manager) Evaluate(input Input, dryRun bool) Decision {
	m.mu.Lock()
	if dryRun {
		// Adaptive evaluation updates cooldown and stop state. Simulations run on
		// a detached copy so they cannot alter production routing.
		copy := &Manager{
			config:               cloneConfig(m.config),
			nextID:               m.nextID,
			now:                  m.now,
			adaptiveStopped:      m.adaptiveStopped,
			adaptiveStopReason:   m.adaptiveStopReason,
			adaptiveSwitches:     m.adaptiveSwitches,
			adaptiveLastTargetID: m.adaptiveLastTargetID,
			adaptiveLastScore:    m.adaptiveLastScore,
			adaptiveLastAt:       m.adaptiveLastAt,
		}
		decision := copy.evaluateLocked(input, true)
		m.mu.Unlock()
		return decision
	}
	decision := m.evaluateLocked(input, false)
	m.mu.Unlock()
	return decision
}

func (m *Manager) evaluateLocked(input Input, dryRun bool) Decision {
	cfg := m.config
	m.nextID++
	decision := Decision{ID: m.nextID, EvaluatedAt: m.now().UTC(), DryRun: dryRun, Enabled: cfg.Enabled, Mode: cfg.Mode, Action: "default", Reason: "no_match", RequestID: input.RequestID, Explanation: []string{"policy evaluated"}}
	if !cfg.Enabled {
		decision.Reason = "disabled"
		decision.Explanation = append(decision.Explanation, "traffic policy is disabled")
		return m.recordLocked(decision)
	}
	if cfg.ReleaseStage == "draft" || cfg.ReleaseStage == "shadow" {
		decision.Reason = "release_not_enforcing"
		decision.Explanation = append(decision.Explanation, "release stage does not enforce client traffic")
	}
	rules := append([]config.TrafficPolicyRule(nil), cfg.Rules...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority < rules[j].Priority })
	for _, rule := range rules {
		if !matches(rule, input) {
			continue
		}
		decision.MatchedRuleID, decision.Action, decision.Reason = rule.ID, rule.Action, "rule_match"
		decision.Explanation = append(decision.Explanation, "matched rule "+rule.ID)
		decision.RecommendedTargetID = rule.TargetID
		decision.Denied = rule.Action == "deny"
		decision.Enforced = cfg.Mode == "enforce" && !dryRun && releaseSelectedLocked(cfg, input)
		decision.CanarySelected = releaseSelectedLocked(cfg, input)
		break
	}
	if decision.RecommendedTargetID == "" && !decision.Denied {
		if target, score, reason := m.adaptiveTargetLocked(cfg.Adaptive, input); target != "" {
			decision.Action, decision.RecommendedTargetID, decision.Adaptive, decision.Reason = "route", target, true, reason
			decision.AdaptiveScore = score
			decision.Enforced = cfg.Mode == "enforce" && !dryRun && releaseSelectedLocked(cfg, input)
			decision.CanarySelected = releaseSelectedLocked(cfg, input)
			decision.Explanation = append(decision.Explanation, "adaptive score selected "+target)
		} else if cfg.Adaptive.Enabled {
			if fallback := fallbackTarget(cfg.Adaptive, input); fallback != "" {
				decision.Action, decision.RecommendedTargetID, decision.Fallback, decision.Reason = "route", fallback, true, "adaptive_fallback"
				decision.Enforced = cfg.Mode == "enforce" && !dryRun && releaseSelectedLocked(cfg, input)
				decision.CanarySelected = releaseSelectedLocked(cfg, input)
				decision.Explanation = append(decision.Explanation, "adaptive guard stopped; fallback target selected")
			}
		}
	}
	if cfg.ReleaseStage == "canary" && !decision.CanarySelected {
		decision.Enforced = false
		decision.Reason = "canary_not_selected"
		decision.Explanation = append(decision.Explanation, "request was outside the canary sample")
	}
	decision.ShadowEligible, decision.ShadowTargetID = shadowPlan(cfg.Shadow, input)
	if decision.Enforced {
		decision.TargetID = decision.RecommendedTargetID
	}
	return m.recordLocked(decision)
}

func (m *Manager) recordLocked(decision Decision) Decision {
	m.decisions++
	if decision.Denied {
		m.denied++
	}
	if decision.TargetID != "" {
		m.routed++
	}
	if decision.Adaptive {
		m.adaptive++
	}
	if decision.ShadowEligible {
		m.shadow++
	}
	m.recent = append(m.recent, decision)
	if len(m.recent) > auditLimit {
		copy(m.recent, m.recent[len(m.recent)-auditLimit:])
		m.recent = m.recent[:auditLimit]
	}
	return decision
}

func (m *Manager) Status(limit int) Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if limit > len(m.recent) {
		limit = len(m.recent)
	}
	// 即使没有决策记录，也保持集合字段为 JSON 数组，避免客户端把 null
	// 误判为契约错误。
	recent := make([]Decision, 0, limit)
	recent = append(recent, m.recent[len(m.recent)-limit:]...)
	for left, right := 0, len(recent)-1; left < right; left, right = left+1, right-1 {
		recent[left], recent[right] = recent[right], recent[left]
	}
	return Status{Enabled: m.config.Enabled, Mode: m.config.Mode, Rules: len(m.config.Rules), Decisions: m.decisions, Denied: m.denied, Routed: m.routed, Adaptive: m.adaptive, ShadowPlanned: m.shadow, ShadowActive: m.shadowActive, ShadowSent: m.shadowSent, ShadowSkipped: m.shadowSkipped, ShadowFailed: m.shadowFailed, ShadowReservedCostMicros: m.shadowWindowCostMicros, ShadowActualCostMicros: m.shadowActualCostMicros, AdaptiveStopped: m.adaptiveStopped, AdaptiveStopReason: m.adaptiveStopReason, AdaptiveSwitches: m.adaptiveSwitches, AdaptiveLastTargetID: m.adaptiveLastTargetID, AdaptiveLastScore: m.adaptiveLastScore, Recent: recent}
}

// StopAdaptive is an explicit operational circuit breaker. It is intentionally
// idempotent so an alert handler can call it repeatedly without changing the
// audit semantics.
func (m *Manager) StopAdaptive(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.adaptiveStopped = true
	m.adaptiveStopReason = strings.TrimSpace(reason)
	if m.adaptiveStopReason == "" {
		m.adaptiveStopReason = "operator_stop"
	}
}

func (m *Manager) ResumeAdaptive() {
	m.mu.Lock()
	m.adaptiveStopped, m.adaptiveStopReason = false, ""
	m.mu.Unlock()
}

func (m *Manager) AcquireShadow(decision Decision) (*ShadowLease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg := m.config.Shadow
	if !decision.ShadowEligible || !cfg.Enabled {
		m.shadowSkipped++
		return nil, false
	}
	now := m.now().UTC().Truncate(time.Hour)
	if !m.shadowWindow.Equal(now) {
		m.shadowWindow, m.shadowWindowRequests, m.shadowWindowCostMicros = now, 0, 0
	}
	if m.shadowActive >= cfg.MaxConcurrent || m.shadowWindowRequests >= cfg.RequestBudgetPerHour || m.shadowWindowCostMicros+cfg.CostReservationMicros > cfg.CostBudgetMicrosPerHour {
		m.shadowSkipped++
		return nil, false
	}
	m.shadowActive++
	m.shadowSent++
	m.shadowWindowRequests++
	m.shadowWindowCostMicros += cfg.CostReservationMicros
	return &ShadowLease{manager: m}, true
}

func (m *Manager) SkipShadow() {
	m.mu.Lock()
	m.shadowSkipped++
	m.mu.Unlock()
}

func (m *Manager) RecordShadowCost(costMicros int64) {
	if costMicros <= 0 {
		return
	}
	m.mu.Lock()
	m.shadowActualCostMicros += costMicros
	m.mu.Unlock()
}

func (l *ShadowLease) Complete(success bool) {
	if l == nil || l.manager == nil {
		return
	}
	l.once.Do(func() {
		l.manager.mu.Lock()
		if l.manager.shadowActive > 0 {
			l.manager.shadowActive--
		}
		if !success {
			l.manager.shadowFailed++
		}
		l.manager.mu.Unlock()
	})
}

func matches(rule config.TrafficPolicyRule, input Input) bool {
	if !rule.Enabled {
		return false
	}
	if rule.Method != "" && !strings.EqualFold(rule.Method, input.Method) {
		return false
	}
	if rule.PathPrefix != "" && !strings.HasPrefix(input.Path, rule.PathPrefix) {
		return false
	}
	if rule.Model != "" && rule.Model != input.Model {
		return false
	}
	return rule.PrincipalPrefix == "" || strings.HasPrefix(input.Principal, rule.PrincipalPrefix)
}

func (m *Manager) adaptiveTargetLocked(cfg config.AdaptiveRoutingConfig, input Input) (string, float64, string) {
	if !cfg.Enabled || m.adaptiveStopped {
		return "", 0, "adaptive_stopped"
	}
	if !input.SLOHealthy || input.ErrorBudgetRemaining < cfg.ErrorBudgetFloor {
		m.adaptiveStopped, m.adaptiveStopReason = true, "slo_guard"
		return "", 0, "adaptive_slo_guarded"
	}
	if cfg.AutoStopBurnRate > 0 && input.ErrorBudgetBurnRate > cfg.AutoStopBurnRate {
		m.adaptiveStopped, m.adaptiveStopReason = true, "burn_rate_guard"
		return "", 0, "adaptive_auto_stopped"
	}
	if cfg.AutoStopFailureRate > 0 && input.FailureRate >= cfg.AutoStopFailureRate {
		m.adaptiveStopped, m.adaptiveStopReason = true, "failure_rate_guard"
		return "", 0, "adaptive_auto_stopped"
	}
	weights := []float64{cfg.LatencyWeight, cfg.ErrorRateWeight, cfg.CostWeight, cfg.CapabilityWeight}
	if sum := weights[0] + weights[1] + weights[2] + weights[3]; sum <= 0 {
		weights = []float64{0.5, 0.3, 0.15, 0.05}
	} else {
		for i := range weights {
			weights[i] /= sum
		}
	}
	bestID, bestScore := "", 0.0
	for _, target := range input.Targets {
		if target.CircuitState != "closed" || target.Observations < cfg.MinimumObservations || target.LatencyMs <= 0 || target.LatencyMs > cfg.MaximumLatencyMillis {
			continue
		}
		latency := float64(target.LatencyMs) / float64(cfg.MaximumLatencyMillis)
		errorRate := clamp(target.ErrorRate+target.RateLimitRate, 0, 1)
		cost := clamp(float64(target.CostMicrosPer1K)/100000.0, 0, 1)
		capability := clamp(target.CapabilityScore, 0, 1)
		score := weights[0]*latency + weights[1]*errorRate + weights[2]*cost + weights[3]*(1-capability)
		if bestID == "" || score < bestScore || score == bestScore && target.ID < bestID {
			bestID, bestScore = target.ID, score
		}
	}
	if bestID == "" {
		return "", 0, "adaptive_no_eligible_target"
	}
	now := m.now().UTC()
	if m.adaptiveLastTargetID != "" && m.adaptiveLastTargetID != bestID && cfg.SwitchCooldown.Duration > 0 && now.Sub(m.adaptiveLastAt) < cfg.SwitchCooldown.Duration {
		for _, target := range input.Targets {
			if target.ID == m.adaptiveLastTargetID && target.CircuitState == "closed" && target.Observations >= cfg.MinimumObservations && target.LatencyMs > 0 && target.LatencyMs <= cfg.MaximumLatencyMillis {
				bestID = target.ID
				break
			}
		}
	}
	if bestID != m.adaptiveLastTargetID {
		if m.adaptiveLastTargetID != "" {
			m.adaptiveSwitches++
		}
		m.adaptiveLastTargetID, m.adaptiveLastAt = bestID, now
	}
	m.adaptiveLastScore = bestScore
	return bestID, bestScore, "adaptive_scored"
}

func fallbackTarget(cfg config.AdaptiveRoutingConfig, input Input) string {
	if cfg.FallbackTargetID == "" {
		return ""
	}
	for _, target := range input.Targets {
		if target.ID == cfg.FallbackTargetID && target.CircuitState == "closed" {
			return target.ID
		}
	}
	return ""
}

func releaseSelectedLocked(cfg config.TrafficPolicyConfig, input Input) bool {
	switch cfg.ReleaseStage {
	case "draft", "shadow":
		return false
	case "canary":
		if cfg.CanaryPercent <= 0 {
			return false
		}
		seed := input.RequestID
		if seed == "" {
			seed = input.Method + "\x00" + input.Path + "\x00" + input.Model + "\x00" + input.Principal
		}
		digest := sha256.Sum256([]byte(seed))
		return int(binary.BigEndian.Uint32(digest[:4])%100) < cfg.CanaryPercent
	default:
		return true
	}
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func shadowPlan(cfg config.ShadowTrafficConfig, input Input) (bool, string) {
	if !cfg.Enabled || cfg.SamplePercent <= 0 || cfg.RequestBudgetPerHour <= 0 || input.BodyBytes > int64(cfg.MaxRequestBody) || cfg.RequireIdempotency && input.IdempotencyKey == "" || !input.SLOHealthy {
		return false, ""
	}
	seed := input.RequestID
	if seed == "" {
		seed = input.Method + "\x00" + input.Path + "\x00" + input.Model + "\x00" + input.Principal
	}
	digest := sha256.Sum256([]byte(seed))
	if int(binary.BigEndian.Uint32(digest[:4])%100) >= cfg.SamplePercent {
		return false, ""
	}
	return true, cfg.TargetID
}

func cloneConfig(cfg config.TrafficPolicyConfig) config.TrafficPolicyConfig {
	cfg.Rules = append([]config.TrafficPolicyRule(nil), cfg.Rules...)
	return cfg
}
