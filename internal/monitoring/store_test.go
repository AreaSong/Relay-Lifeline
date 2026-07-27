package monitoring

import (
	"testing"
	"time"
)

func TestStoreAggregatesMinuteMetricsErrorsAndRecovery(t *testing.T) {
	current := time.Date(2026, 7, 27, 12, 14, 30, 0, time.UTC)
	store := New()
	store.now = func() time.Time { return current }
	store.startedAt = current.Add(-time.Hour)

	store.RecordReceived()
	store.SetLoad(1, 0, 0, 1)
	store.RecordAttempt()
	store.RecordAttemptFailure("server")
	store.RecordAttempt()
	store.RecordRecovery(7500*time.Millisecond, 2)
	store.RecordFinal("successful")
	store.SetLoad(0, 0, 0, 0)

	metrics := store.Metrics(15 * time.Minute)
	if metrics.Window != "15m" || len(metrics.Series) != 15 || !metrics.Complete {
		t.Fatalf("时间窗口异常: window=%s points=%d complete=%v", metrics.Window, len(metrics.Series), metrics.Complete)
	}
	if metrics.From != time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC) || metrics.To != current {
		t.Fatalf("时间边界异常: %s - %s", metrics.From, metrics.To)
	}
	if metrics.Totals.Requests != 1 || metrics.Totals.Successful != 1 || metrics.Totals.Attempts != 2 || metrics.Totals.FailedAttempts != 1 || metrics.Totals.Recovered != 1 {
		t.Fatalf("聚合计数异常: %+v", metrics.Totals)
	}
	if metrics.Totals.SuccessRate != 100 || metrics.Totals.AverageRecoveryMilliseconds != 7500 {
		t.Fatalf("派生指标异常: %+v", metrics.Totals)
	}
	if countForBucket(metrics.Recovery.DurationBuckets, "<=15s") != 1 || countForBucket(metrics.Recovery.AttemptBuckets, "2") != 1 {
		t.Fatalf("恢复直方图异常: %+v", metrics.Recovery)
	}
	if metrics.Load.Active != 0 || metrics.Load.Requesting != 0 {
		t.Fatalf("当前负载异常: %+v", metrics.Load)
	}
	lastPoint := metrics.Series[len(metrics.Series)-1]
	if lastPoint.Active != 1 || lastPoint.Requesting != 1 {
		t.Fatalf("分钟负载峰值异常: %+v", lastPoint.Load)
	}

	errors := store.Errors()
	if errors.Window != "24h" || len(errors.Categories) != len(errorCodes) || categoryCount(errors.Categories, "server") != 1 {
		t.Fatalf("错误分类异常: %+v", errors)
	}
}

func TestStoreRetainsExactly1440UTCMinuteBuckets(t *testing.T) {
	current := time.Date(2026, 7, 27, 0, 0, 10, 0, time.FixedZone("UTC+8", 8*60*60))
	store := New()
	store.now = func() time.Time { return current }
	store.startedAt = current.UTC().Add(-48 * time.Hour)
	store.RecordReceived()

	current = current.Add(1440 * time.Minute)
	store.RecordReceived()
	metrics := store.Metrics(24 * time.Hour)
	if metrics.Totals.Requests != 1 || len(metrics.Series) != minuteBucketCount {
		t.Fatalf("过期分钟桶未覆盖: requests=%d points=%d", metrics.Totals.Requests, len(metrics.Series))
	}
	if metrics.Series[len(metrics.Series)-1].Time.Location() != time.UTC {
		t.Fatalf("分钟桶未使用 UTC: %s", metrics.Series[len(metrics.Series)-1].Time.Location())
	}
}

func TestSecurityEventRingReportsCursorGapAndBounds(t *testing.T) {
	current := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	store := New()
	store.now = func() time.Time { return current }
	for index := 0; index < securityEventCapacity+5; index++ {
		store.RecordSecurityEvent(SecurityEvent{Code: "admin.pause", Outcome: "succeeded", Changed: Bool(true)})
	}

	page := store.Events(1, 2)
	if !page.HasGap || !page.HasMore || page.OldestAfter != 5 || page.NextAfter != 7 || len(page.Events) != 2 {
		t.Fatalf("事件游标异常: %+v", page)
	}
	if page.Events[0].ID != 6 || page.Events[0].Time != current {
		t.Fatalf("事件环起点异常: %+v", page.Events[0])
	}

	tail := store.Events(securityEventCapacity+5, 10)
	if len(tail.Events) != 0 || tail.HasMore || tail.NextAfter != securityEventCapacity+5 {
		t.Fatalf("事件尾游标异常: %+v", tail)
	}
}

func TestUnknownErrorCategoryIsBoundedToHTTP(t *testing.T) {
	store := New()
	store.RecordAttemptFailure("raw-provider-error")
	errors := store.Errors()
	if categoryCount(errors.Categories, "http") != 1 {
		t.Fatalf("未知错误未归入有限分类: %+v", errors.Categories)
	}
}

func TestStoreRecordsEveryTerminalOutcome(t *testing.T) {
	store := New()
	for _, outcome := range []string{"successful", "failed", "canceled", "rejected"} {
		store.RecordReceived()
		store.RecordFinal(outcome)
	}
	totals := store.Metrics(15 * time.Minute).Totals
	if totals.Requests != 4 || totals.Successful != 1 || totals.Failed != 1 || totals.Canceled != 1 || totals.Rejected != 1 {
		t.Fatalf("终态计数异常: %+v", totals)
	}
}

func countForBucket(buckets []HistogramBucket, label string) uint64 {
	for _, bucket := range buckets {
		if bucket.Bucket == label {
			return bucket.Count
		}
	}
	return 0
}

func categoryCount(categories []ErrorCategory, code string) uint64 {
	for _, category := range categories {
		if category.Code == code {
			return category.Count
		}
	}
	return 0
}
