package build

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	"github.com/marz32one/kube-state-graph/internal/cache"
	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/observability"
)

// Orchestrator coordinates singleflight, cache, semaphore, and timeout for
// every incoming graph request.
type Orchestrator struct {
	builder *Builder
	cache   cache.Cache
	sf      singleflight.Group
	sem     *semaphore.Weighted
	timeout time.Duration
	inflight atomic.Int64
	metrics *observability.Metrics
}

// NewOrchestrator wires up the cache, singleflight, and semaphore.
func NewOrchestrator(b *Builder, c cache.Cache, concurrency int, timeout time.Duration, m *observability.Metrics) *Orchestrator {
	return &Orchestrator{
		builder: b,
		cache:   c,
		sem:     semaphore.NewWeighted(int64(concurrency)),
		timeout: timeout,
		metrics: m,
	}
}

// Result captures one resolution of a build request.
type Result struct {
	Graph       *graph.Graph
	CacheStatus string // hit | miss | coalesced
}

// Resolve returns the Graph for the supplied bucket. On cache miss it runs
// the build under singleflight, respecting the concurrency cap and timeout.
func (o *Orchestrator) Resolve(ctx context.Context, key uint64, b cache.Bucketing) (Result, error) {
	if g, ok := o.cache.Get(key); ok {
		o.metrics.BuildDuration.WithLabelValues("hit").Observe(0)
		return Result{Graph: g, CacheStatus: "hit"}, nil
	}

	// Track whether THIS goroutine ran the build (vs being a coalesced waiter).
	weRanCh := make(chan struct{}, 1)

	v, err, shared := o.sf.Do(keyToString(key), func() (interface{}, error) {
		weRanCh <- struct{}{}
		if !o.sem.TryAcquire(1) {
			o.metrics.BuildRejected.WithLabelValues("capacity").Inc()
			return nil, NewError(ReasonCapacity, "build concurrency exhausted", nil)
		}
		defer o.sem.Release(1)
		o.inflight.Add(1)
		o.metrics.BuildConcurrency.Set(float64(o.inflight.Load()))
		defer func() {
			o.inflight.Add(-1)
			o.metrics.BuildConcurrency.Set(float64(o.inflight.Load()))
		}()

		buildCtx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()

		window := b.EndActual.Sub(b.StartActual)
		start := time.Now()
		g, err := o.builder.Build(buildCtx, window, b.EndActual)
		dur := time.Since(start)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				o.metrics.BuildRejected.WithLabelValues("timeout").Inc()
				return nil, NewError(ReasonTimeout, "build timeout", err)
			}
			return nil, err
		}
		o.metrics.BuildDuration.WithLabelValues("miss").Observe(dur.Seconds())
		o.cache.Set(key, g, estimateCost(g), b.TTL)
		return g, nil
	})

	weRan := false
	select {
	case <-weRanCh:
		weRan = true
	default:
	}

	if shared && !weRan {
		o.metrics.SingleflightDedup.Inc()
	}
	if err != nil {
		return Result{}, err
	}
	g := v.(*graph.Graph)
	status := "miss"
	if shared && !weRan {
		status = "coalesced"
	}
	return Result{Graph: g, CacheStatus: status}, nil
}

func keyToString(k uint64) string {
	const hex = "0123456789abcdef"
	buf := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		buf[i] = hex[k&0xf]
		k >>= 4
	}
	return string(buf)
}

// estimateCost returns an approximate in-memory size of g (for Ristretto's
// cost-based budget).
func estimateCost(g *graph.Graph) int64 {
	const perNode = 256 // rough average
	const perEdge = 192
	return int64(len(g.NodesByID))*perNode + int64(len(g.Edges))*perEdge
}
