package build

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/observability"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

// TestProbeClusterSizeUsesRequestEnd verifies the cluster-too-large probe
// queries at the requested window's end timestamp, not at time.Now().
// Regression: historical graph requests must be evaluated against historical
// cluster size — using time.Now() rejects valid historical windows whenever
// the live cluster has grown past --max-pods.
func TestProbeClusterSizeUsesRequestEnd(t *testing.T) {
	type recordedReq struct {
		query string
		time  string
	}
	var (
		mu       sync.Mutex
		captured []recordedReq
	)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		captured = append(captured, recordedReq{
			query: r.Form.Get("query"),
			time:  r.Form.Get("time"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// Return an empty vector so probe passes (count=0 < max-pods).
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(mock.Close)

	cfg := config.Defaults()
	cfg.PromURL = mock.URL
	require.NoError(t, cfg.Validate())

	metrics := observability.NewMetrics()
	prom, err := promql.New(cfg.PromURL, metrics)
	require.NoError(t, err)
	b := New(prom, cfg, metrics)

	// Historical end, far from time.Now().
	end := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	// Build will fail with outside_retention (empty topology + healthy upstream)
	// but the probe runs first; that's all we care about.
	_, _ = b.Build(context.Background(), 5*time.Minute, end)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, captured, "expected at least one upstream call (the probe)")

	// First call MUST be the cluster-size probe at the requested end.
	probe := captured[0]
	assert.Contains(t, probe.query, "count(", "first call should be cluster-size probe")
	require.NotEmpty(t, probe.time, "probe must send a time= parameter")

	got, err := strconv.ParseFloat(probe.time, 64)
	require.NoError(t, err)
	wantSec := float64(end.Unix())
	assert.InDelta(t, wantSec, got, 1.0,
		"probe queried at %s, expected %s (unix=%v vs %v)",
		probe.time, end.Format(time.RFC3339), got, wantSec)
}
