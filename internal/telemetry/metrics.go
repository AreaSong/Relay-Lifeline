package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var runtimeInstruments = newRuntimeInstruments()

type instruments struct {
	requests              metric.Int64Counter
	attempts              metric.Int64Counter
	attemptFailures       metric.Int64Counter
	requestOutcomes       metric.Int64Counter
	recoveryDuration      metric.Float64Histogram
	load                  metric.Int64Gauge
	governanceAdmissions  metric.Int64Counter
	governanceActive      metric.Int64UpDownCounter
	governanceSettlements metric.Int64Counter
	governanceTokens      metric.Int64Counter
	governanceCost        metric.Int64Counter
	journalAppends        metric.Int64Counter
	journalDuration       metric.Float64Histogram
}

func newRuntimeInstruments() instruments {
	meter := Meter("relay-lifeline/runtime")
	requests, _ := meter.Int64Counter("relay_lifeline.requests")
	attempts, _ := meter.Int64Counter("relay_lifeline.attempts")
	attemptFailures, _ := meter.Int64Counter("relay_lifeline.attempt_failures")
	requestOutcomes, _ := meter.Int64Counter("relay_lifeline.request_outcomes")
	recoveryDuration, _ := meter.Float64Histogram("relay_lifeline.recovery.duration", metric.WithUnit("s"))
	load, _ := meter.Int64Gauge("relay_lifeline.load")
	governanceAdmissions, _ := meter.Int64Counter("relay_lifeline.governance.admissions")
	governanceActive, _ := meter.Int64UpDownCounter("relay_lifeline.governance.active_reservations")
	governanceSettlements, _ := meter.Int64Counter("relay_lifeline.governance.settlements")
	governanceTokens, _ := meter.Int64Counter("relay_lifeline.governance.tokens")
	governanceCost, _ := meter.Int64Counter("relay_lifeline.governance.cost", metric.WithUnit("{micro_currency}"))
	journalAppends, _ := meter.Int64Counter("relay_lifeline.journal.appends")
	journalDuration, _ := meter.Float64Histogram("relay_lifeline.journal.append.duration", metric.WithUnit("s"))
	return instruments{
		requests: requests, attempts: attempts, attemptFailures: attemptFailures, requestOutcomes: requestOutcomes,
		recoveryDuration: recoveryDuration, load: load, governanceAdmissions: governanceAdmissions,
		governanceActive: governanceActive, governanceSettlements: governanceSettlements, governanceTokens: governanceTokens,
		governanceCost: governanceCost, journalAppends: journalAppends, journalDuration: journalDuration,
	}
}

func RecordRequestReceived(ctx context.Context) {
	runtimeInstruments.requests.Add(ctx, 1)
}

func RecordAttempt(ctx context.Context) {
	runtimeInstruments.attempts.Add(ctx, 1)
}

func RecordAttemptFailure(ctx context.Context, category string) {
	runtimeInstruments.attemptFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("error.type", category)))
}

func RecordRequestOutcome(ctx context.Context, outcome string) {
	runtimeInstruments.requestOutcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("relay.outcome", outcome)))
}

func RecordRecovery(ctx context.Context, elapsed time.Duration, attempts int) {
	runtimeInstruments.recoveryDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attribute.Int("relay.attempts", attempts)))
}

func RecordLoad(ctx context.Context, active, queued, waiting, requesting int) {
	for state, value := range map[string]int{"active": active, "queued": queued, "waiting": waiting, "requesting": requesting} {
		runtimeInstruments.load.Record(ctx, int64(value), metric.WithAttributes(attribute.String("relay.state", state)))
	}
}

func RecordGovernanceAdmission(ctx context.Context, allowed bool, reason string) {
	if reason == "" {
		reason = "allowed"
	}
	runtimeInstruments.governanceAdmissions.Add(ctx, 1, metric.WithAttributes(attribute.Bool("relay.allowed", allowed), attribute.String("relay.reason", reason)))
}

func RecordGovernanceActive(ctx context.Context, delta int64) {
	runtimeInstruments.governanceActive.Add(ctx, delta)
}

func RecordGovernanceSettlement(ctx context.Context, known bool, tokens, costMicros int64) {
	attributes := metric.WithAttributes(attribute.Bool("relay.usage.known", known))
	runtimeInstruments.governanceSettlements.Add(ctx, 1, attributes)
	if known {
		runtimeInstruments.governanceTokens.Add(ctx, tokens)
		runtimeInstruments.governanceCost.Add(ctx, costMicros)
	}
}

func RecordJournalAppend(ctx context.Context, outcome string, elapsed time.Duration) {
	attributes := metric.WithAttributes(attribute.String("relay.outcome", outcome))
	runtimeInstruments.journalAppends.Add(ctx, 1, attributes)
	runtimeInstruments.journalDuration.Record(ctx, elapsed.Seconds(), attributes)
}
