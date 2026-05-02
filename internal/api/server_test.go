package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/internal/build"
	"github.com/marz32one/kube-state-graph/internal/cache"
	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/observability"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

// promMock returns a httptest.Server speaking the Prometheus HTTP API,
// answering with the supplied JSON per query string substring match.
func promMock(t *testing.T, fixtures map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query := r.Form.Get("query")
		for needle, body := range fixtures {
			if strings.Contains(query, needle) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestServer(t *testing.T, mock *httptest.Server, override func(*config.Config)) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.PromURL = mock.URL
	if override != nil {
		override(&cfg)
	}
	require.NoError(t, cfg.Validate())
	logger := observability.NewLogger("error")
	metrics := observability.NewMetrics()
	prom, err := promql.New(cfg.PromURL, metrics)
	require.NoError(t, err)
	c, err := cache.New(cfg.CacheMaxCostBytes, metrics)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	builder := build.New(prom, cfg, metrics)
	return New(cfg, builder, c, prom, metrics, logger)
}

// TestDebugLastQueries_NotImplemented guards the contract that the route
// returns 501 (not 200 with an empty body). Clients must distinguish "feature
// not built" from "no recent queries".
func TestDebugLastQueries_NotImplemented(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, func(cfg *config.Config) { cfg.EnableDebug = true })
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/debug/last-queries")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	errField, _ := body["error"].(map[string]any)
	assert.Equal(t, "not_implemented", errField["reason"])
}

func TestGraphEndpoint_MissingStart(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/graph?end=2026-05-01T12:00:00Z")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	errField, _ := body["error"].(map[string]any)
	assert.Equal(t, "missing_start", errField["reason"])
}

func TestGraphEndpoint_InvalidRange(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	now := time.Now().UTC().Add(-time.Hour)
	q := url.Values{}
	q.Set("start", now.Format(time.RFC3339))
	q.Set("end", now.Add(-5*time.Minute).Format(time.RFC3339))
	resp, err := http.Get(srv.URL + "/v1/graph?" + q.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestEdgeTypesEndpoint_StaticCatalogue(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/edge-types")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Cache-Control"), "max-age=3600")
	assert.NotEmpty(t, resp.Header.Get("ETag"))

	var body struct {
		APIVersion string `json:"apiVersion"`
		EdgeTypes  []struct {
			Type string `json:"type"`
		} `json:"edge_types"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "v1", body.APIVersion)
	want := map[string]bool{"pod-runs-on-node": false, "pod-mounts-pvc-on-node": false, "pod-calls-pod": false}
	for _, e := range body.EdgeTypes {
		if _, ok := want[e.Type]; ok {
			want[e.Type] = true
		}
	}
	for k, v := range want {
		assert.Truef(t, v, "missing edge type %q", k)
	}
}

func TestEdgeTypesEndpoint_IfNoneMatch304(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/edge-types")
	require.NoError(t, err)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/edge-types", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusNotModified, resp2.StatusCode)
}

func TestLivez(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/livez")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMetricsEndpoint(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAdminCacheFlush(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/admin/cache", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestDebugEndpoint_DisabledByDefault(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/debug/last-queries")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
