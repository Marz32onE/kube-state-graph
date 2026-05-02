package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/marz32one/kube-state-graph/internal/config"
)

// fixedNow is the absolute timestamp anchor every fixture and query uses.
// Per D20 / D5, integration tests MUST NOT use time.Now()-relative values for
// time-bucket alignment.
var fixedNow = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// GraphSuite covers the API contract end-to-end against a real VM container.
type GraphSuite struct {
	VMSuite
}

func TestGraphSuite(t *testing.T) {
	suite.Run(t, new(GraphSuite))
}

// SetupTest seeds the standard multi-cluster fixture set before each test
// using the per-test name as a discriminator label.
//
// Service-graph series are ingested as TWO monotonic counter samples (t0 and
// t1 = t0 + 60s) so that `rate(traces_service_graph_request_total[w])` over
// the test window can recover a non-zero per-second rate. Without two samples
// the rate() result is empty and every pod-call edge silently disappears.
func (s *GraphSuite) SetupTest() {
	disc := s.T().Name()
	t1 := fixedNow.Unix() * 1000          // ms timestamps for /api/v1/import/prometheus
	t0 := fixedNow.Add(-time.Minute).Unix() * 1000
	const counterStep = 60.0 // seconds between t0 and t1 (matches rate denominator)

	// Per-series rates (req/s).
	const (
		rateCheckoutCart      = 5.0
		rateCheckoutPayments  = 2.0
		rateExternalToCheckout = 1.0
	)
	v := func(rate float64) float64 { return rate * counterStep }

	exposition := fmt.Sprintf(`# HELP kube_pod_info dummy
kube_pod_info{cluster="cluster-alpha",namespace="shop",pod="checkout",uid="alpha-1",node="worker-0",test=%q} 1 %d
kube_pod_info{cluster="cluster-alpha",namespace="shop",pod="cart",uid="alpha-2",node="worker-0",test=%q} 1 %d
kube_pod_info{cluster="cluster-beta",namespace="billing",pod="payments",uid="beta-1",node="worker-0",test=%q} 1 %d
kube_node_info{cluster="cluster-alpha",node="worker-0",test=%q} 1 %d
kube_node_info{cluster="cluster-beta",node="worker-0",test=%q} 1 %d
kube_node_status_addresses{cluster="cluster-alpha",node="worker-0",type="ExternalIP",address="203.0.113.10",test=%q} 1 %d
traces_service_graph_request_total{client="checkout",server="cart",client_cluster="cluster-alpha",server_cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="alpha-2",client_k8s_namespace_name="shop",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="cart",client_cluster="cluster-alpha",server_cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="alpha-2",client_k8s_namespace_name="shop",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} %g %d
traces_service_graph_request_total{client="checkout",server="payments",client_cluster="cluster-alpha",server_cluster="cluster-beta",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="beta-1",client_k8s_namespace_name="shop",server_k8s_namespace_name="billing",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="payments",client_cluster="cluster-alpha",server_cluster="cluster-beta",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="beta-1",client_k8s_namespace_name="shop",server_k8s_namespace_name="billing",connection_type="virtual_node",test=%q} %g %d
traces_service_graph_request_total{client="https://payments.partner.example/api",server="checkout",client_cluster="",server_cluster="cluster-alpha",client_k8s_pod_uid="",server_k8s_pod_uid="alpha-1",client_k8s_namespace_name="",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="https://payments.partner.example/api",server="checkout",client_cluster="",server_cluster="cluster-alpha",client_k8s_pod_uid="",server_k8s_pod_uid="alpha-1",client_k8s_namespace_name="",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} %g %d
`,
		disc, t1, disc, t1, disc, t1,
		disc, t1, disc, t1, disc, t1,
		disc, t0, disc, v(rateCheckoutCart), t1,
		disc, t0, disc, v(rateCheckoutPayments), t1,
		disc, t0, disc, v(rateExternalToCheckout), t1,
	)
	s.IngestExpFmt(exposition)
	require.True(s.T(), s.WaitForSeries(`kube_pod_info{test=`+strconv.Quote(disc)+`}`, 10*time.Second),
		"VM did not observe ingested kube_pod_info")
	require.True(s.T(),
		s.WaitForSeries(`rate(traces_service_graph_request_total{test=`+strconv.Quote(disc)+`}[5m]) > 0`, 10*time.Second),
		"VM did not observe non-zero service-graph rate")
}

func (s *GraphSuite) graphURL(srv string, configureQuery func(url.Values)) string {
	q := url.Values{}
	q.Set("start", strconv.FormatInt(fixedNow.Add(-5*time.Minute).Unix(), 10))
	q.Set("end", strconv.FormatInt(fixedNow.Unix(), 10))
	if configureQuery != nil {
		configureQuery(q)
	}
	return srv + "/v1/graph?" + q.Encode()
}

func (s *GraphSuite) TestSingleClusterGraph() {
	srv := s.StartAPIServer(func(cfg *config.Config) {
		cfg.MaxSkew = 365 * 24 * time.Hour
	})
	resp, err := http.Get(s.graphURL(srv.URL, nil))
	require.NoError(s.T(), err)
	defer resp.Body.Close()
	require.Equal(s.T(), http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(s.T(), json.NewDecoder(resp.Body).Decode(&body))
	elements, _ := body["elements"].(map[string]any)
	nodes, _ := elements["nodes"].([]any)
	edges, _ := elements["edges"].([]any)
	assert.NotEmpty(s.T(), nodes, "expected at least one node")
	assert.NotEmpty(s.T(), edges, "expected at least one edge")

	// Regression guard for fixture/rate() drift: at least one pod-calls-pod
	// edge MUST survive. If service-graph fixtures lose counter movement, this
	// drops to zero before any other assertion notices.
	var podCalls int
	for _, raw := range edges {
		e, _ := raw.(map[string]any)
		data, _ := e["data"].(map[string]any)
		if data["type"] == "pod-calls-pod" {
			podCalls++
		}
	}
	assert.GreaterOrEqual(s.T(), podCalls, 1, "service-graph rate() returned no pod-call edges; fixture counter movement likely broken")
}

func (s *GraphSuite) TestCrossClusterEdgePresent() {
	srv := s.StartAPIServer(func(cfg *config.Config) { cfg.MaxSkew = 365 * 24 * time.Hour })
	resp, err := http.Get(s.graphURL(srv.URL, func(q url.Values) { q.Set("edge_type", "pod-calls-pod") }))
	require.NoError(s.T(), err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(s.T(), string(body), `"client_cluster":"cluster-alpha"`)
	assert.Contains(s.T(), string(body), `"server_cluster":"cluster-beta"`)
}

func (s *GraphSuite) TestExternalNodeProducedByPattern() {
	srv := s.StartAPIServer(func(cfg *config.Config) {
		cfg.ExternalNamePattern = "://"
		cfg.MaxSkew = 365 * 24 * time.Hour
	})
	resp, err := http.Get(s.graphURL(srv.URL, func(q url.Values) { q.Set("edge_type", "pod-calls-pod") }))
	require.NoError(s.T(), err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(s.T(), string(body), `"type":"external"`)
	assert.Contains(s.T(), string(body), `"name":"https://payments.partner.example/api"`)
}

func (s *GraphSuite) TestETagRoundTrip304() {
	srv := s.StartAPIServer(func(cfg *config.Config) { cfg.MaxSkew = 365 * 24 * time.Hour })
	first, err := http.Get(s.graphURL(srv.URL, nil))
	require.NoError(s.T(), err)
	etag := first.Header.Get("ETag")
	first.Body.Close()
	require.NotEmpty(s.T(), etag)

	req, _ := http.NewRequest(http.MethodGet, s.graphURL(srv.URL, nil), nil)
	req.Header.Set("If-None-Match", etag)
	second, err := http.DefaultClient.Do(req)
	require.NoError(s.T(), err)
	defer second.Body.Close()
	assert.Equal(s.T(), http.StatusNotModified, second.StatusCode)
}

func (s *GraphSuite) TestCacheMissThenHit() {
	srv := s.StartAPIServer(func(cfg *config.Config) { cfg.MaxSkew = 365 * 24 * time.Hour })
	miss, err := http.Get(s.graphURL(srv.URL, nil))
	require.NoError(s.T(), err)
	miss.Body.Close()
	assert.NotEqual(s.T(), "HIT", miss.Header.Get("X-Cache"), "first request should not be HIT")

	hit, err := http.Get(s.graphURL(srv.URL, nil))
	require.NoError(s.T(), err)
	hit.Body.Close()
	assert.Equal(s.T(), "HIT", hit.Header.Get("X-Cache"))
}

func (s *GraphSuite) TestClustersDiscovery() {
	srv := s.StartAPIServer(func(cfg *config.Config) { cfg.MaxSkew = 365 * 24 * time.Hour })
	resp, err := http.Get(srv.URL + "/v1/clusters")
	require.NoError(s.T(), err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(s.T(), string(body), "cluster-alpha")
	assert.Contains(s.T(), string(body), "cluster-beta")
}

func (s *GraphSuite) TestEdgeTypesCatalogue() {
	srv := s.StartAPIServer(nil)
	resp, err := http.Get(srv.URL + "/v1/edge-types")
	require.NoError(s.T(), err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, et := range []string{"pod-runs-on-node", "pod-mounts-pvc-on-node", "pod-calls-pod"} {
		assert.Contains(s.T(), string(body), et)
	}
}
