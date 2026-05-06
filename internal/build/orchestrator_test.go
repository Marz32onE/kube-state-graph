package build

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	onBuild chan struct{}
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

func TestResolve_SemaphoreReject(t *testing.T) {
	t.Parallel()
	releaseA := make(chan struct{})
	fbA := &fakeBuilder{release: releaseA, onBuild: make(chan struct{}, 1)}
	m := observability.NewMetrics()
	o := newOrchestrator(fbA, 1, time.Second, m)

	go func() { _, _ = o.Resolve(context.Background(), time.Minute, time.Now().UTC()) }()
	<-fbA.onBuild

	_, err := o.Resolve(context.Background(), time.Minute, time.Now().UTC())
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
	m := observability.NewMetrics()
	o := newOrchestrator(fb, 1, 10*time.Millisecond, m)

	_, err := o.Resolve(context.Background(), time.Minute, time.Now().UTC())
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
	m := observability.NewMetrics()
	o := newOrchestrator(fb, 1, time.Second, m)

	_, err := o.Resolve(context.Background(), time.Minute, time.Now().UTC())
	if err == nil {
		t.Fatalf("expected upstream error")
	}
	if r := AsReason(err); r != ReasonUpstream {
		t.Fatalf("reason = %v, want %v", r, ReasonUpstream)
	}
}

func TestResolve_HappyPath(t *testing.T) {
	t.Parallel()
	want := graph.NewGraph(nil, nil, time.Unix(123, 0).UTC())
	fb := &fakeBuilder{result: want}
	m := observability.NewMetrics()
	o := newOrchestrator(fb, 2, time.Second, m)

	got, err := o.Resolve(context.Background(), time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("returned graph mismatch")
	}
	if c := fb.calls.Load(); c != 1 {
		t.Fatalf("Build called %d times, want 1", c)
	}
}
