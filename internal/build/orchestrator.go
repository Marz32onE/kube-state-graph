package build

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/observability"
)

// graphBuilder is the small interface the Orchestrator depends on. *Builder
// satisfies it; tests can substitute a fake.
type graphBuilder interface {
	Build(ctx context.Context, window time.Duration, end time.Time) (*graph.Graph, error)
}

// Orchestrator gates incoming graph requests with a concurrency cap and a
// per-build timeout. Each Resolve call runs the builder directly; there is no
// in-process result cache.
type Orchestrator struct {
	builder  graphBuilder
	sem      *semaphore.Weighted
	timeout  time.Duration
	inflight atomic.Int64
	metrics  *observability.Metrics
}

// NewOrchestrator wires the semaphore + timeout for a *Builder.
func NewOrchestrator(b *Builder, concurrency int, timeout time.Duration, m *observability.Metrics) *Orchestrator {
	return newOrchestrator(b, concurrency, timeout, m)
}

func newOrchestrator(b graphBuilder, concurrency int, timeout time.Duration, m *observability.Metrics) *Orchestrator {
	return &Orchestrator{
		builder: b,
		sem:     semaphore.NewWeighted(int64(concurrency)),
		timeout: timeout,
		metrics: m,
	}
}

// Resolve runs the builder for [end - window, end], honouring the concurrency
// cap and per-build timeout.
func (o *Orchestrator) Resolve(ctx context.Context, window time.Duration, end time.Time) (*graph.Graph, error) {
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

	start := time.Now()
	g, err := o.builder.Build(buildCtx, window, end)
	dur := time.Since(start)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			o.metrics.BuildRejected.WithLabelValues("timeout").Inc()
			return nil, NewError(ReasonTimeout, "build timeout", err)
		}
		return nil, err
	}
	o.metrics.BuildDuration.Observe(dur.Seconds())
	return g, nil
}
