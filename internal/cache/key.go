package cache

import (
	"encoding/json"
	"time"

	"github.com/cespare/xxhash/v2"
)

// TimeClass classifies the requested window's `end` timestamp into one of four
// buckets (live | recent | historical | frozen).
type TimeClass string

const (
	ClassLive       TimeClass = "live"
	ClassRecent     TimeClass = "recent"
	ClassHistorical TimeClass = "historical"
	ClassFrozen     TimeClass = "frozen"
)

// Bucketing holds the resolved time-class and cache-related values for a query.
type Bucketing struct {
	Class         TimeClass
	BucketSeconds int
	StartActual   time.Time
	EndActual     time.Time
	TTL           time.Duration
	MaxAge        int // Cache-Control max-age seconds
}

// uniformBucket is the single bucket width used by every TimeClass. Aligning
// every request to the same one-minute grid gives callers a predictable
// `(start_actual, end_actual)` regardless of how recent the query is, while
// keeping per-class TTLs that match upstream-data volatility.
const uniformBucket = time.Minute

// Bucket classifies (start, end) given a notion of `now`, returning a
// time-class, the bucketed start/end timestamps, and the TTL/max-age that
// should be applied to the response.
//
// Alignment rules:
//   - StartActual is floored to `uniformBucket`.
//   - EndActual is ceiled to `uniformBucket` so the user-requested window is
//     fully covered (a query for `end=12:19` no longer drops 12:15-12:19).
//   - When the ceiled `EndActual` would land in the future relative to `now`,
//     it is clamped to `floor(now, uniformBucket)` so PromQL is never asked
//     to evaluate at a future timestamp.
func Bucket(start, end, now time.Time) Bucketing {
	switch {
	case end.After(now.Add(-time.Minute)):
		return bucketWith(start, end, now, ClassLive, 30*time.Second)
	case end.After(now.Add(-24 * time.Hour)):
		return bucketWith(start, end, now, ClassRecent, 5*time.Minute)
	case end.After(now.Add(-7 * 24 * time.Hour)):
		return bucketWith(start, end, now, ClassHistorical, time.Hour)
	default:
		return bucketWith(start, end, now, ClassFrozen, 24*time.Hour)
	}
}

func bucketWith(start, end, now time.Time, class TimeClass, ttl time.Duration) Bucketing {
	startActual := floor(start, uniformBucket)
	endActual := ceil(end, uniformBucket)
	if nowFloor := floor(now, uniformBucket); endActual.After(nowFloor) {
		endActual = nowFloor
	}
	if endActual.Before(startActual) {
		endActual = startActual
	}
	return Bucketing{
		Class:         class,
		BucketSeconds: int(uniformBucket / time.Second),
		StartActual:   startActual,
		EndActual:     endActual,
		TTL:           ttl,
		MaxAge:        int(ttl / time.Second),
	}
}

func floor(t time.Time, d time.Duration) time.Time {
	if d <= 0 {
		return t
	}
	return t.Truncate(d)
}

func ceil(t time.Time, d time.Duration) time.Time {
	if d <= 0 {
		return t
	}
	floored := t.Truncate(d)
	if floored.Equal(t) {
		return t
	}
	return floored.Add(d)
}

// Key returns the time-only cache key for a Bucketing.
func Key(b Bucketing) uint64 {
	type payload struct {
		StartBucket   int64 `json:"start_bucket"`
		EndBucket     int64 `json:"end_bucket"`
		BucketSeconds int   `json:"bucket_seconds"`
	}
	p := payload{
		StartBucket:   b.StartActual.Unix(),
		EndBucket:     b.EndActual.Unix(),
		BucketSeconds: b.BucketSeconds,
	}
	raw, _ := json.Marshal(p)
	return xxhash.Sum64(raw)
}
