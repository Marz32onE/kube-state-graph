package build

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marz32one/kube-state-graph/internal/cache"
	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/observability"
)

// fakeBuilder records invocations and lets the test control timing/result.
type fakeBuilder struct {
	mu      sync.Mutex
	calls   atomic.Int32
	delay   time.Duration
	failErr error
	result  *graph.Graph
	// onBuild fires once Build is entered; tests use it to coordinate.
	onBuild chan struct{}
	// release blocks Build until closed (for slow-build tests).
	release chan struct{}
}

func (f *fakeBuilder) Build(ctx context.Context, _ time.Duration, _ time.Time) (*graph.Graph, error) {
	f.calls.Add(1)
	if f.onBuild != nil {
		select {
		case f.onBuild <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.failErr != nil {
		return nil, f.failErr
	}
	f.mu.Lock()
	g := f.result
	f.mu.Unlock()
	if g == nil {
		g = graph.NewGraph(nil, nil, time.Unix(0, 0).UTC())
	}
	return g, nil
}

// mapCache is a trivial in-memory cache implementation for tests.
type mapCache struct {
	mu      sync.Mutex
	data    map[uint64]*graph.Graph
	gets    atomic.Int32
	sets    atomic.Int32
}

func newMapCache() *mapCache { return &mapCache{data: map[uint64]*graph.Graph{}} }

func (m *mapCache) Get(key uint64) (*graph.Graph, bool) {
	m.gets.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.data[key]
	return g, ok
}
func (m *mapCache) Set(key uint64, g *graph.Graph, _ int64, _ time.Duration) bool {
	m.sets.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = g
	return true
}
func (m *mapCache) Delete(key uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
}
func (m *mapCache) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = map[uint64]*graph.Graph{}
}
func (m *mapCache) Close() {}

var _ cache.Cache = (*mapCache)(nil)

func newTestBucket() cache.Bucketing {
	end := time.Now().UTC()
	return cache.Bucketing{
		Class:         cache.ClassRecent,
		BucketSeconds: 60,
		StartActual:   end.Add(-15 * time.Minute),
		EndActual:     end,
		TTL:           5 * time.Minute,
		MaxAge:        300,
	}
}

func TestResolve_CacheHit(t *testing.T) {
	t.Parallel()
	fb := &fakeBuilder{}
	c := newMapCache()
	m := observability.NewMetrics()
	o := newOrchestrator(fb, c, 2, time.Second, m)

	prebuilt := graph.NewGraph(nil, nil, time.Unix(0, 0).UTC())
	c.Set(42, prebuilt, 1, time.Minute)

	res, err := o.Resolve(context.Background(), 42, newTestBucket())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.CacheStatus != "hit" {
		t.Fatalf("CacheStatus = %q, want hit", res.CacheStatus)
	}
	if res.Graph != prebuilt {
		t.Fatalf("returned graph mismatch")
	}
	if got := fb.calls.Load(); got != 0 {
		t.Fatalf("Build called %d times on cache hit, want 0", got)
	}
}

func TestResolve_Coalescing(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	fb := &fakeBuilder{release: release, onBuild: make(chan struct{}, 1)}
	c := newMapCache()
	m := observability.NewMetrics()
	o := newOrchestrator(fb, c, 8, time.Second, m)
	bucket := newTestBucket()

	const N = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]Result, N)
	errs := make([]error, N)
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = o.Resolve(context.Background(), 99, bucket)
		}(i)
	}
	close(start)
	// Wait until builder enters singleflight, then give stragglers time to
	// land in sf.Do before allowing the leader to finish.
	<-fb.onBuild
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("call %d: %v", i, e)
		}
	}
	if got := fb.calls.Load(); got != 1 {
		t.Fatalf("Build called %d times under singleflight, want 1", got)
	}
	misses, coalesced := 0, 0
	for _, r := range results {
		switch r.CacheStatus {
		case "miss":
			misses++
		case "coalesced":
			coalesced++
		default:
			t.Fatalf("unexpected status %q", r.CacheStatus)
		}
	}
	if misses != 1 {
		t.Fatalf("misses=%d, want 1", misses)
	}
	if coalesced != N-1 {
		t.Fatalf("coalesced=%d, want %d", coalesced, N-1)
	}
}

func TestResolve_SemaphoreReject(t *testing.T) {
	t.Parallel()
	releaseA := make(chan struct{})
	fbA := &fakeBuilder{release: releaseA, onBuild: make(chan struct{}, 1)}
	c := newMapCache()
	m := observability.NewMetrics()
	o := newOrchestrator(fbA, c, 1, time.Second, m)
	bucket := newTestBucket()

	// Hold one slot via key=1.
	go func() { _, _ = o.Resolve(context.Background(), 1, bucket) }()
	<-fbA.onBuild

	// Different key bypasses singleflight, must hit the semaphore and reject.
	_, err := o.Resolve(context.Background(), 2, bucket)
	if err == nil {
		t.Fatalf("expected ReasonCapacity error, got nil")
	}
	if r := AsReason(err); r != ReasonCapacity {
		t.Fatalf("reason = %v, want %v", r, ReasonCapacity)
	}
	close(releaseA)
}

func TestResolve_Timeout(t *testing.T) {
	t.Parallel()
	fb := &fakeBuilder{delay: 200 * time.Millisecond}
	c := newMapCache()
	m := observability.NewMetrics()
	o := newOrchestrator(fb, c, 1, 10*time.Millisecond, m)

	_, err := o.Resolve(context.Background(), 7, newTestBucket())
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if r := AsReason(err); r != ReasonTimeout {
		t.Fatalf("reason = %v, want %v", r, ReasonTimeout)
	}
}

func TestResolve_BuilderError(t *testing.T) {
	t.Parallel()
	upstreamErr := NewError(ReasonUpstream, "boom", errors.New("net"))
	fb := &fakeBuilder{failErr: upstreamErr}
	c := newMapCache()
	m := observability.NewMetrics()
	o := newOrchestrator(fb, c, 1, time.Second, m)

	_, err := o.Resolve(context.Background(), 11, newTestBucket())
	if err == nil {
		t.Fatalf("expected upstream error")
	}
	if r := AsReason(err); r != ReasonUpstream {
		t.Fatalf("reason = %v, want %v", r, ReasonUpstream)
	}
}

func TestKeyToString_Format(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   uint64
		want string
	}{
		{0x0, "0000000000000000"},
		{0x1, "0000000000000001"},
		{0xdeadbeefcafef00d, "deadbeefcafef00d"},
		{^uint64(0), "ffffffffffffffff"},
	}
	for _, tc := range cases {
		if got := keyToString(tc.in); got != tc.want {
			t.Errorf("keyToString(%x) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEstimateCost_LinearInCardinality(t *testing.T) {
	t.Parallel()
	empty := graph.NewGraph(nil, nil, time.Unix(0, 0).UTC())
	if got := estimateCost(empty); got != 0 {
		t.Errorf("empty cost = %d, want 0", got)
	}
}
