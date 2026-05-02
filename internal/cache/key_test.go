package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBucket_TimeClass(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		end   time.Time
		class TimeClass
	}{
		{now.Add(-30 * time.Second), ClassLive},
		{now.Add(-30 * time.Minute), ClassRecent},
		{now.Add(-3 * 24 * time.Hour), ClassHistorical},
		{now.Add(-30 * 24 * time.Hour), ClassFrozen},
	}
	for _, tc := range cases {
		b := Bucket(tc.end.Add(-5*time.Minute), tc.end, now)
		assert.Equal(t, tc.class, b.Class, "end=%s", tc.end)
	}
}

func TestBucket_StartEndFloored(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	end := now.Add(-3 * time.Second)
	start := end.Add(-1 * time.Minute)
	b := Bucket(start, end, now)
	assert.Equal(t, 15, b.BucketSeconds, "expected 15s bucket for live")
	assert.Equal(t, int64(0), b.StartActual.Unix()%15, "start_actual not floored to 15s")
	assert.Equal(t, int64(0), b.EndActual.Unix()%15, "end_actual not floored to 15s")
}

func TestKey_StableAcrossEquivalentInputs(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	a := Bucket(now.Add(-60*time.Second), now.Add(-1*time.Second), now)
	b := Bucket(now.Add(-59*time.Second), now.Add(-2*time.Second), now)
	assert.Equal(t, Key(a), Key(b), "expected same bucketed key for inputs in same bucket")
}

func TestKey_DifferentBucketsDifferentKeys(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	a := Bucket(now.Add(-1*time.Minute), now.Add(-1*time.Second), now)
	b := Bucket(now.Add(-1*time.Minute), now.Add(-30*time.Second), now)
	assert.NotEqual(t, Key(a), Key(b), "expected different keys for different buckets")
}
