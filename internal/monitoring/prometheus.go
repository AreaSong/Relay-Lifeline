package monitoring

import (
	"fmt"
	"net/http"
	"strings"
	"time"
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
}

func (s *Store) SetPersistenceProvider(provider func() []PersistenceMetric) {
	s.mu.Lock()
	s.persistence = provider
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
	errors := s.ErrorsFor(24 * time.Hour)
	var output strings.Builder
	writeMetric := func(name, help, metricType string, value any) {
		fmt.Fprintf(&output, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, metricType, name, value)
	}
	writeMetric("relay_lifeline_requests_24h", "Requests received in the retained 24 hour window.", "gauge", metrics.Totals.Requests)
	writeMetric("relay_lifeline_successful_requests_24h", "Successful requests in the retained 24 hour window.", "gauge", metrics.Totals.Successful)
	writeMetric("relay_lifeline_failed_attempts_24h", "Failed upstream attempts in the retained 24 hour window.", "gauge", metrics.Totals.FailedAttempts)
	writeMetric("relay_lifeline_recovered_requests_24h", "Requests recovered after at least one failure in the retained 24 hour window.", "gauge", metrics.Totals.Recovered)
	writeMetric("relay_lifeline_active_requests", "Requests currently owned by Relay-Lifeline.", "gauge", metrics.Load.Active)
	writeMetric("relay_lifeline_queued_requests", "Requests waiting for an active concurrency slot.", "gauge", metrics.Load.Queued)
	writeMetric("relay_lifeline_waiting_requests", "Requests waiting before another upstream attempt.", "gauge", metrics.Load.Waiting)
	writeMetric("relay_lifeline_requesting_requests", "Requests currently attempting the upstream.", "gauge", metrics.Load.Requesting)
	output.WriteString("# HELP relay_lifeline_failed_attempts_by_category_24h Failed attempts by bounded category in the retained 24 hour window.\n")
	output.WriteString("# TYPE relay_lifeline_failed_attempts_by_category_24h gauge\n")
	for _, category := range errors.Categories {
		fmt.Fprintf(&output, "relay_lifeline_failed_attempts_by_category_24h{category=%q} %d\n", category.Code, category.Count)
	}
	s.mu.Lock()
	persistenceProvider := s.persistence
	s.mu.Unlock()
	if persistenceProvider != nil {
		writePersistenceMetrics(&output, persistenceProvider())
	}
	return output.String()
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
	}
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}
