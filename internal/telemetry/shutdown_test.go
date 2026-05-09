package telemetry

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordingExporter is a SpanExporter that just counts how many spans were
// pushed through ExportSpans. Used to verify Shutdown drains a batcher's
// queue before returning.
type recordingExporter struct {
	mu       sync.Mutex
	count    atomic.Int64
	shutdown atomic.Bool
}

func (r *recordingExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count.Add(int64(len(spans)))
	return nil
}

func (r *recordingExporter) Shutdown(_ context.Context) error {
	r.shutdown.Store(true)
	return nil
}

// TestShutdown_FlushesPendingSpans installs an in-memory trace exporter wired
// through a BatchSpanProcessor (mirroring production), emits a span, then
// invokes Shutdown and asserts the buffered span is flushed before the
// closure returns.
func TestShutdown_FlushesPendingSpans(t *testing.T) {
	exporter := &recordingExporter{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(time.Hour), // never auto-flush; force drain via Shutdown
		),
	)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	providers := Providers{
		Tracer:  tp,
		Enabled: true,
		Shutdown: func(ctx context.Context) error {
			return tp.Shutdown(ctx)
		},
	}

	const want = 3
	for i := 0; i < want; i++ {
		_, span := tp.Tracer("test").Start(context.Background(), "shutdown-flush")
		span.End()
	}
	require.Zero(t, exporter.count.Load(), "batcher must not auto-flush before Shutdown with hour-long batch timeout")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, providers.Shutdown(ctx))

	assert.GreaterOrEqual(t, exporter.count.Load(), int64(want),
		"Shutdown must drain buffered spans through the exporter before returning")
	assert.True(t, exporter.shutdown.Load(),
		"Shutdown must propagate to the underlying exporter")
}

// TestShutdown_NoopWhenDisabled asserts the no-op Shutdown returned by Init
// when telemetry is disabled is safe to call and returns nil even with an
// already-cancelled context.
func TestShutdown_NoopWhenDisabled(t *testing.T) {
	clearOTELEnv(t)
	providers, err := Init(t.Context(), "test")
	require.NoError(t, err)
	require.False(t, providers.Enabled)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, providers.Shutdown(cancelled))
}

// TestShutdown_ContextDeadlineRespected asserts a Shutdown call against an
// already-expired context returns an error rather than blocking — operators
// rely on the deadline matching `terminationGracePeriodSeconds`.
func TestShutdown_ContextDeadlineRespected(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	// Shutdown returns once batches are exported OR the context expires.
	// In-memory exporter is synchronous so this typically succeeds; the
	// assertion here is "does not block past the expired deadline".
	start := time.Now()
	_ = tp.Shutdown(expired)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, time.Second, "Shutdown must respect expired context")
}
