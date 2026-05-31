package api

import (
	"io"
	"io/fs"
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

// externalWoff2Re matches an absolute http(s) URL to a .woff2 file — Scalar's
// bundle ships such references (fonts.scalar.com); refresh-docs-ui.sh rewrites
// them to a locally served path so the docs UI fetches no external resources.
var externalWoff2Re = regexp.MustCompile(`https?://[^"')\s]+\.woff2`)

func TestDocs_OfflineInvariant(t *testing.T) {
	q := newMockQuerier(t, nil)
	s := newServerWithMocks(t, q, nil)
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
	q := newMockQuerier(t, nil)
	s := newServerWithMocks(t, q, nil)
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

// TestDocs_FontsServed enumerates the embedded web fonts and asserts every one
// is served with the correct woff2 content-type, so the vendored bundle renders
// with its intended typography offline.
func TestDocs_FontsServed(t *testing.T) {
	q := newMockQuerier(t, nil)
	s := newServerWithMocks(t, q, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	entries, err := fs.ReadDir(scalarFS, "static/scalar/fonts")
	require.NoError(t, err, "vendored fonts directory must be embedded")
	require.NotEmpty(t, entries, "no vendored fonts embedded")

	for _, e := range entries {
		name := e.Name()
		resp, err := http.Get(srv.URL + "/docs/assets/fonts/" + name)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "font %s", name)
		assert.Equal(t, "font/woff2", resp.Header.Get("Content-Type"), "font %s", name)
		assert.Contains(t, resp.Header.Get("Cache-Control"), "immutable", "font %s", name)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.NotEmpty(t, body, "font %s body empty", name)
		resp.Body.Close()
	}
}

// TestDocs_BundleHasNoExternalFontURL locks in the offline invariant for the
// JS/CSS bundle: no external font host may survive, and the JS must reference
// the locally served font path instead.
func TestDocs_BundleHasNoExternalFontURL(t *testing.T) {
	q := newMockQuerier(t, nil)
	s := newServerWithMocks(t, q, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for _, asset := range []string{"scalar.js", "scalar.css"} {
		resp, err := http.Get(srv.URL + "/docs/assets/" + asset)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		resp.Body.Close()

		assert.NotContains(t, string(body), "fonts.scalar.com",
			"%s must not reference the external Scalar font host", asset)
		assert.NotRegexp(t, externalWoff2Re, string(body),
			"%s must not reference any external .woff2 URL", asset)
	}

	resp, err := http.Get(srv.URL + "/docs/assets/scalar.js")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Contains(t, string(body), "/docs/assets/fonts/",
		"scalar.js must reference the locally served font path after rewrite")
}

func TestDocs_AssetsRejectsTraversal(t *testing.T) {
	q := newMockQuerier(t, nil)
	s := newServerWithMocks(t, q, nil)
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
	q := newMockQuerier(t, nil)
	s := newServerWithMocks(t, q, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/openapi.yaml")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/yaml")
	assert.Contains(t, resp.Header.Get("Cache-Control"), "max-age=3600")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "openapi:")
}

func TestOpenAPIJSONEndpoint(t *testing.T) {
	q := newMockQuerier(t, nil)
	s := newServerWithMocks(t, q, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/openapi.json")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	assert.Contains(t, resp.Header.Get("Cache-Control"), "max-age=3600")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.NotEmpty(t, body)
}
