package monitoring

import (
	"sync"
	"time"
)

const minuteBucketCount = 24 * 60

var (
	durationBucketLabels = [...]string{"<=1s", "<=5s", "<=15s", "<=30s", "<=1m", "<=5m", "<=15m", "<=1h", ">1h"}
	durationBucketLimits = [...]int64{1_000, 5_000, 15_000, 30_000, 60_000, 300_000, 900_000, 3_600_000}
	attemptBucketLabels  = [...]string{"1", "2", "3", "4-5", "6-10", "11-20", ">20"}
	attemptBucketLimits  = [...]int{1, 2, 3, 5, 10, 20}
	errorCodes           = [...]string{"transport", "protocol", "auth", "rate_limit", "client", "server", "http"}
)

type Counters struct {
	Requests       uint64 `json:"requests"`
	Successful     uint64 `json:"successful"`
	Failed         uint64 `json:"failed"`
	Canceled       uint64 `json:"canceled"`
	Rejected       uint64 `json:"rejected"`
	Attempts       uint64 `json:"attempts"`
	FailedAttempts uint64 `json:"failedAttempts"`
	Recovered      uint64 `json:"recovered"`
}

type Totals struct {
	Counters
	SuccessRate                 float64 `json:"successRate"`
	AverageRecoveryMilliseconds float64 `json:"averageRecoveryMilliseconds"`
}

type Load struct {
	Active     int `json:"active"`
	Queued     int `json:"queued"`
	Waiting    int `json:"waiting"`
	Requesting int `json:"requesting"`
}

type Point struct {
	Time time.Time `json:"time"`
	Counters
	Load
}

type HistogramBucket struct {
	Bucket string `json:"bucket"`
	Count  uint64 `json:"count"`
}

type Recovery struct {
	DurationBuckets []HistogramBucket `json:"durationBuckets"`
	AttemptBuckets  []HistogramBucket `json:"attemptBuckets"`
}

type Metrics struct {
	GeneratedAt time.Time `json:"generatedAt"`
	DataSince   time.Time `json:"dataSince"`
	Complete    bool      `json:"complete"`
	Window      string    `json:"window"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Totals      Totals    `json:"totals"`
	Load        Load      `json:"load"`
	Series      []Point   `json:"series"`
	Recovery    Recovery  `json:"recovery"`
}

type ErrorCategory struct {
	Code  string `json:"code"`
	Count uint64 `json:"count"`
}

type Errors struct {
	GeneratedAt time.Time       `json:"generatedAt"`
	DataSince   time.Time       `json:"dataSince"`
	Complete    bool            `json:"complete"`
	Window      string          `json:"window"`
	From        time.Time       `json:"from"`
	To          time.Time       `json:"to"`
	Categories  []ErrorCategory `json:"categories"`
}

type minuteBucket struct {
	valid                bool
	minute               int64
	counters             Counters
	load                 Load
	peak                 Load
	recoveryMilliseconds uint64
	durationBuckets      [len(durationBucketLabels)]uint64
	attemptBuckets       [len(attemptBucketLabels)]uint64
	errors               [len(errorCodes)]uint64
}

type Store struct {
	mu        sync.Mutex
	buckets   [minuteBucketCount]minuteBucket
	load      Load
	startedAt time.Time
	now       func() time.Time
	events    eventRing
}

func New() *Store {
	now := time.Now().UTC()
	return &Store{startedAt: now, now: time.Now, events: newEventRing()}
}

func ParseWindow(value string) (time.Duration, bool) {
	switch value {
	case "15m":
		return 15 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "6h":
		return 6 * time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

func (s *Store) RecordReceived() {
	s.updateCurrent(func(bucket *minuteBucket) { bucket.counters.Requests++ })
}

func (s *Store) RecordAttempt() {
	s.updateCurrent(func(bucket *minuteBucket) { bucket.counters.Attempts++ })
}

func (s *Store) RecordAttemptFailure(category string) {
	s.updateCurrent(func(bucket *minuteBucket) {
		bucket.counters.FailedAttempts++
		bucket.errors[errorIndex(category)]++
	})
}

func (s *Store) RecordRecovery(elapsed time.Duration, attempts int) {
	milliseconds := max(elapsed.Milliseconds(), 0)
	s.updateCurrent(func(bucket *minuteBucket) {
		bucket.counters.Recovered++
		bucket.recoveryMilliseconds += uint64(milliseconds)
		bucket.durationBuckets[durationBucketIndex(milliseconds)]++
		bucket.attemptBuckets[attemptBucketIndex(attempts)]++
	})
}

func (s *Store) RecordFinal(outcome string) {
	s.updateCurrent(func(bucket *minuteBucket) {
		switch outcome {
		case "successful":
			bucket.counters.Successful++
		case "failed":
			bucket.counters.Failed++
		case "canceled":
			bucket.counters.Canceled++
		case "rejected":
			bucket.counters.Rejected++
		}
	})
}

func (s *Store) SetLoad(active, queued, waiting, requesting int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load = Load{Active: active, Queued: queued, Waiting: waiting, Requesting: requesting}
	bucket := s.currentBucketLocked(s.now().UTC())
	bucket.load = s.load
	maximizeLoad(&bucket.peak, s.load)
}

func (s *Store) Metrics(window time.Duration) Metrics {
	now := s.now().UTC()
	currentMinute := now.Truncate(time.Minute)
	minutes := int(window / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	if minutes > minuteBucketCount {
		minutes = minuteBucketCount
	}
	from := currentMinute.Add(-time.Duration(minutes-1) * time.Minute)

	s.mu.Lock()
	defer s.mu.Unlock()
	result := Metrics{
		GeneratedAt: now,
		DataSince:   s.dataSinceLocked(currentMinute),
		Complete:    !s.startedAt.After(from),
		Window:      windowLabel(window),
		From:        from,
		To:          now,
		Load:        s.load,
		Series:      make([]Point, 0, minutes),
	}
	var durationCounts [len(durationBucketLabels)]uint64
	var attemptCounts [len(attemptBucketLabels)]uint64
	var recoveryMilliseconds uint64
	load := s.loadBeforeLocked(from.Unix() / 60)
	for offset := 0; offset < minutes; offset++ {
		pointTime := from.Add(time.Duration(offset) * time.Minute)
		bucket, found := s.bucketLocked(pointTime.Unix() / 60)
		point := Point{Time: pointTime, Load: load}
		if found {
			point.Counters = bucket.counters
			point.Load = bucket.peak
			load = bucket.load
			addCounters(&result.Totals.Counters, bucket.counters)
			recoveryMilliseconds += bucket.recoveryMilliseconds
			for index := range durationCounts {
				durationCounts[index] += bucket.durationBuckets[index]
			}
			for index := range attemptCounts {
				attemptCounts[index] += bucket.attemptBuckets[index]
			}
		}
		result.Series = append(result.Series, point)
	}
	if result.Totals.Requests > 0 {
		result.Totals.SuccessRate = float64(result.Totals.Successful) * 100 / float64(result.Totals.Requests)
	}
	if result.Totals.Recovered > 0 {
		result.Totals.AverageRecoveryMilliseconds = float64(recoveryMilliseconds) / float64(result.Totals.Recovered)
	}
	result.Recovery.DurationBuckets = histogram(durationBucketLabels[:], durationCounts[:])
	result.Recovery.AttemptBuckets = histogram(attemptBucketLabels[:], attemptCounts[:])
	return result
}

func (s *Store) Errors() Errors { return s.ErrorsFor(24 * time.Hour) }

func (s *Store) ErrorsFor(window time.Duration) Errors {
	now := s.now().UTC()
	currentMinute := now.Truncate(time.Minute)
	minutes := int(window / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	if minutes > minuteBucketCount {
		minutes = minuteBucketCount
	}
	from := currentMinute.Add(-time.Duration(minutes-1) * time.Minute)
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Errors{
		GeneratedAt: now,
		DataSince:   s.dataSinceLocked(currentMinute),
		Complete:    !s.startedAt.After(from),
		Window:      windowLabel(window),
		From:        from,
		To:          now,
		Categories:  make([]ErrorCategory, len(errorCodes)),
	}
	for index, code := range errorCodes {
		result.Categories[index].Code = code
	}
	for offset := 0; offset < minutes; offset++ {
		bucket, found := s.bucketLocked(from.Add(time.Duration(offset)*time.Minute).Unix() / 60)
		if !found {
			continue
		}
		for index := range result.Categories {
			result.Categories[index].Count += bucket.errors[index]
		}
	}
	return result
}

func (s *Store) updateCurrent(update func(*minuteBucket)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(s.currentBucketLocked(s.now().UTC()))
}

func (s *Store) currentBucketLocked(now time.Time) *minuteBucket {
	minute := now.Unix() / 60
	index := bucketIndex(minute)
	bucket := &s.buckets[index]
	if !bucket.valid || bucket.minute != minute {
		*bucket = minuteBucket{valid: true, minute: minute, load: s.load, peak: s.load}
	}
	return bucket
}

func (s *Store) bucketLocked(minute int64) (minuteBucket, bool) {
	bucket := s.buckets[bucketIndex(minute)]
	return bucket, bucket.valid && bucket.minute == minute
}

func (s *Store) loadBeforeLocked(minute int64) Load {
	var selected minuteBucket
	found := false
	for _, bucket := range s.buckets {
		if !bucket.valid || bucket.minute >= minute || found && bucket.minute <= selected.minute {
			continue
		}
		selected, found = bucket, true
	}
	if found {
		return selected.load
	}
	return Load{}
}

func (s *Store) dataSinceLocked(currentMinute time.Time) time.Time {
	retentionStart := currentMinute.Add(-(minuteBucketCount - 1) * time.Minute)
	if s.startedAt.After(retentionStart) {
		return s.startedAt
	}
	return retentionStart
}

func bucketIndex(minute int64) int {
	index := int(minute % minuteBucketCount)
	if index < 0 {
		index += minuteBucketCount
	}
	return index
}

func addCounters(target *Counters, value Counters) {
	target.Requests += value.Requests
	target.Successful += value.Successful
	target.Failed += value.Failed
	target.Canceled += value.Canceled
	target.Rejected += value.Rejected
	target.Attempts += value.Attempts
	target.FailedAttempts += value.FailedAttempts
	target.Recovered += value.Recovered
}

func durationBucketIndex(milliseconds int64) int {
	for index, limit := range durationBucketLimits {
		if milliseconds <= limit {
			return index
		}
	}
	return len(durationBucketLabels) - 1
}

func attemptBucketIndex(attempts int) int {
	for index, limit := range attemptBucketLimits {
		if attempts <= limit {
			return index
		}
	}
	return len(attemptBucketLabels) - 1
}

func errorIndex(category string) int {
	for index, code := range errorCodes {
		if category == code {
			return index
		}
	}
	return len(errorCodes) - 1
}

func histogram(labels []string, counts []uint64) []HistogramBucket {
	result := make([]HistogramBucket, len(labels))
	for index, label := range labels {
		result[index] = HistogramBucket{Bucket: label, Count: counts[index]}
	}
	return result
}

func maximizeLoad(target *Load, value Load) {
	target.Active = max(target.Active, value.Active)
	target.Queued = max(target.Queued, value.Queued)
	target.Waiting = max(target.Waiting, value.Waiting)
	target.Requesting = max(target.Requesting, value.Requesting)
}

func windowLabel(window time.Duration) string {
	switch window {
	case 15 * time.Minute:
		return "15m"
	case time.Hour:
		return "1h"
	case 6 * time.Hour:
		return "6h"
	default:
		return "24h"
	}
}
