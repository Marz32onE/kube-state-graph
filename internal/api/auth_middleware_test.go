package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuth_Disabled_AllRoutesPassThrough(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil) // empty keyset = auth disabled
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/livez", "/v1/edge-types", "/metrics"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equalf(t, http.StatusOK, resp.StatusCode, "path %s", path)
	}
}

func TestAuth_MissingHeader_Returns401(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServerWithKeys(t, mock, []string{"k1"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/edge-types")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	errField, _ := body["error"].(map[string]any)
	assert.Equal(t, "unauthorized", errField["reason"])
}

func TestAuth_WrongKey_Returns401(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServerWithKeys(t, mock, []string{"k1"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/edge-types", nil)
	req.Header.Set(APIKeyHeader, "wrong-key")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_ValidKey_Passes(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServerWithKeys(t, mock, []string{"k1", "k2"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for _, key := range []string{"k1", "k2"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/edge-types", nil)
		req.Header.Set(APIKeyHeader, key)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equalf(t, http.StatusOK, resp.StatusCode, "key %s rejected", key)
	}
}

func TestAuth_OpenPaths_BypassWithoutKey(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServerWithKeys(t, mock, []string{"k1"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/livez", "/metrics", "/openapi.yaml", "/openapi.json", "/docs"} {
		resp, err := http.Get(srv.URL + path)
		require.NoErrorf(t, err, "path %s", path)
		_ = resp.Body.Close()
		assert.Equalf(t, http.StatusOK, resp.StatusCode, "open path %s should not require key", path)
	}
}

func TestAuth_DocsAssets_BypassWithoutKey(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServerWithKeys(t, mock, []string{"k1"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/docs/assets/scalar.js")
	require.NoError(t, err)
	_ = resp.Body.Close()
	// Either 200 (asset present) or 404 (asset missing in test bundle), but
	// never 401: the route is exempt from auth.
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_GraphRoute_RequiresKey(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServerWithKeys(t, mock, []string{"k1"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/graph?start=2026-05-01T12:00:00Z&end=2026-05-01T12:05:00Z")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_DebugRoute_RequiresKeyWhenEnabled(t *testing.T) {
	mock := promMock(t, nil)
	cfg := func(c *struct{}) {} // unused; setup via override below
	_ = cfg
	// Reuse newTestServer override path to flip EnableDebug.
	mockSrv := mock
	s := newTestServerWithKeys(t, mockSrv, []string{"k1"})
	s.cfg.EnableDebug = true
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	// Without key.
	resp, err := http.Get(srv.URL + "/debug/last-queries")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// With key.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/debug/last-queries", nil)
	req.Header.Set(APIKeyHeader, "k1")
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp2.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp2.StatusCode)
}
