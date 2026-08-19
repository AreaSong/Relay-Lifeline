package monitoring

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/areasong/relay-lifeline/internal/governance"
	"github.com/areasong/relay-lifeline/internal/telemetry"
)

func TestPrometheusHandlerExportsBoundedMetrics(t *testing.T) {
	store := New()
	store.RecordReceived()
	store.RecordAttempt()
	store.RecordAttemptFailure("server")
	store.RecordFailover()
	store.RecordUncertain()
	store.RecordPersistenceFailure()
	store.RecordCaptureFailure()
	store.SetLoad(2, 1, 1, 1)
	store.SetPersistenceProvider(func() []PersistenceMetric {
		return []PersistenceMetric{{Journal: "requests", Entries: 12, SizeBytes: 4096, Healthy: true, CompactionHealthy: true}}
	})
	store.SetGovernanceProvider(func() governance.Snapshot {
		return governance.Snapshot{
			Reservations: 2,
			Counters:     governance.Counters{Admitted: 3, UnknownSettlements: 1, Rejected: map[string]uint64{"rate_limit": 1}},
			Ledger:       governance.LedgerStatus{Enabled: true, Healthy: true},
			Entries: []governance.PrincipalUsage{
				{Scope: "principal", Key: "p", Tokens: 7, CostMicros: 11, UnknownUsage: 1},
				{Scope: "tenant", Key: "acme", Tokens: 7, CostMicros: 11, UnknownUsage: 1},
			},
		}
	})
	store.SetTelemetryProvider(func() telemetry.Status {
		return telemetry.Status{Enabled: true, Healthy: false, TraceHealthy: false, MetricHealthy: true, TraceExportFailures: 2}
	})
	recorder := httptest.NewRecorder()
	store.PrometheusHandler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Prometheus 响应异常: %d %s", recorder.Code, recorder.Header())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"relay_lifeline_requests_24h 1",
		"# TYPE relay_lifeline_failovers_24h gauge",
		"relay_lifeline_failovers_24h 1",
		"# TYPE relay_lifeline_uncertain_deliveries_24h gauge",
		"relay_lifeline_uncertain_deliveries_24h 1",
		"# TYPE relay_lifeline_persistence_failures_24h gauge",
		"relay_lifeline_persistence_failures_24h 1",
		"# TYPE relay_lifeline_capture_persistence_failures_24h gauge",
		"relay_lifeline_capture_persistence_failures_24h 1",
		"relay_lifeline_active_requests 2",
		`relay_lifeline_failed_attempts_by_category_24h{category="server"} 1`,
		`relay_lifeline_journal_entries{journal="requests"} 12`,
		`relay_lifeline_journal_size_bytes{journal="requests"} 4096`,
		`relay_lifeline_journal_healthy{journal="requests"} 1`,
		"relay_lifeline_governance_active_reservations 2",
		"relay_lifeline_governance_window_tokens 7",
		`relay_lifeline_governance_rejected_total{reason="rate_limit"} 1`,
		"relay_lifeline_telemetry_enabled 1",
		"relay_lifeline_telemetry_healthy 0",
		"relay_lifeline_telemetry_trace_export_failures_total 2",
		"relay_lifeline_process_pid ",
		"relay_lifeline_process_goroutines ",
		"relay_lifeline_process_heap_alloc_bytes ",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("Prometheus 输出缺少 %q:\n%s", expected, body)
		}
	}
}
