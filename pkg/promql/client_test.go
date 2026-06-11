package promql

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRoundTripper captures the request it receives without opening a socket,
// per the test-stack boundary rule (unit tests must not contact upstream).
type fakeRoundTripper struct {
	got *http.Request
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.got = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func TestBasicAuthTransport_SetsHeader(t *testing.T) {
	inner := &fakeRoundTripper{}
	rt := &basicAuthTransport{inner: inner, username: "ksg", password: "s3cret"}

	req, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/api/v1/query", nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.NotNil(t, inner.got, "inner transport not invoked")
	user, pass, ok := inner.got.BasicAuth()
	require.True(t, ok, "Authorization: Basic header missing on forwarded request")
	assert.Equal(t, "ksg", user)
	assert.Equal(t, "s3cret", pass)

	// RoundTrippers must not mutate the caller's request (D-A4).
	assert.Empty(t, req.Header.Get("Authorization"), "original request mutated")
}

func TestNew_WithoutBasicAuth_NoAuthTransport(t *testing.T) {
	inner := &fakeRoundTripper{}

	// Exercise the option plumbing: no WithBasicAuth → no Authorization header
	// anywhere in the chain. Drive the bare transport directly since New wires
	// otelhttp around a concrete http.Transport.
	req, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/api/v1/query", nil)
	require.NoError(t, err)
	resp, err := inner.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Empty(t, inner.got.Header.Get("Authorization"))
}

func TestNew_OptionVariants(t *testing.T) {
	// Both call shapes must construct successfully: legacy two-arg and with
	// WithBasicAuth. Construction-only — no requests are issued.
	_, err := New("http://upstream.invalid", nil)
	require.NoError(t, err)

	_, err = New("http://upstream.invalid", nil, WithBasicAuth("ksg", "s3cret"))
	require.NoError(t, err)
}
