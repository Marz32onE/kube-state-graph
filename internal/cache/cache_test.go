package cache

import (
	"testing"
	"time"

	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/observability"
)

func newTestCache(t *testing.T) Cache {
	t.Helper()
	c, err := New(64<<20, observability.NewMetrics())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func sampleGraph(builtAt time.Time) *graph.Graph {
	return graph.NewGraph(nil, nil, builtAt)
}

func TestNew_ZeroBudgetRejected(t *testing.T) {
	t.Parallel()
	if _, err := New(0, observability.NewMetrics()); err == nil {
		t.Fatalf("expected error for zero budget")
	}
	if _, err := New(-1, observability.NewMetrics()); err == nil {
		t.Fatalf("expected error for negative budget")
	}
}

func TestCache_SetGetRoundtrip(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	g := sampleGraph(time.Unix(1, 0).UTC())

	if !c.Set(42, g, 64, time.Minute) {
		t.Fatalf("Set returned false")
	}
	got, ok := c.Get(42)
	if !ok {
		t.Fatalf("Get miss after Set+Wait")
	}
	if got != g {
		t.Errorf("Get returned different pointer")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	g := sampleGraph(time.Unix(2, 0).UTC())

	if !c.Set(7, g, 32, 100*time.Millisecond) {
		t.Fatalf("Set returned false")
	}
	if _, ok := c.Get(7); !ok {
		t.Fatalf("expected immediate hit before TTL expiry")
	}
	time.Sleep(250 * time.Millisecond)
	if _, ok := c.Get(7); ok {
		t.Fatalf("expected miss after TTL expiry")
	}
}

func TestCache_Delete(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	g := sampleGraph(time.Unix(3, 0).UTC())
	c.Set(11, g, 16, time.Minute)
	c.Delete(11)
	// Ristretto's Del is async; wait briefly then assert miss.
	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get(11); ok {
		t.Fatalf("expected miss after Delete")
	}
}

func TestCache_Clear(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	c.Set(1, sampleGraph(time.Unix(1, 0).UTC()), 16, time.Minute)
	c.Set(2, sampleGraph(time.Unix(2, 0).UTC()), 16, time.Minute)
	c.Clear()
	if _, ok := c.Get(1); ok {
		t.Errorf("key 1 still present after Clear")
	}
	if _, ok := c.Get(2); ok {
		t.Errorf("key 2 still present after Clear")
	}
}
