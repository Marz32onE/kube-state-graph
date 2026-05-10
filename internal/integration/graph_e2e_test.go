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
	t1 := fixedNow.Unix() * 1000 // ms timestamps for /api/v1/import/prometheus
	t0 := fixedNow.Add(-time.Minute).Unix() * 1000
	const counterStep = 60.0 // seconds between t0 and t1 (matches rate denominator)

	// Per-series rates (req/s).
	const (
		rateCheckoutCart       = 5.0
		rateCheckoutPayments   = 2.0
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
traces_service_graph_request_total{client="checkout",server="cart",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="alpha-2",client_k8s_namespace_name="shop",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="cart",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="alpha-2",client_k8s_namespace_name="shop",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} %g %d
traces_service_graph_request_total{client="checkout",server="payments",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="beta-1",client_k8s_namespace_name="shop",server_k8s_namespace_name="billing",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="payments",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="beta-1",client_k8s_namespace_name="shop",server_k8s_namespace_name="billing",connection_type="virtual_node",test=%q} %g %d
traces_service_graph_request_total{client="https://payments.partner.example/api",server="checkout",cluster="cluster-alpha",client_k8s_pod_uid="",server_k8s_pod_uid="alpha-1",client_k8s_namespace_name="",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="https://payments.partner.example/api",server="checkout",cluster="cluster-alpha",client_k8s_pod_uid="",server_k8s_pod_uid="alpha-1",client_k8s_namespace_name="",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} %g %d
`,
		disc, t1, disc, t1, disc, t1,
		disc, t1, disc, t1, disc, t1,
		disc, t0, disc, v(rateCheckoutCart), t1,
		disc, t0, disc, v(rateCheckoutPayments), t1,
		disc, t0, disc, v(rateExternalToCheckout), t1,
	)
	s.IngestExpFmt(exposition)
	s.Require().True(s.WaitForSeries(`kube_pod_info{test=`+strconv.Quote(disc)+`}`, fixedNow, 30*time.Second),
		"VM did not observe ingested kube_pod_info")
	s.Require().True(
		s.WaitForSeries(`rate(traces_service_graph_request_total{test=`+strconv.Quote(disc)+`}[5m]) > 0`, fixedNow, 30*time.Second),
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

func (s *GraphSuite) httpGet(rawURL string) *http.Response {
	s.T().Helper()
	req, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, rawURL, nil)
	s.Require().NoError(err)
	resp, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	return resp
}

func (s *GraphSuite) TestSingleClusterGraph() {
	srv := s.StartAPIServer(func(cfg *config.Config) {

	})
	resp := s.httpGet(s.graphURL(srv.URL, nil))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)

	var body map[string]any
	s.Require().NoError(json.NewDecoder(resp.Body).Decode(&body))
	elements, _ := body["elements"].(map[string]any)
	nodes, _ := elements["nodes"].([]any)
	edges, _ := elements["edges"].([]any)
	s.NotEmpty(nodes, "expected at least one node")
	s.NotEmpty(edges, "expected at least one edge")

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
	s.GreaterOrEqual(podCalls, 1, "service-graph rate() returned no pod-call edges; fixture counter movement likely broken")
}

func (s *GraphSuite) TestCrossClusterEdgePresent() {
	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, func(q url.Values) { q.Set("edge_type", "pod-calls-pod") }))
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	// Cross-cluster status is recovered via the topology pod-UID index: the
	// metric stamped `cluster=cluster-alpha`, but `server_k8s_pod_uid=beta-1`
	// resolves to a topology pod whose own cluster is `cluster-beta`. The
	// edge should target that resolved pod and carry `cluster=cluster-alpha`
	// (the trace source / client side).
	bodyStr := string(body)
	s.Contains(bodyStr, `"target":"cluster-beta/beta-1"`, "cross-cluster target resolved via UID index")
	s.Contains(bodyStr, `"source":"cluster-alpha/alpha-1"`)
	s.Contains(bodyStr, `"cluster":"cluster-alpha"`)
	s.NotContains(bodyStr, `"client_cluster"`, "v1 edges must not carry client_cluster")
	s.NotContains(bodyStr, `"server_cluster"`, "v1 edges must not carry server_cluster")
}

func (s *GraphSuite) TestNameFilter_PodAnchor() {
	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, func(q url.Values) { q.Set("name", "checkout") }))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	s.Contains(bodyStr, `"id":"cluster-alpha/alpha-1"`, "checkout pod present")
	// Cross-cluster partner pod IS re-added by the unified edge-endpoint
	// rule on pod-calls-pod, so the cross-cluster edge can render with
	// both endpoints visible.
	s.Contains(bodyStr, `"id":"cluster-beta/beta-1"`,
		"cross-cluster partner pod re-added as edge endpoint of named anchor")
}

func (s *GraphSuite) TestNameFilter_UnknownReturnsEmpty() {
	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, func(q url.Values) { q.Set("name", "does-not-exist") }))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	s.Contains(bodyStr, `"nodes":[]`)
	s.Contains(bodyStr, `"edges":[]`)
}

func (s *GraphSuite) TestExternalNodeProducedByPattern() {
	srv := s.StartAPIServer(func(cfg *config.Config) {
		cfg.ExternalNamePattern = "://"

	})
	resp := s.httpGet(s.graphURL(srv.URL, func(q url.Values) { q.Set("edge_type", "pod-calls-pod") }))
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	s.Contains(string(body), `"type":"external"`)
	s.Contains(string(body), `"name":"https://payments.partner.example/api"`)
}

func (s *GraphSuite) TestETagRoundTrip304() {
	srv := s.StartAPIServer(func(cfg *config.Config) {})
	first := s.httpGet(s.graphURL(srv.URL, nil))
	etag := first.Header.Get("ETag")
	_ = first.Body.Close()
	s.Require().NotEmpty(etag)

	req, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, s.graphURL(srv.URL, nil), nil)
	s.Require().NoError(err)
	req.Header.Set("If-None-Match", etag)
	second, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer func() { _ = second.Body.Close() }()
	s.Equal(http.StatusNotModified, second.StatusCode)
}

func (s *GraphSuite) TestRepeatedRequestsReturnSameETag() {
	srv := s.StartAPIServer(func(cfg *config.Config) {})
	first := s.httpGet(s.graphURL(srv.URL, nil))
	etag1 := first.Header.Get("ETag")
	_ = first.Body.Close()
	s.Require().NotEmpty(etag1)

	second := s.httpGet(s.graphURL(srv.URL, nil))
	etag2 := second.Header.Get("ETag")
	_ = second.Body.Close()
	s.Equal(etag1, etag2, "deterministic body must yield deterministic ETag across rebuilds")
}

func (s *GraphSuite) TestClustersDiscovery() {
	// Discovery handler evaluates "now" via Server.nowFunc; pin it to fixedNow
	// so the 1h discovery lookback covers the statically-timestamped fixtures.
	srv, apiSrv := s.StartAPIServerWith(nil)
	apiSrv.SetNowFunc(func() time.Time { return fixedNow })
	resp := s.httpGet(srv.URL + "/v1/clusters")
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	s.Contains(string(body), "cluster-alpha")
	s.Contains(string(body), "cluster-beta")
}

func (s *GraphSuite) TestEdgeTypesCatalogue() {
	srv := s.StartAPIServer(nil)
	resp := s.httpGet(srv.URL + "/v1/edge-types")
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	for _, et := range []string{"pod-runs-on-node", "pod-mounts-pvc", "pod-calls-pod"} {
		s.Contains(string(body), et)
	}
}

func (s *GraphSuite) TestAPIKey_FileBacked_Enforced() {
	srv := s.StartAPIServer(func(cfg *config.Config) {

		cfg.APIKeys = "secret-test-key-1,secret-test-key-2"
	})

	// Without header → 401.
	without, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, s.graphURL(srv.URL, nil), nil)
	s.Require().NoError(err)
	resp1, err := http.DefaultClient.Do(without)
	s.Require().NoError(err)
	_ = resp1.Body.Close()
	s.Equal(http.StatusUnauthorized, resp1.StatusCode)

	// With wrong key → 401.
	wrong, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, s.graphURL(srv.URL, nil), nil)
	s.Require().NoError(err)
	wrong.Header.Set("X-API-Key", "nope")
	resp2, err := http.DefaultClient.Do(wrong)
	s.Require().NoError(err)
	_ = resp2.Body.Close()
	s.Equal(http.StatusUnauthorized, resp2.StatusCode)

	// With valid key → 200.
	good, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, s.graphURL(srv.URL, nil), nil)
	s.Require().NoError(err)
	good.Header.Set("X-API-Key", "secret-test-key-2")
	resp3, err := http.DefaultClient.Do(good)
	s.Require().NoError(err)
	_ = resp3.Body.Close()
	s.Equal(http.StatusOK, resp3.StatusCode)

	// /livez stays open even with auth enabled.
	live, err := http.Get(srv.URL + "/livez") //nolint:noctx,gosec // local httptest URL
	s.Require().NoError(err)
	_ = live.Body.Close()
	s.Equal(http.StatusOK, live.StatusCode)
}
