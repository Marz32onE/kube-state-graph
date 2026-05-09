package promql

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"github.com/marz32one/kube-state-graph/internal/observability"
)

// Client wraps the Prometheus HTTP API and emits self-metrics for every call.
type Client struct {
	api     v1.API
	metrics *observability.Metrics
}

// New constructs a Client targeting the supplied URL.
func New(promURL string, metrics *observability.Metrics) (*Client, error) {
	c, err := promapi.NewClient(promapi.Config{
		Address: promURL,
		RoundTripper: &http.Transport{
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     30 * time.Second,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("prometheus client: %w", err)
	}
	return &Client{api: v1.NewAPI(c), metrics: metrics}, nil
}

// Instant runs an instant PromQL query at ts, recording duration / failure
// metrics labelled with the supplied query name.
func (c *Client) Instant(ctx context.Context, name, query string, ts time.Time) (model.Vector, error) {
	start := time.Now()
	defer func() {
		c.metrics.UpstreamQueryDur.WithLabelValues(name).Observe(time.Since(start).Seconds())
	}()
	val, _, err := c.api.Query(ctx, query, ts)
	if err != nil {
		c.metrics.UpstreamQueryFail.WithLabelValues(name).Inc()
		return nil, fmt.Errorf("prom query %s: %w", name, err)
	}
	vec, ok := val.(model.Vector)
	if !ok {
		c.metrics.UpstreamQueryFail.WithLabelValues(name).Inc()
		return nil, fmt.Errorf("prom query %s: unexpected result type %T", name, val)
	}
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
		// Fall back to seconds, rounded.
		return strconv.FormatInt(int64(d.Seconds()), 10) + "s"
	}
}

