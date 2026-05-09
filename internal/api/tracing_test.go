package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/marz32one/kube-state-graph/internal/auth"
	"github.com/marz32one/kube-state-graph/internal/build"
	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/observability"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

// installInMemoryTracer registers an in-memory exporter so tests can inspect
// emitted spans without contacting an OTLP collector. Also installs the W3C
// TraceContext propagator so otelgin extracts inbound traceparent headers.
func installInMemoryTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
		otel.SetTextMapPropagator(prevProp)
	})
	return exporter
}

func TestTracing_LivezProbeEmitsNoSpan(t *testing.T) {
	exporter := installInMemoryTracer(t)
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/livez")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, exporter.GetSpans(), "/livez must not generate spans")
}

func TestTracing_MetricsScrapeEmitsNoSpan(t *testing.T) {
	exporter := installInMemoryTracer(t)
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/metrics")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, exporter.GetSpans(), "/metrics must not generate spans")
}

func TestTracing_EdgeTypesEmitsServerSpan(t *testing.T) {
	exporter := installInMemoryTracer(t)
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/edge-types")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	spans := exporter.GetSpans()
	require.NotEmpty(t, spans, "/v1/edge-types must emit at least one server span")

	var found bool
	for _, span := range spans {
		if span.Name == "GET /v1/edge-types" {
			found = true
			// ETag attribute must be set on success.
			var hasETag bool
			for _, kv := range span.Attributes {
				if string(kv.Key) == "kube_state_graph.etag" && kv.Value.AsString() != "" {
					hasETag = true
					break
				}
			}
			assert.True(t, hasETag, "server span must carry kube_state_graph.etag")
		}
	}
	assert.True(t, found, "expected server span named GET /v1/edge-types")
}

// TestTracing_InboundTraceparentBecomesParent asserts otelgin extracts the
// W3C traceparent header so the server span chains under the caller's trace.
func TestTracing_InboundTraceparentBecomesParent(t *testing.T) {
	exporter := installInMemoryTracer(t)
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	const wantTraceID = "0af7651916cd43dd8448eb211c80319c"
	const wantParent = "b7ad6b7169203331"
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/edge-types", nil)
	require.NoError(t, err)
	req.Header.Set("traceparent", "00-"+wantTraceID+"-"+wantParent+"-01")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	spans := exporter.GetSpans()
	require.NotEmpty(t, spans)
	var serverSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "GET /v1/edge-types" {
			serverSpan = &spans[i]
			break
		}
	}
	require.NotNil(t, serverSpan, "server span missing")
	assert.Equal(t, wantTraceID, serverSpan.SpanContext.TraceID().String())
	assert.Equal(t, wantParent, serverSpan.Parent.SpanID().String())
}

// TestTracing_FailedGraphRecordsErrorSpan asserts a failed /v1/graph build
// stamps Error status on the server span via mapBuildError.
func TestTracing_FailedGraphRecordsErrorSpan(t *testing.T) {
	exporter := installInMemoryTracer(t)
	// Mock returns a 500 so the prom client surfaces an upstream error.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(mock.Close)

	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/graph?start=2026-05-01T12:00:00Z&end=2026-05-01T12:05:00Z")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)

	spans := exporter.GetSpans()
	var serverSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "GET /v1/graph" {
			serverSpan = &spans[i]
			break
		}
	}
	require.NotNil(t, serverSpan, "server span missing")
	assert.Equal(t, codes.Error, serverSpan.Status.Code,
		"server span must be marked Error when build returns 502")
	// Description from mapBuildError ("upstream") may be overwritten by
	// otelgin's post-handler status hook. The span events list still carries
	// the original error via span.RecordError — assert that's present.
	var sawErrorEvent bool
	for _, ev := range serverSpan.Events {
		if ev.Name == "exception" {
			sawErrorEvent = true
			break
		}
	}
	assert.True(t, sawErrorEvent, "server span must carry an exception event recording the build error")
}

// TestAuth_NoAPIKeyInLogs asserts authentication failure does not leak the
// presented X-API-Key value into log output.
func TestAuth_NoAPIKeyInLogs(t *testing.T) {
	const sentinelKey = "SENTINEL-NEVER-LOG-ME-1234"
	mock := promMock(t, nil)

	cfg := config.Defaults()
	cfg.PromURL = mock.URL
	require.NoError(t, cfg.Validate())

	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	metrics := observability.NewMetrics()
	prom, err := promql.New(cfg.PromURL, metrics)
	require.NoError(t, err)
	ks := auth.NewKeySet()
	ks.LoadCSV("real-key-1,real-key-2")
	builder := build.New(prom, cfg, metrics)
	server := New(cfg, builder, prom, metrics, logger, ks)

	srv := httptest.NewServer(server.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/edge-types", nil)
	require.NoError(t, err)
	req.Header.Set(APIKeyHeader, sentinelKey)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	out := buf.String()
	assert.NotContains(t, out, sentinelKey, "log output must never contain the presented API key value")
	// Sanity: a log line did get emitted (so the assertion above is meaningful).
	assert.Contains(t, out, "\"http\"", "expected an http log line: %s", out)
}

// TestTracing_ETagStableAcrossTracingState asserts enabling tracing does not
// change the response ETag — resource attributes and span IDs must NOT leak
// into the JSON body.
func TestTracing_ETagStableAcrossTracingState(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	url := srv.URL + "/v1/edge-types"

	// First fetch with no tracer overrides (default is whatever the test
	// process has installed — typically the noop tracer).
	r1, err := http.Get(url)
	require.NoError(t, err)
	r1.Body.Close()
	etag1 := r1.Header.Get("ETag")

	// Install a recording tracer and refetch.
	prevTP := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(tracetest.NewInMemoryExporter()))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
	})

	r2, err := http.Get(url)
	require.NoError(t, err)
	r2.Body.Close()
	etag2 := r2.Header.Get("ETag")

	require.NotEmpty(t, etag1)
	assert.Equal(t, etag1, etag2, "ETag must not change when tracing is enabled")
}
