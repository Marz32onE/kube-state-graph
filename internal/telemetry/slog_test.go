package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestSpanCorrelatedHandler_RecordsTraceID asserts the local handler stamps
// trace_id and span_id keys onto records emitted from a context carrying a
// recording span.
func TestSpanCorrelatedHandler_RecordsTraceID(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)

	var buf bytes.Buffer
	local := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := &spanCorrelatedHandler{inner: local}
	logger := slog.New(handler)

	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "op")
	logger.InfoContext(ctx, "with-span")
	wantTraceID := span.SpanContext().TraceID().String()
	wantSpanID := span.SpanContext().SpanID().String()
	span.End()

	var line map[string]any
	require.NoError(t, json.NewDecoder(&buf).Decode(&line))
	require.Equal(t, wantTraceID, line["trace_id"])
	require.Equal(t, wantSpanID, line["span_id"])
}

// TestSpanCorrelatedHandler_NoSpanContext asserts the local handler does not
// emit empty trace_id / span_id when the context has no active span.
func TestSpanCorrelatedHandler_NoSpanContext(t *testing.T) {
	var buf bytes.Buffer
	local := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(&spanCorrelatedHandler{inner: local})

	logger.InfoContext(context.Background(), "no-span")

	var line map[string]any
	require.NoError(t, json.NewDecoder(&buf).Decode(&line))
	require.NotContains(t, line, "trace_id")
	require.NotContains(t, line, "span_id")
}
