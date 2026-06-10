package promql

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
)

// tracer is obtained from the global provider; it is a no-op until an
// application installs an OpenTelemetry SDK. The instrumentation scope name is
// kept stable ("kube-state-graph") so span dimensions are unchanged.
var tracer = otel.Tracer("kube-state-graph")

// Client wraps the Prometheus HTTP API and emits self-metrics for every call.
type Client struct {
	api     v1.API
	metrics Metrics
}

// New constructs a Client targeting the supplied URL. metrics may be nil
// (no-op). The HTTP transport is wrapped with otelhttp so outbound PromQL
// requests propagate W3C traceparent headers and emit a client span per call.
func New(promURL string, metrics Metrics) (*Client, error) {
	base := &http.Transport{
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
	}
	c, err := promapi.NewClient(promapi.Config{
		Address:      promURL,
		RoundTripper: otelhttp.NewTransport(base),
	})
	if err != nil {
		return nil, fmt.Errorf("prometheus client: %w", err)
	}
	return &Client{api: v1.NewAPI(c), metrics: metrics}, nil
}

// Instant runs an instant PromQL query at ts, recording duration / failure
// metrics labelled with the supplied query name and emitting a `prometheus.query`
// span carrying the rendered statement.
func (c *Client) Instant(ctx context.Context, name, query string, ts time.Time) (model.Vector, error) {
	ctx, span := tracer.Start(ctx, "prometheus.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.DBSystemKey.String("prometheus"),
			attribute.String("db.statement", query),
			attribute.String("kube_state_graph.query_name", name),
		),
	)
	defer span.End()

	slog.DebugContext(ctx, "promql query",
		"name", name,
		"query", query,
		"ts", ts.UTC().Format(time.RFC3339),
	)

	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.ObserveQueryDuration(name, time.Since(start).Seconds())
		}
	}()
	val, warns, err := c.api.Query(ctx, query, ts)
	if len(warns) > 0 {
		// VictoriaMetrics signals truncated / partial responses (e.g. a
		// -search.* limit hit) via the warnings return; dropping them hides
		// silent data truncation. Logged regardless of err so a partial
		// response that still errors keeps both signals. Only the query name
		// and upstream warning text are logged — never credentials or the
		// upstream URL.
		slog.WarnContext(ctx, "upstream query returned warnings",
			"query_name", name,
			"warnings", warns,
		)
	}
	if err != nil {
		if c.metrics != nil {
			c.metrics.IncQueryFailure(name)
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		slog.ErrorContext(ctx, "promql query failed",
			"name", name,
			"query", query,
			"ts", ts.UTC().Format(time.RFC3339),
			"err", err,
		)
		return nil, fmt.Errorf("prom query %s: %w", name, err)
	}
	vec, ok := val.(model.Vector)
	if !ok {
		if c.metrics != nil {
			c.metrics.IncQueryFailure(name)
		}
		err := fmt.Errorf("unexpected result type %T", val)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("prom query %s: %w", name, err)
	}
	span.SetAttributes(attribute.Int("kube_state_graph.result_series_count", len(vec)))
	slog.DebugContext(ctx, "promql result",
		"name", name,
		"series", len(vec),
		"ts", ts.UTC().Format(time.RFC3339),
	)
	return vec, nil
}

// FormatDuration renders d as a PromQL duration literal (e.g., 90s, 5m, 3h).
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	switch {
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	default:
		// Fall back to seconds, truncated. A positive sub-second window would
		// truncate to 0 and render a zero-width range selector (`...[0s]`),
		// which is at best a no-op and at worst rejected by PromQL/MetricsQL.
		// Floor any positive duration to 1s so a valid (end > start) window
		// never produces a degenerate `[0s]` selector.
		secs := int64(d.Seconds())
		if secs < 1 {
			secs = 1
		}
		return strconv.FormatInt(secs, 10) + "s"
	}
}
