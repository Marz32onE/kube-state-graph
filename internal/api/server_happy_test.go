package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// promFixturePodInfo returns one kube_pod_info sample so Builder.Build emits a
// non-empty topology and the request reaches the serialiser.
const promFixturePodInfo = `{"status":"success","data":{"resultType":"vector","result":[
  {"metric":{"cluster":"test","namespace":"default","pod":"web-1","uid":"uid-web-1","node":"node-a","host_ip":"10.0.0.1","pod_ip":"10.244.0.10","pod_ip_family":"IPv4"},
   "value":[1714515600,"1"]}
]}}`

const promFixtureNodeInfo = `{"status":"success","data":{"resultType":"vector","result":[
  {"metric":{"cluster":"test","node":"node-a","kernel_version":"6.1","os_image":"linux","container_runtime_version":"containerd"},
   "value":[1714515600,"1"]}
]}}`

func happyFixtures() map[string]string {
	return map[string]string{
		"last_over_time(kube_pod_info":  promFixturePodInfo,
		"last_over_time(kube_node_info": promFixtureNodeInfo,
	}
}

func graphURL(base string, start, end time.Time) string {
	q := url.Values{}
	q.Set("start", start.Format(time.RFC3339))
	q.Set("end", end.Format(time.RFC3339))
	return base + "?" + q.Encode()
}

func TestGraphEndpoint_HappyPath(t *testing.T) {
	mock := promMock(t, happyFixtures())
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	end := time.Now().UTC()
	start := end.Add(-15 * time.Minute)
	resp, err := http.Get(graphURL(srv.URL+"/v1/graph", start, end))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.NotEmpty(t, resp.Header.Get("ETag"))

	var body cytoscapeBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "v1", body.APIVersion)
	assert.NotEmpty(t, body.Elements.Nodes, "expected at least one node in cytoscape body")
}

func TestGraphEndpoint_HappyPath_IfNoneMatch304(t *testing.T) {
	mock := promMock(t, happyFixtures())
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	end := time.Now().UTC()
	start := end.Add(-15 * time.Minute)
	first, err := http.Get(graphURL(srv.URL+"/v1/graph", start, end))
	require.NoError(t, err)
	etag := first.Header.Get("ETag")
	first.Body.Close()
	require.NotEmpty(t, etag)

	req, _ := http.NewRequest(http.MethodGet, graphURL(srv.URL+"/v1/graph", start, end), nil)
	req.Header.Set("If-None-Match", etag)
	second, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer second.Body.Close()
	assert.Equal(t, http.StatusNotModified, second.StatusCode)
}

func TestNodeGraphEndpoint_HappyPath(t *testing.T) {
	mock := promMock(t, happyFixtures())
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	end := time.Now().UTC()
	start := end.Add(-15 * time.Minute)
	resp, err := http.Get(graphURL(srv.URL+"/v1/graph/nodegraph", start, end))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body grafanaBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "v1", body.APIVersion)
	assert.NotNil(t, body.Nodes)
	assert.NotNil(t, body.Edges)
}

func TestGraphEndpoint_OutsideRetention_ReturnsError(t *testing.T) {
	// All queries return empty + up probe returns 1 sample → outside_retention.
	mock := promMock(t, map[string]string{
		"up": `{"status":"success","data":{"resultType":"vector","result":[
		  {"metric":{"job":"vm"},"value":[1714515600,"1"]}
		]}}`,
	})
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	end := time.Now().UTC()
	start := end.Add(-15 * time.Minute)
	resp, err := http.Get(graphURL(srv.URL+"/v1/graph", start, end))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	errField, _ := body["error"].(map[string]any)
	assert.Equal(t, "outside_retention", errField["reason"])
}

func TestGraphEndpoint_UpstreamError_Returns502(t *testing.T) {
	// promMock that always returns 500 simulates upstream failure.
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"server","error":"boom"}`))
	}))
	t.Cleanup(srv500.Close)

	s := newTestServer(t, srv500, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	end := time.Now().UTC()
	start := end.Add(-15 * time.Minute)
	resp, err := http.Get(graphURL(srv.URL+"/v1/graph", start, end))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

// --- /v1/clusters --------------------------------------------------------

// countingProm wraps promMock-style fixtures and counts Instant invocations
// matching the discovery query so we can assert cache-hit behaviour.
type countingProm struct {
	calls atomic.Int32
}

func newCountingProm(t *testing.T, fixtures map[string]string) (*httptest.Server, *countingProm) {
	t.Helper()
	cp := &countingProm{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query := r.Form.Get("query")
		if strings.Contains(query, "group by (cluster)") {
			cp.calls.Add(1)
		}
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
	return srv, cp
}

const promFixtureClusters = `{"status":"success","data":{"resultType":"vector","result":[
  {"metric":{"cluster":"prod-east"},"value":[1714515600,"1"]},
  {"metric":{"cluster":"prod-west"},"value":[1714515600,"1"]},
  {"metric":{"cluster":"stg"},"value":[1714515600,"1"]}
]}}`

func TestClustersEndpoint_SortedOutput(t *testing.T) {
	mock := promMock(t, map[string]string{"group by (cluster)": promFixtureClusters})
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/clusters")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body clustersBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Clusters, 3)
	assert.Equal(t, "prod-east", body.Clusters[0].Name)
	assert.Equal(t, "prod-west", body.Clusters[1].Name)
	assert.Equal(t, "stg", body.Clusters[2].Name)
}

func TestClustersEndpoint_HitsUpstreamPerRequest(t *testing.T) {
	mock, cp := newCountingProm(t, map[string]string{"group by (cluster)": promFixtureClusters})
	s := newTestServer(t, mock, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for range 3 {
		resp, err := http.Get(srv.URL + "/v1/clusters")
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	assert.Equal(t, int32(3), cp.calls.Load(), "each /v1/clusters request must hit upstream (no in-process cache)")
}

func TestClustersEndpoint_UpstreamError_Returns502(t *testing.T) {
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"server","error":"boom"}`))
	}))
	t.Cleanup(srv500.Close)
	s := newTestServer(t, srv500, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/clusters")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}
