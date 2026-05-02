package cache

import (
	"testing"
	"time"
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
		if b.Class != tc.class {
			t.Errorf("end=%s: got class %s, want %s", tc.end, b.Class, tc.class)
		}
	}
}

func TestBucket_StartEndFloored(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	end := now.Add(-3 * time.Second)
	start := end.Add(-1 * time.Minute)
	b := Bucket(start, end, now)
	if b.BucketSeconds != 15 {
		t.Errorf("expected 15s bucket for live, got %d", b.BucketSeconds)
	}
	if b.StartActual.Unix()%15 != 0 {
		t.Errorf("start_actual not floored to 15s: %d", b.StartActual.Unix())
	}
	if b.EndActual.Unix()%15 != 0 {
		t.Errorf("end_actual not floored to 15s: %d", b.EndActual.Unix())
	}
}

func TestKey_StableAcrossEquivalentInputs(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	// Pick two end timestamps that floor to the same 15s boundary AND start
	// timestamps that floor to the same 15s boundary.
	a := Bucket(now.Add(-60*time.Second), now.Add(-1*time.Second), now)
	b := Bucket(now.Add(-59*time.Second), now.Add(-2*time.Second), now)
	if Key(a) != Key(b) {
		t.Errorf("expected same bucketed key for inputs in same bucket, got %d vs %d (a=%v b=%v)", Key(a), Key(b), a, b)
	}
}

func TestKey_DifferentBucketsDifferentKeys(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	a := Bucket(now.Add(-1*time.Minute), now.Add(-1*time.Second), now)
	b := Bucket(now.Add(-1*time.Minute), now.Add(-30*time.Second), now)
	if Key(a) == Key(b) {
		t.Errorf("expected different keys for different buckets")
	}
}
