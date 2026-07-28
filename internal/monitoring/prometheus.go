package monitoring

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

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
	writeMetric("relay_lifeline_active_requests", "Requests currently owned by Transfer Lifeline.", "gauge", metrics.Load.Active)
	writeMetric("relay_lifeline_queued_requests", "Requests waiting for an active concurrency slot.", "gauge", metrics.Load.Queued)
	writeMetric("relay_lifeline_waiting_requests", "Requests waiting before another upstream attempt.", "gauge", metrics.Load.Waiting)
	writeMetric("relay_lifeline_requesting_requests", "Requests currently attempting the upstream.", "gauge", metrics.Load.Requesting)
	output.WriteString("# HELP relay_lifeline_failed_attempts_by_category_24h Failed attempts by bounded category in the retained 24 hour window.\n")
	output.WriteString("# TYPE relay_lifeline_failed_attempts_by_category_24h gauge\n")
	for _, category := range errors.Categories {
		fmt.Fprintf(&output, "relay_lifeline_failed_attempts_by_category_24h{category=%q} %d\n", category.Code, category.Count)
	}
	return output.String()
}
