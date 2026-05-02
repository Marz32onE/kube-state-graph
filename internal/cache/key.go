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

// Bucket classifies (start, end) given a notion of `now`, returning a
// time-class, the bucketed start/end timestamps, and the TTL/max-age that
// should be applied to the response.
func Bucket(start, end, now time.Time) Bucketing {
	switch {
	case end.After(now.Add(-time.Minute)):
		return bucketWith(start, end, ClassLive, 15*time.Second, 30*time.Second)
	case end.After(now.Add(-24 * time.Hour)):
		return bucketWith(start, end, ClassRecent, time.Minute, 5*time.Minute)
	case end.After(now.Add(-7 * 24 * time.Hour)):
		return bucketWith(start, end, ClassHistorical, 5*time.Minute, time.Hour)
	default:
		return bucketWith(start, end, ClassFrozen, 5*time.Minute, 24*time.Hour)
	}
}

func bucketWith(start, end time.Time, class TimeClass, bucket, ttl time.Duration) Bucketing {
	bs := int(bucket / time.Second)
	return Bucketing{
		Class:         class,
		BucketSeconds: bs,
		StartActual:   floor(start, bucket),
		EndActual:     floor(end, bucket),
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
