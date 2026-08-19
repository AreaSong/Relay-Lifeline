package admin

import (
	"net/http"
	"time"

	"github.com/areasong/relay-lifeline/internal/state"
)

type healthComponent struct {
	Name    string         `json:"name"`
	State   string         `json:"state"`
	Healthy bool           `json:"healthy"`
	Details map[string]any `json:"details,omitempty"`
}

type healthSummary struct {
	GeneratedAt time.Time         `json:"generatedAt"`
	Overall     string            `json:"overall"`
	Components  []healthComponent `json:"components"`
	Actions     []string          `json:"actions,omitempty"`
}

func (h *Handler) health(writer http.ResponseWriter) {
	now := time.Now().UTC()
	result := healthSummary{GeneratedAt: now, Overall: "healthy", Components: make([]healthComponent, 0, 8)}
	add := func(component healthComponent) {
		result.Components = append(result.Components, component)
		if !component.Healthy {
			result.Overall = "degraded"
		}
	}

	paused := false
	if h.controller != nil {
		paused = h.controller.IsPaused()
	}
	snapshot := h.registry.Snapshot(paused)
	gatewayMode := h.controller.Mode()
	add(healthComponent{Name: "gateway", State: gatewayMode, Healthy: gatewayMode == state.ControlRunning || gatewayMode == state.ControlPaused, Details: map[string]any{
		"active": snapshot.Active, "queued": snapshot.Queued, "waiting": snapshot.Waiting, "requesting": snapshot.Requesting,
	}})
	uncertainTarget := h.store.Get().Lifecycle.EffectiveUncertainResolutionTarget().Seconds()
	uncertainHealthy := snapshot.Uncertain == 0 || snapshot.OldestUncertainSeconds <= uncertainTarget
	uncertainState := "healthy"
	if snapshot.Uncertain > 0 {
		uncertainState = "open"
	}
	if !uncertainHealthy {
		uncertainState = "degraded"
	}
	add(healthComponent{Name: "uncertain-delivery", State: uncertainState, Healthy: uncertainHealthy, Details: map[string]any{
		"open": snapshot.Uncertain, "oldestSeconds": snapshot.OldestUncertainSeconds, "targetSeconds": uncertainTarget,
	}})
	if !uncertainHealthy {
		result.Actions = append(result.Actions, "resolve-uncertain-delivery")
	}

	if snapshot.PersistenceDegraded {
		add(healthComponent{Name: "request-persistence", State: "degraded", Healthy: false, Details: map[string]any{"pending": snapshot.PersistencePending}})
	} else {
		add(healthComponent{Name: "request-persistence", State: "healthy", Healthy: true})
	}

	for name, store := range h.journals {
		if store == nil {
			continue
		}
		status, stats := store.Status(), store.Stats()
		healthy := status.State == "healthy" && stats.CompactionHealthy
		componentState := "healthy"
		if !healthy {
			componentState = "degraded"
		}
		add(healthComponent{Name: "journal:" + name, State: componentState, Healthy: healthy, Details: map[string]any{
			"state": status.State, "failedStage": status.FailedStage, "failureCount": status.FailureCount,
			"entries": stats.Entries, "sizeBytes": stats.SizeBytes, "compactionHealthy": stats.CompactionHealthy,
		}})
	}

	if h.captures != nil {
		captureStatus := h.captures.Status()
		state := "healthy"
		if !captureStatus.Available || !captureStatus.PersistenceHealthy {
			state = "degraded"
		}
		add(healthComponent{Name: "capture", State: state, Healthy: captureStatus.Available && captureStatus.PersistenceHealthy, Details: map[string]any{
			"available": captureStatus.Available, "active": captureStatus.Active, "storageBytes": captureStatus.StorageBytes,
			"maxTotalBytes": captureStatus.MaxTotalBytes, "failureCount": captureStatus.FailureCount, "failedStage": captureStatus.FailedStage,
		}})
	}

	if h.upstreamStatus != nil {
		upstream := h.upstreamStatus()
		open := 0
		for _, target := range upstream.Targets {
			if target.State == "open" {
				open++
			}
		}
		add(healthComponent{Name: "upstream", State: map[bool]string{true: "degraded", false: "healthy"}[open > 0], Healthy: open == 0, Details: map[string]any{
			"strategy": upstream.Strategy, "targets": len(upstream.Targets), "openCircuits": open,
		}})
	}

	if h.governanceStatus != nil {
		governance := h.governanceStatus()
		add(healthComponent{Name: "governance", State: map[bool]string{true: "healthy", false: "degraded"}[governance.Ledger.Healthy], Healthy: governance.Ledger.Healthy, Details: map[string]any{
			"ledgerState": governance.Ledger.State, "persistenceFailures": governance.Counters.PersistenceFailures,
		}})
	}
	if h.telemetryStatus != nil {
		telemetry := h.telemetryStatus()
		add(healthComponent{Name: "telemetry", State: map[bool]string{true: "healthy", false: "degraded"}[telemetry.Healthy], Healthy: telemetry.Healthy, Details: map[string]any{
			"enabled": telemetry.Enabled, "traceHealthy": telemetry.TraceHealthy, "metricHealthy": telemetry.MetricHealthy,
		}})
	}
	if h.monitor != nil {
		cfg := h.store.Get()
		if !cfg.SLO.Enabled {
			add(healthComponent{Name: "slo", State: "disabled", Healthy: true})
		} else {
			slo := h.monitor.SLO(cfg.SLO.Window.Duration, cfg.SLO.AvailabilityTarget, cfg.SLO.RecoveryLatencyTarget.Duration)
			add(healthComponent{Name: "slo", State: map[bool]string{true: "healthy", false: "degraded"}[slo.Healthy], Healthy: slo.Healthy, Details: map[string]any{"availability": slo.Availability, "errorBudgetRemaining": slo.ErrorBudgetRemaining, "burnRate": slo.BurnRate}})
		}
	}
	configState := h.store.State()
	if configState.PendingRestart.RestartRequired {
		add(healthComponent{Name: "configuration", State: "pending-restart", Healthy: true, Details: map[string]any{
			"activeRevision": configState.ActiveRevision, "desiredRevision": configState.DesiredRevision,
		}})
		result.Actions = append(result.Actions, "restart-to-apply-configuration")
	} else {
		add(healthComponent{Name: "configuration", State: "healthy", Healthy: true, Details: map[string]any{"revision": configState.ActiveRevision}})
	}
	if snapshot.PersistenceDegraded {
		result.Actions = append(result.Actions, "run-diagnostics-and-check-journal")
	}
	writeJSON(writer, http.StatusOK, result)
}
