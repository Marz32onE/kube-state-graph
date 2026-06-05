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
	"github.com/marz32one/kube-state-graph/pkg/clock"
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

// TestPodOwnerLabelsSkipReplicaSet — D34. Ingest kube_pod_owner for the
// `checkout` pod pointing at a ReplicaSet, plus kube_replicaset_owner mapping
// that ReplicaSet to a Deployment; assert /v1/graph stamps the pod node with
// owner_kind=Deployment / owner_name=<deployment> (the ReplicaSet is skipped),
// while the `cart` pod (no owner series) carries no owner labels.
func (s *GraphSuite) TestPodOwnerLabelsSkipReplicaSet() {
	disc := s.T().Name()
	t1 := fixedNow.Unix() * 1000
	s.IngestExpFmt(fmt.Sprintf(`# HELP kube_pod_owner dummy
kube_pod_owner{cluster="cluster-alpha",namespace="shop",pod="checkout",owner_kind="ReplicaSet",owner_name="checkout-7f9c",owner_is_controller="true",test=%q} 1 %d
kube_replicaset_owner{cluster="cluster-alpha",namespace="shop",replicaset="checkout-7f9c",owner_kind="Deployment",owner_name="checkout-deploy",test=%q} 1 %d
`, disc, t1, disc, t1))
	s.Require().True(s.WaitForSeries(`kube_pod_owner{test=`+strconv.Quote(disc)+`}`, fixedNow, 30*time.Second),
		"VM did not observe ingested kube_pod_owner")

	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, nil))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Elements struct {
			Nodes []struct {
				Data struct {
					Name   string            `json:"name"`
					Type   string            `json:"type"`
					Labels map[string]string `json:"labels"`
				} `json:"data"`
			} `json:"nodes"`
		} `json:"elements"`
	}
	s.Require().NoError(json.NewDecoder(resp.Body).Decode(&body))

	labelsFor := func(name string) (map[string]string, bool) {
		for _, n := range body.Elements.Nodes {
			if n.Data.Type == "pod" && n.Data.Name == name {
				return n.Data.Labels, true
			}
		}
		return nil, false
	}

	checkout, ok := labelsFor("checkout")
	s.Require().True(ok, "checkout pod node must be present")
	s.Equal("Deployment", checkout["owner_kind"], "ReplicaSet must be skipped to its Deployment")
	s.Equal("checkout-deploy", checkout["owner_name"])

	cart, ok := labelsFor("cart")
	s.Require().True(ok, "cart pod node must be present")
	_, hasKind := cart["owner_kind"]
	_, hasName := cart["owner_name"]
	s.False(hasKind, "pod with no owner series must omit owner_kind")
	s.False(hasName, "pod with no owner series must omit owner_name")
}

func (s *GraphSuite) TestConnStringUnresolvableProducesExternalNode() {
	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, func(q url.Values) { q.Set("edge_type", "pod-calls-pod") }))
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	s.Contains(string(body), `"type":"external"`)
	s.Contains(string(body), `"name":"https://payments.partner.example/api"`)
}

// TestConnStringServiceResolvesToServiceNodeWithBackingPods exercises the full
// D29 connection-string pipeline end-to-end against a real VM: kube_service_info
// + kube_endpointslice_{labels,endpoints} are read into the topology indexes,
// a checkout→https://payments-svc.shop.svc.cluster.local/api call (empty server
// UID) resolves the server to a type="service" node, and a service-selects-pod
// edge fans out to the backing "cart" pod (uid alpha-2, from the standard
// fixture). These extra series are ingested on top of SetupTest's standard set
// under the per-test discriminator.
func (s *GraphSuite) TestConnStringServiceResolvesToServiceNodeWithBackingPods() {
	disc := s.T().Name()
	t1 := fixedNow.Unix() * 1000
	t0 := fixedNow.Add(-time.Minute).Unix() * 1000
	extra := fmt.Sprintf(`# HELP kube_service_info dummy
kube_service_info{cluster="cluster-alpha",namespace="shop",service="payments-svc",cluster_ip="10.96.0.9",test=%q} 1 %d
kube_endpointslice_labels{cluster="cluster-alpha",namespace="shop",endpointslice="payments-svc-x1",label_kubernetes_io_service_name="payments-svc",test=%q} 1 %d
kube_endpointslice_endpoints{cluster="cluster-alpha",namespace="shop",endpointslice="payments-svc-x1",targetref_kind="Pod",targetref_name="cart",targetref_namespace="shop",hostname="cart",test=%q} 1 %d
traces_service_graph_request_total{client="checkout",server="https://payments-svc.shop.svc.cluster.local/api",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="",client_k8s_namespace_name="shop",server_k8s_namespace_name="",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="https://payments-svc.shop.svc.cluster.local/api",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="",client_k8s_namespace_name="shop",server_k8s_namespace_name="",connection_type="virtual_node",test=%q} 120 %d
`, disc, t1, disc, t1, disc, t1, disc, t0, disc, t1)
	s.IngestExpFmt(extra)
	s.Require().True(s.WaitForSeries(`kube_service_info{test=`+strconv.Quote(disc)+`}`, fixedNow, 30*time.Second),
		"VM did not observe ingested kube_service_info")

	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, nil))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Service node materialised from the connection string.
	s.Contains(bodyStr, `"type":"service"`, "ClusterIP connection string must resolve to a service node")
	s.Contains(bodyStr, `"id":"cluster-alpha/shop/payments-svc"`)
	s.Contains(bodyStr, `"10.96.0.9"`, "service node carries cluster_ip on ipaddress")
	// pod-calls-service edge points the client pod at the service node.
	s.Contains(bodyStr, `"type":"pod-calls-service"`,
		"call edge to a resolved service node is typed pod-calls-service")
	s.Contains(bodyStr, `"target":"cluster-alpha/shop/payments-svc"`)
	// service-selects-pod edge fans out to the backing pod (cart = alpha-2).
	s.Contains(bodyStr, `"type":"service-selects-pod"`)
	s.Contains(bodyStr, `"target":"cluster-alpha/alpha-2"`,
		"service-selects-pod edge resolves the backing cart pod via endpointslice targetref")
}

// TestConnStringHeadlessResolvesToServiceNodeNotPod exercises the D29 unified
// resolution end-to-end against a real VM: a headless per-pod connection string
// (checkout→redis://redis-0.redis-svc.shop.svc.cluster.local:6379, empty server
// UID) drops the leading pod-hostname `redis-0` and resolves to the SERVICE node
// `cluster-alpha/shop/redis-svc` (NOT the specific pod), fanning out a
// service-selects-pod edge to the backing pod. The headless service carries
// cluster_ip="None", so the service node has no ipaddress.
func (s *GraphSuite) TestConnStringHeadlessResolvesToServiceNodeNotPod() {
	disc := s.T().Name()
	t1 := fixedNow.Unix() * 1000
	t0 := fixedNow.Add(-time.Minute).Unix() * 1000
	extra := fmt.Sprintf(`# HELP kube_service_info dummy
kube_service_info{cluster="cluster-alpha",namespace="shop",service="redis-svc",cluster_ip="None",test=%q} 1 %d
kube_endpointslice_labels{cluster="cluster-alpha",namespace="shop",endpointslice="redis-svc-x1",label_kubernetes_io_service_name="redis-svc",test=%q} 1 %d
kube_endpointslice_endpoints{cluster="cluster-alpha",namespace="shop",endpointslice="redis-svc-x1",targetref_kind="Pod",targetref_name="cart",targetref_namespace="shop",test=%q} 1 %d
traces_service_graph_request_total{client="checkout",server="redis://redis-0.redis-svc.shop.svc.cluster.local:6379",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="",client_k8s_namespace_name="shop",server_k8s_namespace_name="",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="redis://redis-0.redis-svc.shop.svc.cluster.local:6379",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="",client_k8s_namespace_name="shop",server_k8s_namespace_name="",connection_type="virtual_node",test=%q} 120 %d
`, disc, t1, disc, t1, disc, t1, disc, t0, disc, t1)
	s.IngestExpFmt(extra)
	s.Require().True(s.WaitForSeries(`kube_service_info{service="redis-svc",test=`+strconv.Quote(disc)+`}`, fixedNow, 30*time.Second),
		"VM did not observe ingested headless kube_service_info")

	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, nil))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// The headless per-pod string resolves to the SERVICE node, not pod redis-0.
	s.Contains(bodyStr, `"id":"cluster-alpha/shop/redis-svc"`, "headless string must resolve to its service node")
	s.Contains(bodyStr, `"target":"cluster-alpha/shop/redis-svc"`,
		"pod-calls-service target is the service node (pod-hostname dropped), not a specific pod")
	// service-selects-pod fan-out reaches the backing pod (cart = alpha-2).
	s.Contains(bodyStr, `"type":"service-selects-pod"`)
	s.Contains(bodyStr, `"target":"cluster-alpha/alpha-2"`,
		"service-selects-pod edge resolves the backing pod via endpointslice targetref")
}

// TestConnStringSelfLoopUIDResolvesToServiceNode exercises design.md D33
// end-to-end against a real VM: a buggy exporter stamps the SAME pod UID on
// BOTH client_k8s_pod_uid and server_k8s_pod_uid for a "://" peer (the real
// remote lives only in the server label). Without the self-loop guard the
// server would collapse onto the caller's own pod (a self-loop pod-calls-pod)
// and no service node would materialise. The guard clears the bogus colliding
// UID on the "://" side so it falls through to D29 Stage 0, resolves to the
// service node, and fans out to the backing pod. A unique service name
// (selfloop-svc) proves the resolution came from THIS colliding-UID series.
func (s *GraphSuite) TestConnStringSelfLoopUIDResolvesToServiceNode() {
	disc := s.T().Name()
	t1 := fixedNow.Unix() * 1000
	t0 := fixedNow.Add(-time.Minute).Unix() * 1000
	extra := fmt.Sprintf(`# HELP kube_service_info dummy
kube_service_info{cluster="cluster-alpha",namespace="shop",service="selfloop-svc",cluster_ip="10.96.0.77",test=%q} 1 %d
kube_endpointslice_labels{cluster="cluster-alpha",namespace="shop",endpointslice="selfloop-svc-x1",label_kubernetes_io_service_name="selfloop-svc",test=%q} 1 %d
kube_endpointslice_endpoints{cluster="cluster-alpha",namespace="shop",endpointslice="selfloop-svc-x1",targetref_kind="Pod",targetref_name="cart",targetref_namespace="shop",test=%q} 1 %d
traces_service_graph_request_total{client="checkout",server="https://selfloop-svc.shop.svc.cluster.local/api",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="alpha-1",client_k8s_namespace_name="shop",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="https://selfloop-svc.shop.svc.cluster.local/api",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="alpha-1",client_k8s_namespace_name="shop",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} 120 %d
`, disc, t1, disc, t1, disc, t1, disc, t0, disc, t1)
	s.IngestExpFmt(extra)
	// One poll gates the whole batch: IngestExpFmt POSTs all series (gauge +
	// both counter samples) in a single request, so once kube_service_info is
	// queryable the rate() series are too — matching the sibling conn-string
	// tests, which assert their pod-calls-service edges off this one wait.
	s.Require().True(s.WaitForSeries(`kube_service_info{service="selfloop-svc",test=`+strconv.Quote(disc)+`}`, fixedNow, 30*time.Second),
		"VM did not observe ingested selfloop kube_service_info")

	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, nil))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Despite the colliding self-loop UID, the "://" server resolves to its
	// service node — proves the D33 guard cleared the bogus UID.
	s.Contains(bodyStr, `"id":"cluster-alpha/shop/selfloop-svc"`,
		"colliding self-loop UID must not block service resolution of the '://' side")
	s.Contains(bodyStr, `"target":"cluster-alpha/shop/selfloop-svc"`,
		"call edge targets the resolved service node, not the caller's own pod")
	s.Contains(bodyStr, `"type":"pod-calls-service"`)
	s.Contains(bodyStr, `"type":"service-selects-pod"`,
		"resolved service fans out to its backing pod")
}

// TestSentinelPeersExcludedAtQueryLayer exercises design.md D30 end-to-end
// against a real VM: the servicegraph connector's virtual peers (client="user",
// server="unknown") are dropped by the anchored selector matchers
// (client!~"user|unknown",server!~"user|unknown") so they never reach the API.
// Crucially the raw sentinel series ARE ingested into VM (asserted below), so a
// missing node proves the QUERY matcher excluded them — not that the data was
// absent. A connection string whose host merely CONTAINS "user"
// ("http://user/api") is NOT excluded (the match is fully anchored), proving
// the matcher is exact rather than substring.
func (s *GraphSuite) TestSentinelPeersExcludedAtQueryLayer() {
	disc := s.T().Name()
	t1 := fixedNow.Unix() * 1000
	t0 := fixedNow.Add(-time.Minute).Unix() * 1000
	extra := fmt.Sprintf(`# HELP traces_service_graph_request_total dummy
traces_service_graph_request_total{client="user",server="checkout",cluster="cluster-alpha",client_k8s_pod_uid="",server_k8s_pod_uid="alpha-1",client_k8s_namespace_name="",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="user",server="checkout",cluster="cluster-alpha",client_k8s_pod_uid="",server_k8s_pod_uid="alpha-1",client_k8s_namespace_name="",server_k8s_namespace_name="shop",connection_type="virtual_node",test=%q} 120 %d
traces_service_graph_request_total{client="checkout",server="unknown",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="",client_k8s_namespace_name="shop",server_k8s_namespace_name="",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="unknown",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="",client_k8s_namespace_name="shop",server_k8s_namespace_name="",connection_type="virtual_node",test=%q} 120 %d
traces_service_graph_request_total{client="checkout",server="http://user/api",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="",client_k8s_namespace_name="shop",server_k8s_namespace_name="",connection_type="virtual_node",test=%q} 0 %d
traces_service_graph_request_total{client="checkout",server="http://user/api",cluster="cluster-alpha",client_k8s_pod_uid="alpha-1",server_k8s_pod_uid="",client_k8s_namespace_name="shop",server_k8s_namespace_name="",connection_type="virtual_node",test=%q} 120 %d
`, disc, t0, disc, t1, disc, t0, disc, t1, disc, t0, disc, t1)
	s.IngestExpFmt(extra)

	// Prove VM actually holds the sentinel series (so a later absent node is
	// the matcher's doing, not missing data) and that the substring series
	// produces a non-zero rate the API build will pick up.
	s.Require().True(
		s.WaitForSeries(`traces_service_graph_request_total{client="user",test=`+strconv.Quote(disc)+`}`, fixedNow, 30*time.Second),
		"VM did not observe ingested sentinel client=\"user\" series")
	s.Require().True(
		s.WaitForSeries(`rate(traces_service_graph_request_total{server="http://user/api",test=`+strconv.Quote(disc)+`}[5m]) > 0`, fixedNow, 30*time.Second),
		"VM did not observe non-zero rate for the http://user/api series")

	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, nil))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Sentinel peers are excluded at the query layer: no node, no edge.
	s.NotContains(bodyStr, `external/user`, "client=\"user\" virtual peer must be excluded upstream")
	s.NotContains(bodyStr, `external/unknown`, "server=\"unknown\" virtual peer must be excluded upstream")
	s.NotContains(bodyStr, `"name":"user"`, "no node named user should appear")
	s.NotContains(bodyStr, `"name":"unknown"`, "no node named unknown should appear")

	// The anchored matcher does NOT catch a host that merely contains "user":
	// http://user/api survives and resolves to an external node.
	s.Contains(bodyStr, `"name":"http://user/api"`,
		"connection string containing (but not equal to) user must survive the anchored matcher")
}

func (s *GraphSuite) TestClustersDiscovery() {
	// Discovery handler evaluates "now" via the injected Clock. Pin it to
	// fixedNow so the 1h discovery lookback covers the statically-timestamped
	// fixtures.
	srv := s.StartAPIServer(nil, WithClock(clock.Fake{T: fixedNow}))
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
	for _, et := range []string{"pod-mounts-pvc", "pod-calls-pod", "pod-calls-service"} {
		s.Contains(string(body), et)
	}
}

// TestMetricPrefix_ResolvesPrefixedSeries covers design.md D26 end-to-end
// against a real VictoriaMetrics container: ingest a topology under an
// `o11y_`-prefixed metric-name family, start the API with
// `cfg.MetricPrefix="o11y_"`, and assert the resulting graph contains the
// pod node. Without the prefix knob the build would issue queries for stock
// `kube_pod_info` / `kube_node_info` which the fixture deliberately does NOT
// publish, so an empty graph would result.
func (s *GraphSuite) TestMetricPrefix_ResolvesPrefixedSeries() {
	disc := s.T().Name()
	t1 := fixedNow.Unix() * 1000

	exposition := fmt.Sprintf(`# HELP o11y_kube_pod_info dummy
o11y_kube_pod_info{cluster="cluster-prefix",namespace="ops",pod="prefixed-pod",uid="prefix-uid-1",node="worker-x",test=%q} 1 %d
o11y_kube_node_info{cluster="cluster-prefix",node="worker-x",test=%q} 1 %d
`,
		disc, t1,
		disc, t1,
	)
	s.IngestExpFmt(exposition)
	s.Require().True(
		s.WaitForSeries(`o11y_kube_pod_info{test=`+strconv.Quote(disc)+`}`, fixedNow, 30*time.Second),
		"VM did not observe ingested o11y_kube_pod_info",
	)

	srv := s.StartAPIServer(func(cfg *config.Config) {
		cfg.MetricPrefix = "o11y_"
	})
	resp := s.httpGet(s.graphURL(srv.URL, func(q url.Values) { q.Set("cluster", "cluster-prefix") }))
	defer func() { _ = resp.Body.Close() }()
	s.Require().Equal(http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	s.Contains(bodyStr, `"id":"cluster-prefix/prefix-uid-1"`,
		"pod resolved from o11y_-prefixed topology series")
	s.Contains(bodyStr, `"name":"prefixed-pod"`)
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
