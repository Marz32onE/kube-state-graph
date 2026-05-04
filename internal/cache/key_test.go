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
		assert.Equal(t, 60, b.BucketSeconds, "all classes share the 1-minute bucket")
	}
}

func TestBucket_TTLLadder(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		end time.Time
		ttl time.Duration
	}{
		{now.Add(-30 * time.Second), 30 * time.Second},
		{now.Add(-30 * time.Minute), 5 * time.Minute},
		{now.Add(-3 * 24 * time.Hour), time.Hour},
		{now.Add(-30 * 24 * time.Hour), 24 * time.Hour},
	}
	for _, tc := range cases {
		b := Bucket(tc.end.Add(-5*time.Minute), tc.end, now)
		assert.Equal(t, tc.ttl, b.TTL, "end=%s", tc.end)
		assert.Equal(t, int(tc.ttl/time.Second), b.MaxAge, "MaxAge mirrors TTL")
	}
}

func TestBucket_StartFlooredEndCeiled(t *testing.T) {
	now := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	// Historical example: start=12:04, end=12:19 two days ago.
	end := time.Date(2026, 5, 2, 12, 19, 0, 0, time.UTC)
	start := time.Date(2026, 5, 2, 12, 4, 0, 0, time.UTC)
	b := Bucket(start, end, now)

	assert.Equal(t, ClassHistorical, b.Class)
	assert.Equal(t, 60, b.BucketSeconds)
	assert.Equal(t,
		time.Date(2026, 5, 2, 12, 4, 0, 0, time.UTC),
		b.StartActual,
		"start already on minute boundary stays put",
	)
	assert.Equal(t,
		time.Date(2026, 5, 2, 12, 19, 0, 0, time.UTC),
		b.EndActual,
		"end already on minute boundary stays put",
	)

	// Sub-minute end now ceils up so the user-requested instant is included.
	endSub := time.Date(2026, 5, 2, 12, 19, 30, 0, time.UTC)
	startSub := time.Date(2026, 5, 2, 12, 4, 17, 0, time.UTC)
	bSub := Bucket(startSub, endSub, now)
	assert.Equal(t,
		time.Date(2026, 5, 2, 12, 4, 0, 0, time.UTC),
		bSub.StartActual,
		"sub-minute start is floored down",
	)
	assert.Equal(t,
		time.Date(2026, 5, 2, 12, 20, 0, 0, time.UTC),
		bSub.EndActual,
		"sub-minute end is ceiled up so caller's instant is in window",
	)
}

func TestBucket_LiveEndClampedToNow(t *testing.T) {
	now := time.Date(2026, 5, 4, 14, 32, 30, 0, time.UTC)
	// end = 14:32:15 is in the live class. Naive ceil → 14:33:00 which is
	// in the future. Must clamp to floor(now) = 14:32:00.
	end := time.Date(2026, 5, 4, 14, 32, 15, 0, time.UTC)
	start := end.Add(-5 * time.Minute)
	b := Bucket(start, end, now)

	assert.Equal(t, ClassLive, b.Class)
	assert.False(t, b.EndActual.After(now), "EndActual must not exceed now")
	assert.Equal(t,
		time.Date(2026, 5, 4, 14, 32, 0, 0, time.UTC),
		b.EndActual,
		"end clamped to floor(now, 1m)",
	)
}

func TestBucket_EndAlreadyOnBoundaryUnchanged(t *testing.T) {
	now := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 12, 15, 0, 0, time.UTC) // already :00
	start := end.Add(-15 * time.Minute)
	b := Bucket(start, end, now)
	assert.Equal(t, end, b.EndActual, "end on boundary must not be pushed forward")
}

func TestKey_StableAcrossEquivalentInputs(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	a := Bucket(now.Add(-10*time.Minute-15*time.Second), now.Add(-1*time.Second), now)
	b := Bucket(now.Add(-10*time.Minute-44*time.Second), now.Add(-2*time.Second), now)
	assert.Equal(t, Key(a), Key(b), "expected same bucketed key for inputs in same minute bucket")
}

func TestKey_DifferentBucketsDifferentKeys(t *testing.T) {
	now := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	a := Bucket(time.Date(2026, 5, 2, 12, 4, 0, 0, time.UTC), time.Date(2026, 5, 2, 12, 19, 0, 0, time.UTC), now)
	b := Bucket(time.Date(2026, 5, 2, 12, 4, 0, 0, time.UTC), time.Date(2026, 5, 2, 12, 20, 0, 0, time.UTC), now)
	assert.NotEqual(t, Key(a), Key(b), "different end-bucket must yield different key")
}
