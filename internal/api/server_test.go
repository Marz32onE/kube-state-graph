package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
		// Default empty vector.
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
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	logger := observability.NewLogger("error")
	metrics := observability.NewMetrics()
	prom, err := promql.New(cfg.PromURL, metrics)
	if err != nil {
		t.Fatalf("promql: %v", err)
	}
	c, err := cache.New(cfg.CacheMaxCostBytes, metrics)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	t.Cleanup(c.Close)
	builder := build.New(prom, cfg, metrics)
	return New(cfg, builder, c, prom, metrics, logger)
}

func TestGraphEndpoint_MissingStart(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/graph?end=2026-05-01T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	errField, _ := body["error"].(map[string]any)
	if errField["reason"] != "missing_start" {
		t.Errorf("unexpected reason: %v", errField)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestEdgeTypesEndpoint_StaticCatalogue(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/edge-types")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "max-age=3600") {
		t.Errorf("expected long Cache-Control, got %q", got)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Error("expected ETag header")
	}
	var body struct {
		APIVersion string `json:"apiVersion"`
		EdgeTypes  []struct {
			Type string `json:"type"`
		} `json:"edge_types"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.APIVersion != "v1" {
		t.Errorf("expected apiVersion v1, got %q", body.APIVersion)
	}
	want := map[string]bool{"pod-runs-on-node": false, "pod-mounts-pvc-on-node": false, "pod-calls-pod": false}
	for _, e := range body.EdgeTypes {
		if _, ok := want[e.Type]; ok {
			want[e.Type] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Errorf("missing edge type %q", k)
		}
	}
}

func TestEdgeTypesEndpoint_IfNoneMatch304(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/edge-types")
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/edge-types", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("expected 304, got %d", resp2.StatusCode)
	}
}

func TestLivez(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/livez")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAdminCacheFlush(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/admin/cache", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestDebugEndpoint_DisabledByDefault(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/debug/last-queries")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 when --enable-debug not set, got %d", resp.StatusCode)
	}
}
