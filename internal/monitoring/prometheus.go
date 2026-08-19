package monitoring

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/governance"
	"github.com/areasong/relay-lifeline/internal/telemetry"
)

type PersistenceMetric struct {
	Journal                 string
	Entries                 int
	SizeBytes               int64
	ReplayDurationSeconds   float64
	LastCompactionTimestamp float64
	LastCompactionSeconds   float64
	LastCompactionRemoved   int
	Healthy                 bool
	CompactionHealthy       bool
	State                   string
	FailedAtTimestamp       float64
	FailureCount            uint64
	FailedStage             string
}

func (s *Store) SetPersistenceProvider(provider func() []PersistenceMetric) {
	s.mu.Lock()
	s.persistence = provider
	s.mu.Unlock()
}

func (s *Store) SetGovernanceProvider(provider func() governance.Snapshot) {
	s.mu.Lock()
	s.governance = provider
	s.mu.Unlock()
}

func (s *Store) SetTelemetryProvider(provider func() telemetry.Status) {
	s.mu.Lock()
	s.telemetry = provider
	s.mu.Unlock()
}

func (s *Store) SetControlModeProvider(provider func() string) {
	s.mu.Lock()
	s.controlMode = provider
	s.mu.Unlock()
}

func (s *Store) SetUncertainProvider(provider func() UncertainStatus) {
	s.mu.Lock()
	s.uncertain = provider
	s.mu.Unlock()
}

func (s *Store) PrometheusHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		_, _ = writer.Write([]byte(s.Prometheus()))
	})
}

func (s *Store) Prometheus() string {
	metrics := s.Metrics(24 * time.Hour)
	slo := s.SLO(24*time.Hour, 0.99, 30*time.Second)
	errors := s.ErrorsFor(24 * time.Hour)
	var output strings.Builder
	writeMetric := func(name, help, metricType string, value any) {
		fmt.Fprintf(&output, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, metricType, name, value)
	}
	writeMetric("relay_lifeline_requests_24h", "Requests received in the retained 24 hour window.", "gauge", metrics.Totals.Requests)
	writeMetric("relay_lifeline_successful_requests_24h", "Successful requests in the retained 24 hour window.", "gauge", metrics.Totals.Successful)
	writeMetric("relay_lifeline_failed_requests_24h", "Failed requests in the retained 24 hour window.", "gauge", metrics.Totals.Failed)
	writeMetric("relay_lifeline_canceled_requests_24h", "Canceled requests in the retained 24 hour window.", "gauge", metrics.Totals.Canceled)
	writeMetric("relay_lifeline_rejected_requests_24h", "Rejected requests in the retained 24 hour window.", "gauge", metrics.Totals.Rejected)
	writeMetric("relay_lifeline_expired_requests_24h", "Expired requests in the retained 24 hour window.", "gauge", metrics.Totals.Expired)
	writeMetric("relay_lifeline_attempts_24h", "Upstream attempts in the retained 24 hour window.", "gauge", metrics.Totals.Attempts)
	writeMetric("relay_lifeline_failed_attempts_24h", "Failed upstream attempts in the retained 24 hour window.", "gauge", metrics.Totals.FailedAttempts)
	writeMetric("relay_lifeline_recovered_requests_24h", "Requests recovered after at least one failure in the retained 24 hour window.", "gauge", metrics.Totals.Recovered)
	writeMetric("relay_lifeline_failovers_24h", "Upstream target switches in the retained 24 hour window.", "gauge", metrics.Totals.Failovers)
	writeMetric("relay_lifeline_uncertain_deliveries_24h", "Deliveries whose upstream write outcome is uncertain in the retained 24 hour window.", "gauge", metrics.Totals.Uncertain)
	writeMetric("relay_lifeline_uncertain_confirmed_total_24h", "Uncertain deliveries manually confirmed successful in the retained 24 hour window.", "gauge", metrics.Totals.UncertainConfirmed)
	writeMetric("relay_lifeline_uncertain_abandoned_total_24h", "Uncertain deliveries manually abandoned in the retained 24 hour window.", "gauge", metrics.Totals.UncertainAbandoned)
	writeMetric("relay_lifeline_uncertain_compensation_requested_total_24h", "Compensation requests approved for uncertain deliveries in the retained 24 hour window.", "gauge", metrics.Totals.UncertainCompensated)
	writeMetric("relay_lifeline_persistence_failures_24h", "Request persistence failures in the retained 24 hour window.", "gauge", metrics.Totals.PersistenceFailures)
	writeMetric("relay_lifeline_capture_persistence_failures_24h", "Capture persistence failures in the retained 24 hour window.", "gauge", metrics.Totals.CaptureFailures)
	writeMetric("relay_lifeline_slo_availability_ratio_24h", "Observed availability ratio in the retained 24 hour window.", "gauge", slo.Availability)
	writeMetric("relay_lifeline_slo_error_budget_remaining_ratio_24h", "Remaining availability error budget ratio.", "gauge", slo.ErrorBudgetRemaining)
	writeMetric("relay_lifeline_slo_burn_rate_24h", "Availability error budget burn rate.", "gauge", slo.BurnRate)
	writeMetric("relay_lifeline_slo_recovery_latency_milliseconds_24h", "Average recovery latency in milliseconds.", "gauge", slo.RecoveryLatencyMillis)
	writeMetric("relay_lifeline_active_requests", "Requests currently owned by Relay-Lifeline.", "gauge", metrics.Load.Active)
	writeMetric("relay_lifeline_queued_requests", "Requests waiting for an active concurrency slot.", "gauge", metrics.Load.Queued)
	writeMetric("relay_lifeline_waiting_requests", "Requests waiting before another upstream attempt.", "gauge", metrics.Load.Waiting)
	writeMetric("relay_lifeline_requesting_requests", "Requests currently attempting the upstream.", "gauge", metrics.Load.Requesting)
	output.WriteString("# HELP relay_lifeline_failed_attempts_by_category_24h Failed attempts by bounded category in the retained 24 hour window.\n")
	output.WriteString("# TYPE relay_lifeline_failed_attempts_by_category_24h gauge\n")
	for _, category := range errors.Categories {
		fmt.Fprintf(&output, "relay_lifeline_failed_attempts_by_category_24h{category=%q} %d\n", category.Code, category.Count)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	writeMetric("relay_lifeline_process_pid", "PID in the current process namespace.", "gauge", os.Getpid())
	writeMetric("relay_lifeline_process_goroutines", "Current Go goroutine count.", "gauge", runtime.NumGoroutine())
	writeMetric("relay_lifeline_process_heap_alloc_bytes", "Bytes currently allocated on the Go heap.", "gauge", memory.HeapAlloc)
	writeMetric("relay_lifeline_process_heap_inuse_bytes", "Bytes in spans currently used by the Go heap.", "gauge", memory.HeapInuse)
	writeMetric("relay_lifeline_process_system_memory_bytes", "Total bytes obtained from the operating system by the Go runtime.", "gauge", memory.Sys)
	writeMetric("relay_lifeline_process_gc_cycles", "Completed Go garbage collection cycles.", "counter", memory.NumGC)
	s.mu.Lock()
	persistenceProvider, governanceProvider, telemetryProvider, controlModeProvider, uncertainProvider := s.persistence, s.governance, s.telemetry, s.controlMode, s.uncertain
	s.mu.Unlock()
	if uncertainProvider != nil {
		status := uncertainProvider()
		writeMetric("relay_lifeline_uncertain_open", "Uncertain deliveries awaiting an operator decision.", "gauge", status.Open)
		writeMetric("relay_lifeline_uncertain_oldest_seconds", "Age in seconds of the oldest unresolved uncertain delivery.", "gauge", status.OldestSeconds)
		writeMetric("relay_lifeline_uncertain_resolution_target_seconds", "Configured uncertain-delivery resolution target in seconds.", "gauge", status.TargetSeconds)
		healthy := status.Open == 0 || status.OldestSeconds <= status.TargetSeconds
		writeMetric("relay_lifeline_uncertain_slo_healthy", "Whether open uncertain deliveries meet the configured resolution target.", "gauge", boolMetric(healthy))
	}
	if persistenceProvider != nil {
		writePersistenceMetrics(&output, persistenceProvider())
	}
	if governanceProvider != nil {
		writeGovernanceMetrics(&output, governanceProvider())
	}
	if telemetryProvider != nil {
		writeTelemetryMetrics(&output, telemetryProvider())
	}
	if controlModeProvider != nil {
		mode := controlModeProvider()
		fmt.Fprintln(&output, "# HELP relay_lifeline_control_mode Current administrative control mode, one active label is 1.")
		fmt.Fprintln(&output, "# TYPE relay_lifeline_control_mode gauge")
		for _, candidate := range []string{"running", "paused", "draining", "maintenance"} {
			value := 0
			if candidate == mode {
				value = 1
			}
			fmt.Fprintf(&output, "relay_lifeline_control_mode{mode=%q} %d\n", candidate, value)
		}
	}
	return output.String()
}

func writeTelemetryMetrics(output *strings.Builder, status telemetry.Status) {
	for _, definition := range []struct {
		name       string
		help       string
		metricType string
		value      any
	}{
		{"relay_lifeline_telemetry_enabled", "Whether OpenTelemetry export is enabled.", "gauge", boolMetric(status.Enabled)},
		{"relay_lifeline_telemetry_healthy", "Whether all enabled OpenTelemetry signals are exporting successfully.", "gauge", boolMetric(status.Healthy)},
		{"relay_lifeline_telemetry_trace_export_failures_total", "Trace export failures since startup.", "counter", status.TraceExportFailures},
		{"relay_lifeline_telemetry_metric_export_failures_total", "Metric export failures since startup.", "counter", status.MetricExportFailures},
	} {
		fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", definition.name, definition.help, definition.name, definition.metricType, definition.name, definition.value)
	}
	lastFailure := float64(0)
	if !status.LastFailureAt.IsZero() {
		lastFailure = float64(status.LastFailureAt.Unix())
	}
	fmt.Fprintln(output, "# HELP relay_lifeline_telemetry_last_failure_timestamp_seconds Unix timestamp of the most recent telemetry export failure.")
	fmt.Fprintln(output, "# TYPE relay_lifeline_telemetry_last_failure_timestamp_seconds gauge")
	fmt.Fprintf(output, "relay_lifeline_telemetry_last_failure_timestamp_seconds %g\n", lastFailure)
}

func writeGovernanceMetrics(output *strings.Builder, snapshot governance.Snapshot) {
	definitions := []struct {
		name       string
		help       string
		metricType string
		value      any
	}{
		{"relay_lifeline_governance_active_reservations", "Currently active governance reservations.", "gauge", snapshot.Reservations},
		{"relay_lifeline_governance_admitted_total", "Governance reservations admitted since startup, including replayed entries.", "counter", snapshot.Counters.Admitted},
		{"relay_lifeline_governance_settlements_total", "Governance usage settlements since startup, including replayed entries.", "counter", snapshot.Counters.Settlements},
		{"relay_lifeline_governance_known_settlements_total", "Known governance usage settlements since startup.", "counter", snapshot.Counters.KnownSettlements},
		{"relay_lifeline_governance_unknown_settlements_total", "Unknown governance usage settlements since startup.", "counter", snapshot.Counters.UnknownSettlements},
		{"relay_lifeline_governance_reconciled_total", "Reservations reconciled after an interrupted process.", "counter", snapshot.Counters.Reconciled},
		{"relay_lifeline_governance_persistence_failures_total", "Governance ledger persistence failures since startup.", "counter", snapshot.Counters.PersistenceFailures},
		{"relay_lifeline_governance_ledger_healthy", "Whether the governance usage ledger is healthy.", "gauge", boolMetric(snapshot.Ledger.Healthy)},
	}
	for _, definition := range definitions {
		fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", definition.name, definition.help, definition.name, definition.metricType, definition.name, definition.value)
	}
	var tokens, cost, reservedTokens, reservedCost int64
	unknown := 0
	for _, entry := range snapshot.Entries {
		if entry.Scope != "" && entry.Scope != "principal" {
			continue
		}
		tokens += entry.Tokens
		cost += entry.CostMicros
		reservedTokens += entry.ReservedTokens
		reservedCost += entry.ReservedCostMicros
		unknown += entry.UnknownUsage
	}
	fmt.Fprintln(output, "# HELP relay_lifeline_governance_window_tokens Tokens settled in active governance windows.")
	fmt.Fprintln(output, "# TYPE relay_lifeline_governance_window_tokens gauge")
	fmt.Fprintf(output, "relay_lifeline_governance_window_tokens %d\n", tokens)
	fmt.Fprintln(output, "# HELP relay_lifeline_governance_window_cost_micros Cost settled in active governance windows.")
	fmt.Fprintln(output, "# TYPE relay_lifeline_governance_window_cost_micros gauge")
	fmt.Fprintf(output, "relay_lifeline_governance_window_cost_micros %d\n", cost)
	fmt.Fprintln(output, "# HELP relay_lifeline_governance_window_reserved_tokens Tokens reserved by active governance admissions.")
	fmt.Fprintln(output, "# TYPE relay_lifeline_governance_window_reserved_tokens gauge")
	fmt.Fprintf(output, "relay_lifeline_governance_window_reserved_tokens %d\n", reservedTokens)
	fmt.Fprintln(output, "# HELP relay_lifeline_governance_window_reserved_cost_micros Cost reserved by active governance admissions.")
	fmt.Fprintln(output, "# TYPE relay_lifeline_governance_window_reserved_cost_micros gauge")
	fmt.Fprintf(output, "relay_lifeline_governance_window_reserved_cost_micros %d\n", reservedCost)
	fmt.Fprintln(output, "# HELP relay_lifeline_governance_window_unknown_usage Unknown usage settlements in active governance windows.")
	fmt.Fprintln(output, "# TYPE relay_lifeline_governance_window_unknown_usage gauge")
	fmt.Fprintf(output, "relay_lifeline_governance_window_unknown_usage %d\n", unknown)
	fmt.Fprintln(output, "# HELP relay_lifeline_governance_rejected_total Governance admissions rejected by bounded reason.")
	fmt.Fprintln(output, "# TYPE relay_lifeline_governance_rejected_total counter")
	for _, reason := range []string{"concurrent_limit", "rate_limit", "token_limit", "cost_limit", "unknown_usage", "tenant_required", "ledger_unavailable"} {
		fmt.Fprintf(output, "relay_lifeline_governance_rejected_total{reason=%q} %d\n", reason, snapshot.Counters.Rejected[reason])
	}
}

func writePersistenceMetrics(output *strings.Builder, journals []PersistenceMetric) {
	definitions := []struct{ name, help string }{
		{"relay_lifeline_journal_entries", "Events currently retained in the journal."},
		{"relay_lifeline_journal_size_bytes", "Current journal file size in bytes."},
		{"relay_lifeline_journal_replay_duration_seconds", "Duration of the most recent startup replay."},
		{"relay_lifeline_journal_last_compaction_timestamp_seconds", "Unix timestamp of the most recent successful compaction."},
		{"relay_lifeline_journal_last_compaction_duration_seconds", "Duration of the most recent compaction attempt."},
		{"relay_lifeline_journal_last_compaction_removed_entries", "Entries removed by the most recent successful compaction."},
		{"relay_lifeline_journal_healthy", "Whether the journal is open and writable."},
		{"relay_lifeline_journal_compaction_healthy", "Whether the most recent compaction attempt succeeded."},
		{"relay_lifeline_journal_failed_at_timestamp_seconds", "Unix timestamp of the most recent journal failure."},
		{"relay_lifeline_journal_failure_count", "Number of journal failures observed since startup."},
	}
	for _, definition := range definitions {
		fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s gauge\n", definition.name, definition.help, definition.name)
	}
	for _, item := range journals {
		label := fmt.Sprintf("{journal=%q}", item.Journal)
		fmt.Fprintf(output, "relay_lifeline_journal_entries%s %d\n", label, item.Entries)
		fmt.Fprintf(output, "relay_lifeline_journal_size_bytes%s %d\n", label, item.SizeBytes)
		fmt.Fprintf(output, "relay_lifeline_journal_replay_duration_seconds%s %g\n", label, item.ReplayDurationSeconds)
		fmt.Fprintf(output, "relay_lifeline_journal_last_compaction_timestamp_seconds%s %g\n", label, item.LastCompactionTimestamp)
		fmt.Fprintf(output, "relay_lifeline_journal_last_compaction_duration_seconds%s %g\n", label, item.LastCompactionSeconds)
		fmt.Fprintf(output, "relay_lifeline_journal_last_compaction_removed_entries%s %d\n", label, item.LastCompactionRemoved)
		fmt.Fprintf(output, "relay_lifeline_journal_healthy%s %d\n", label, boolMetric(item.Healthy))
		fmt.Fprintf(output, "relay_lifeline_journal_compaction_healthy%s %d\n", label, boolMetric(item.CompactionHealthy))
		fmt.Fprintf(output, "relay_lifeline_journal_failed_at_timestamp_seconds%s %g\n", label, item.FailedAtTimestamp)
		fmt.Fprintf(output, "relay_lifeline_journal_failure_count%s %d\n", label, item.FailureCount)
		if item.FailedStage != "" {
			fmt.Fprintf(output, "relay_lifeline_journal_failure_stage{journal=%q,stage=%q} 1\n", item.Journal, item.FailedStage)
		}
	}
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}
