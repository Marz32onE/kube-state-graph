package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// httpsLinkRe matches absolute https:// references in <script src=...> or
// <link href=...> attributes — exactly the patterns that would defeat the
// offline-rendering invariant.
var httpsLinkRe = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*["']https://`)

func TestDocs_OfflineInvariant(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/docs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.NotRegexp(t, httpsLinkRe, string(body),
		"/docs HTML must not reference any https:// origin (vendored offline UI invariant)")
}

func TestDocs_AssetsServed(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for _, asset := range []string{"scalar.js", "scalar.css"} {
		resp, err := http.Get(srv.URL + "/docs/assets/" + asset)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "asset %s", asset)
		assert.Contains(t, resp.Header.Get("Cache-Control"), "max-age=86400")
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.NotEmpty(t, body, "asset %s body empty", asset)
		resp.Body.Close()
	}
}

func TestDocs_AssetsRejectsTraversal(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for _, evil := range []string{"../docs.go", "../../go.mod", "..\\evil"} {
		resp, err := http.Get(srv.URL + "/docs/assets/" + evil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "expected 404 for traversal %q", evil)
		resp.Body.Close()
	}
}

func TestOpenAPIYAMLEndpoint(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/openapi.yaml")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/yaml")
	assert.Contains(t, resp.Header.Get("Cache-Control"), "max-age=3600")
	assert.NotEmpty(t, resp.Header.Get("ETag"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "openapi:")
}

func TestOpenAPIJSONEndpoint_IfNoneMatch304(t *testing.T) {
	mock := promMock(t, nil)
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/openapi.json")
	require.NoError(t, err)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	require.NotEmpty(t, etag)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/openapi.json", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusNotModified, resp2.StatusCode)
}
