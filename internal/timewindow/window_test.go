package timewindow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAlign_FloorsStartCeilsEnd(t *testing.T) {
	now := time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)
	start := time.Date(2026, 5, 1, 12, 0, 30, 0, time.UTC)
	end := time.Date(2026, 5, 1, 12, 19, 30, 0, time.UTC)

	w := Align(start, end, now)
	assert.Equal(t, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), w.StartActual)
	assert.Equal(t, time.Date(2026, 5, 1, 12, 20, 0, 0, time.UTC), w.EndActual)
	assert.Equal(t, 60, w.BucketSeconds)
}

func TestAlign_ClampsEndToNow(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 19, 30, 0, time.UTC)
	start := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	// end > now — would otherwise ceil to 12:20 (future).
	end := time.Date(2026, 5, 1, 12, 19, 30, 0, time.UTC)

	w := Align(start, end, now)
	assert.Equal(t, time.Date(2026, 5, 1, 12, 19, 0, 0, time.UTC), w.EndActual)
}

func TestAlign_AlreadyAligned_NoChange(t *testing.T) {
	now := time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)
	start := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)

	w := Align(start, end, now)
	assert.Equal(t, start, w.StartActual)
	assert.Equal(t, end, w.EndActual)
}
