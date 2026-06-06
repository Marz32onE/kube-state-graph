package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDebugLastQueries_RouteNotRegistered confirms removal of the debug route.
func TestDebugLastQueries_RouteNotRegistered(t *testing.T) {
	s := newServerWithMocks(t, newMockQuerier(t, nil), nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/debug/last-queries")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGraphEndpoint_MissingStart(t *testing.T) {
	s := newServerWithMocks(t, newMockQuerier(t, nil), nil)
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
	s := newServerWithMocks(t, newMockQuerier(t, nil), nil)
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
	s := newServerWithMocks(t, newMockQuerier(t, nil), nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/edge-types")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Cache-Control"), "max-age=3600")

	var body struct {
		APIVersion string `json:"apiVersion"`
		EdgeTypes  []struct {
			Type string `json:"type"`
		} `json:"edge_types"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "v1", body.APIVersion)
	want := map[string]bool{"pod-mounts-pvc": false, "pod-calls-pod": false}
	for _, e := range body.EdgeTypes {
		if _, ok := want[e.Type]; ok {
			want[e.Type] = true
		}
	}
	for k, v := range want {
		assert.Truef(t, v, "missing edge type %q", k)
	}
}

func TestLivez(t *testing.T) {
	s := newServerWithMocks(t, newMockQuerier(t, nil), nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/livez")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMetricsEndpoint(t *testing.T) {
	s := newServerWithMocks(t, newMockQuerier(t, nil), nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestDebugEndpoint_DisabledByDefault(t *testing.T) {
	s := newServerWithMocks(t, newMockQuerier(t, nil), nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/debug/last-queries")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
