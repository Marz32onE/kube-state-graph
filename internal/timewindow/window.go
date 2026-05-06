// Package timewindow aligns caller-supplied [start, end] ranges onto a fixed
// 60 s grid. The grid keeps PromQL `query_range` step-aligned to upstream's
// typical 60 s scrape cadence and gives callers a predictable
// (start_actual, end_actual) for charting.
//
// No caching is performed; alignment is a pure function of (start, end, now).
package timewindow

import "time"

// BucketSize is the fixed alignment grid.
const BucketSize = time.Minute

// Window is the aligned [StartActual, EndActual] range plus the bucket size in
// seconds, returned to callers in the response body.
type Window struct {
	StartActual   time.Time
	EndActual     time.Time
	BucketSeconds int
}

// Align floors `start` and ceils `end` to BucketSize, clamping `end` to
// `floor(now, BucketSize)` so PromQL is never asked to evaluate at a future
// timestamp.
func Align(start, end, now time.Time) Window {
	startActual := floor(start, BucketSize)
	endActual := ceil(end, BucketSize)
	if nowFloor := floor(now, BucketSize); endActual.After(nowFloor) {
		endActual = nowFloor
	}
	if endActual.Before(startActual) {
		endActual = startActual
	}
	return Window{
		StartActual:   startActual,
		EndActual:     endActual,
		BucketSeconds: int(BucketSize / time.Second),
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
