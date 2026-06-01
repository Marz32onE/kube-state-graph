package build

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/pkg/graph"
)

// Sentinel-peer exclusion note (design.md D30): the servicegraph connector's
// virtual peers (client / server ∈ {"user", "unknown"}) are dropped at the
// PromQL QUERY layer via anchored matchers on the QServiceGraphTotal selector
// (see internal/promql/queries.go + queries_test.go), NOT inside
// parseServiceGraph. These tests therefore do not exercise sentinel filtering
// at the parse level — a sentinel label handed directly to parseServiceGraph is
// (correctly) still resolved, because excluded series never reach the parser in
// production. End-to-end exclusion is proven against a real VictoriaMetrics in
// internal/integration (TestSentinelPeersExcludedAtQueryLayer). Do NOT add a
// parse-level sentinel filter here; it belongs upstream in the selector.

func sampleTopology() Topology {
	alphaPod := &graph.PodNode{
		IDValue:     "cluster-alpha/abc",
		NameValue:   "checkout",
		LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"},
	}
	betaPod := &graph.PodNode{
		IDValue:     "cluster-beta/def",
		NameValue:   "payments",
		LabelsValue: map[string]string{"cluster": "cluster-beta", "namespace": "billing"},
	}
	return Topology{
		Pods: []*graph.PodNode{alphaPod, betaPod},
		PodsByUID: map[string]*graph.PodNode{
			"abc": alphaPod,
			"def": betaPod,
		},
	}
}

// sampleTopologyWithServices adds D29 service / endpoint / pod-name indexes:
//   - ClusterIP service "payments" (ns shop, cluster_ip 10.0.0.5) → pods pay0, pay1
//   - headless service "mongo" (ns db, cluster_ip None) → endpoint hostname "mongo-0" → m0
//   - headless service "redis" (ns db) with NO endpointslice hostname match, but
//     PodsByNameNS has redis-0 (exercises the StatefulSet pod-name fallback)
func sampleTopologyWithServices() Topology {
	clientPod := &graph.PodNode{IDValue: "cluster-alpha/abc", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	pay0 := &graph.PodNode{IDValue: "cluster-alpha/pay0", NameValue: "payments-0", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	pay1 := &graph.PodNode{IDValue: "cluster-alpha/pay1", NameValue: "payments-1", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	mongo0 := &graph.PodNode{IDValue: "cluster-alpha/m0", NameValue: "mongo-0", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "db"}}
	redis0 := &graph.PodNode{IDValue: "cluster-alpha/r0", NameValue: "redis-0", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "db"}}
	return Topology{
		Pods:      []*graph.PodNode{clientPod, pay0, pay1, mongo0, redis0},
		PodsByUID: map[string]*graph.PodNode{"abc": clientPod, "pay0": pay0, "pay1": pay1, "m0": mongo0, "r0": redis0},
		ServicesByNameNS: map[serviceKey]ServiceObs{
			{"cluster-alpha", "shop", "payments"}: {ClusterIP: "10.0.0.5"},
			{"cluster-alpha", "db", "mongo"}:      {ClusterIP: "None"},
			{"cluster-alpha", "db", "redis"}:      {ClusterIP: "None"},
		},
		EndpointsByService: map[serviceKey][]EndpointObs{
			{"cluster-alpha", "shop", "payments"}: {{Pod: pay0, Hostname: "payments-0"}, {Pod: pay1, Hostname: "payments-1"}},
			{"cluster-alpha", "db", "mongo"}:      {{Pod: mongo0, Hostname: "mongo-0"}},
			// "redis" deliberately has no endpoint hostname match.
		},
		PodsByNameNS: map[podNameKey]*graph.PodNode{
			{"cluster-alpha", "shop", "checkout"}:   clientPod,
			{"cluster-alpha", "shop", "payments-0"}: pay0,
			{"cluster-alpha", "shop", "payments-1"}: pay1,
			{"cluster-alpha", "db", "mongo-0"}:      mongo0,
			{"cluster-alpha", "db", "redis-0"}:      redis0,
		},
	}
}

func sampleVec(samples ...model.Sample) model.Vector {
	out := make(model.Vector, len(samples))
	for i := range samples {
		s := samples[i]
		out[i] = &s
	}
	return out
}

// edgesByType partitions a result's edges by edge type.
func edgesByType(res ServiceGraphResult, t graph.EdgeType) []*graph.Edge {
	var out []*graph.Edge
	for _, e := range res.Edges {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func TestParseServiceGraph_DropsZeroRate(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "abc",
		},
		Value: 0,
	})
	res := parseServiceGraph(vec, sampleTopology())
	assert.Empty(t, res.Edges)
}

func TestParseServiceGraph_CrossClusterEdge(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "payments",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopology())
	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "cluster-alpha/abc", e.Source)
	assert.Equal(t, "cluster-beta/def", e.Target, "server-side cluster recovered via UID index")
	assert.Equal(t, "cluster-alpha", e.Labels["cluster"], "edge cluster label = trace source cluster")
	for _, k := range []string{"client_cluster", "server_cluster", "rate", "p99_ms", "error_rate", "cross_cluster", "ghost"} {
		assert.NotContains(t, e.Labels, k, "unexpected label %q in v1 edge labels", k)
	}
}

func TestParseServiceGraph_IntraClusterEdge(t *testing.T) {
	alphaPod1 := &graph.PodNode{IDValue: "cluster-alpha/abc", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha"}}
	alphaPod2 := &graph.PodNode{IDValue: "cluster-alpha/xyz", NameValue: "cart", LabelsValue: map[string]string{"cluster": "cluster-alpha"}}
	topo := Topology{
		Pods:      []*graph.PodNode{alphaPod1, alphaPod2},
		PodsByUID: map[string]*graph.PodNode{"abc": alphaPod1, "xyz": alphaPod2},
	}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "xyz",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, topo)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "cluster-alpha", res.Edges[0].Labels["cluster"])
}

// ---------------------------------------------------------------------------
// D29: hardcoded "://" connection-string resolution.
// ---------------------------------------------------------------------------

func TestParseServiceGraph_ConnString_ServiceLevelResolvesToServiceNode(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "https://payments.shop.svc.cluster.local/api",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopologyWithServices())

	// Service node materialised with cluster_ip on ipaddress.
	require.Len(t, res.ServiceNodes, 1)
	svc := res.ServiceNodes[0]
	assert.Equal(t, "cluster-alpha/shop/payments", svc.IDValue)
	assert.Equal(t, "payments", svc.NameValue)
	assert.Equal(t, map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}, svc.LabelsValue)
	assert.Equal(t, []string{"10.0.0.5"}, svc.IPAddressValue)

	// pod-calls-pod edge points at the service node, carrying cluster (client is a pod).
	pcp := edgesByType(res, graph.EdgeTypePodCallsPod)
	require.Len(t, pcp, 1)
	assert.Equal(t, "cluster-alpha/abc", pcp[0].Source)
	assert.Equal(t, "cluster-alpha/shop/payments", pcp[0].Target)
	assert.Equal(t, "cluster-alpha", pcp[0].Labels["cluster"])

	// service-selects-pod edges fan out to both backing pods.
	ssp := edgesByType(res, graph.EdgeTypeServiceSelectsPod)
	require.Len(t, ssp, 2)
	gotTargets := []string{ssp[0].Target, ssp[1].Target}
	assert.ElementsMatch(t, []string{"cluster-alpha/pay0", "cluster-alpha/pay1"}, gotTargets)
	for _, e := range ssp {
		assert.Equal(t, "cluster-alpha/shop/payments", e.Source)
		assert.Equal(t, "shop", e.Labels["namespace"])
	}
}

func TestParseServiceGraph_ConnString_HeadlessResolvesToPod_ViaEndpointHostname(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "mongodb://mongo-0.mongo.db.svc.cluster.local:27017",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopologyWithServices())

	assert.Empty(t, res.ServiceNodes, "headless pod record resolves to a real pod, not a service node")
	assert.Empty(t, res.OthersNodes)
	pcp := edgesByType(res, graph.EdgeTypePodCallsPod)
	require.Len(t, pcp, 1)
	assert.Equal(t, "cluster-alpha/abc", pcp[0].Source)
	assert.Equal(t, "cluster-alpha/m0", pcp[0].Target, "endpointslice hostname mongo-0 → real pod")
	assert.Equal(t, "cluster-alpha", pcp[0].Labels["cluster"])
}

func TestParseServiceGraph_ConnString_HeadlessResolvesToPod_ViaPodNameFallback(t *testing.T) {
	// "redis" service has no endpointslice hostname match; resolution falls
	// back to PodsByNameNS (pod-name == hostname).
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "redis://redis-0.redis.db.svc.cluster.local:6379",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopologyWithServices())
	pcp := edgesByType(res, graph.EdgeTypePodCallsPod)
	require.Len(t, pcp, 1)
	assert.Equal(t, "cluster-alpha/r0", pcp[0].Target, "pod-name==hostname fallback resolves redis-0")
	assert.Empty(t, res.OthersNodes)
}

func TestParseServiceGraph_ConnString_ClientHeadlessPodMakesEdgeCarryCluster(t *testing.T) {
	// A client-side connection string that resolves to a real headless pod
	// now makes the edge carry labels.cluster (D29 improvement over the old
	// others-only behaviour).
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "mongodb://mongo-0.mongo.db.svc.cluster.local:27017",
			"server":             "checkout",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "",
			"server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopologyWithServices())
	pcp := edgesByType(res, graph.EdgeTypePodCallsPod)
	require.Len(t, pcp, 1)
	assert.Equal(t, "cluster-alpha/m0", pcp[0].Source, "client headless string → real pod")
	assert.Equal(t, "cluster-alpha/abc", pcp[0].Target)
	assert.Equal(t, "cluster-alpha", pcp[0].Labels["cluster"], "client resolved to a pod → edge carries cluster")
}

func TestParseServiceGraph_ConnString_UnresolvableExternalURL_BecomesOthers(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "https://payments.partner.example/api",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopologyWithServices())

	require.Len(t, res.OthersNodes, 1)
	oth := res.OthersNodes[0]
	assert.Equal(t, "others/https://payments.partner.example/api", oth.IDValue)
	assert.Equal(t, "https://payments.partner.example/api", oth.NameValue)
	assert.Empty(t, oth.LabelsValue, "others node carries empty labels (no pattern key)")
	assert.Empty(t, res.ExternalNodes, `"://" labels never reach the external fallback`)

	pcp := edgesByType(res, graph.EdgeTypePodCallsPod)
	require.Len(t, pcp, 1)
	assert.Equal(t, "others/https://payments.partner.example/api", pcp[0].Target)
	assert.Equal(t, "cluster-alpha", pcp[0].Labels["cluster"], "client side is a pod")
}

func TestParseServiceGraph_ConnString_EmptyUIDWithURL_NeverExternal(t *testing.T) {
	// Both endpoints have empty UID; both labels are "://" URLs. Neither becomes
	// an external node — connection-string resolution handles them (here: both
	// unresolvable → others).
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "https://a.partner.example",
			"server":             "https://b.partner.example",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopologyWithServices())
	assert.Empty(t, res.ExternalNodes, `no external node may be produced for a "://" label`)
	assert.Len(t, res.OthersNodes, 2)
}

func TestParseServiceGraph_ConnString_NonK8sHostBecomesOthers(t *testing.T) {
	// A "://" connection string whose host is not a 2/3-label k8s .svc name —
	// e.g. an IP:port or a bare single-label host — is not classifiable as a
	// service/headless record and falls back to an others node.
	for _, server := range []string{
		"grpc://10.0.0.5:50051",          // IP host → 4 dotted labels → dnsNone
		"redis://my-redis:6379",          // single-label host → dnsNone
		"amqp://broker.a.b.c.d.svc:5672", // >3 service-relative labels → dnsNone
	} {
		t.Run(server, func(t *testing.T) {
			vec := sampleVec(model.Sample{
				Metric: model.Metric{
					"client": "checkout", "server": model.LabelValue(server),
					"cluster": "cluster-alpha", "client_k8s_pod_uid": "abc", "server_k8s_pod_uid": "",
				},
				Value: 5,
			})
			res := parseServiceGraph(vec, sampleTopologyWithServices())
			require.Len(t, res.OthersNodes, 1)
			assert.Equal(t, graph.OthersID(server), res.OthersNodes[0].IDValue)
			assert.Empty(t, res.ServiceNodes)
		})
	}
}

func TestParseServiceGraph_ConnString_UnknownServiceBecomesOthers(t *testing.T) {
	// A 2-label service-level connection string whose service is absent from the
	// trace cluster's topology resolves to an others node (labels={}).
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client": "checkout", "server": "https://ghost-svc.ghost-ns.svc.cluster.local/x",
			"cluster": "cluster-alpha", "client_k8s_pod_uid": "abc", "server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopologyWithServices())
	require.Len(t, res.OthersNodes, 1)
	assert.Equal(t, "others/https://ghost-svc.ghost-ns.svc.cluster.local/x", res.OthersNodes[0].IDValue)
	assert.Empty(t, res.OthersNodes[0].LabelsValue)
	assert.Empty(t, res.ServiceNodes, "unknown service must not materialise a service node")
}

func TestParseServiceGraph_ConnString_HeadlessArbitraryHostnameViaEndpointSlice(t *testing.T) {
	// Pod NAME differs from its DNS hostname (arbitrary spec.hostname). Only the
	// endpointslice hostname index can resolve it — PodsByNameNS (keyed by pod
	// name) has NO "mongo-0" entry. Proves the endpointslice-hostname primary
	// path independently of the StatefulSet pod-name fallback.
	mongoPod := &graph.PodNode{IDValue: "cluster-alpha/m0", NameValue: "mongo-statefulset-xyz", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "db"}}
	clientPod := &graph.PodNode{IDValue: "cluster-alpha/abc", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	topo := Topology{
		Pods:      []*graph.PodNode{clientPod, mongoPod},
		PodsByUID: map[string]*graph.PodNode{"abc": clientPod, "m0": mongoPod},
		ServicesByNameNS: map[serviceKey]ServiceObs{
			{"cluster-alpha", "db", "mongo"}: {ClusterIP: "None"},
		},
		EndpointsByService: map[serviceKey][]EndpointObs{
			{"cluster-alpha", "db", "mongo"}: {{Pod: mongoPod, Hostname: "mongo-0"}},
		},
		PodsByNameNS: map[podNameKey]*graph.PodNode{
			{"cluster-alpha", "shop", "checkout"}: clientPod,
			// deliberately NO {"cluster-alpha","db","mongo-0"} — pod name is mongo-statefulset-xyz.
			{"cluster-alpha", "db", "mongo-statefulset-xyz"}: mongoPod,
		},
	}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client": "checkout", "server": "mongodb://mongo-0.mongo.db.svc.cluster.local:27017",
			"cluster": "cluster-alpha", "client_k8s_pod_uid": "abc", "server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, topo)
	pcp := edgesByType(res, graph.EdgeTypePodCallsPod)
	require.Len(t, pcp, 1)
	assert.Equal(t, "cluster-alpha/m0", pcp[0].Target, "arbitrary hostname mongo-0 resolved via endpointslice to pod m0")
	assert.Empty(t, res.OthersNodes, "must resolve to the real pod, not fall back to others")
}

func TestParseServiceGraph_ConnString_ServiceMaterialisedOnceAcrossSeries(t *testing.T) {
	// Two distinct clients call the same ClusterIP service. The service node and
	// its service-selects-pod edges materialise exactly once (deduped), while
	// two pod-calls-pod edges are produced.
	topo := sampleTopologyWithServices()
	mk := func(client, clientUID string) model.Sample {
		return model.Sample{
			Metric: model.Metric{
				"client": model.LabelValue(client), "server": "https://payments.shop.svc.cluster.local/api",
				"cluster": "cluster-alpha", "client_k8s_pod_uid": model.LabelValue(clientUID), "server_k8s_pod_uid": "",
			},
			Value: 5,
		}
	}
	vec := sampleVec(mk("checkout", "abc"), mk("payments-0", "pay0"))
	res := parseServiceGraph(vec, topo)

	require.Len(t, res.ServiceNodes, 1, "service node materialised once despite two referencing series")
	ssp := edgesByType(res, graph.EdgeTypeServiceSelectsPod)
	require.Len(t, ssp, 2, "payments has two backing pods; each service-selects-pod edge deduped to one")
	pcp := edgesByType(res, graph.EdgeTypePodCallsPod)
	require.Len(t, pcp, 2, "two distinct clients → two pod-calls-pod edges to the service")
}

func TestParseServiceGraph_UIDPresentSkipsConnStringResolution(t *testing.T) {
	// A client label containing "://" but with a NON-empty UID resolves by pod
	// UID; Stage 0 does not run.
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "http://api.example.com",
			"server":             "payments",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopology())
	assert.Empty(t, res.OthersNodes)
	assert.Empty(t, res.ExternalNodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "cluster-alpha/abc", res.Edges[0].Source)
}

func TestParseServiceGraph_GhostFallback_ServerUIDUnknown(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "missing",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "missing-uid",
		},
		Value: 1,
	})
	res := parseServiceGraph(vec, sampleTopology())
	require.Len(t, res.SynthPods, 1)
	sp := res.SynthPods[0]
	assert.Equal(t, "/missing-uid", sp.IDValue, "synth pod ID has empty cluster prefix when server cluster unknown")
	assert.Empty(t, sp.LabelsValue["cluster"], "server-side synth pod has empty cluster label")
	assert.NotContains(t, sp.LabelsValue, "ghost", "ghost label must NOT be set in v1")
}

func TestParseServiceGraph_EmptyVectorIsNotAnError(t *testing.T) {
	res := parseServiceGraph(nil, sampleTopology())
	assert.Empty(t, res.Edges)
}

func TestParseServiceGraph_DedupSamePair(t *testing.T) {
	vec := sampleVec(
		model.Sample{
			Metric: model.Metric{
				"client": "checkout", "server": "payments", "cluster": "cluster-alpha",
				"client_k8s_pod_uid": "abc", "server_k8s_pod_uid": "def", "connection_type": "virtual_node",
			},
			Value: 5,
		},
		model.Sample{
			Metric: model.Metric{
				"client": "checkout", "server": "payments", "cluster": "cluster-alpha",
				"client_k8s_pod_uid": "abc", "server_k8s_pod_uid": "def", "connection_type": "messaging_system",
			},
			Value: 3,
		},
	)
	res := parseServiceGraph(vec, sampleTopology())
	require.Len(t, res.Edges, 1, "duplicate (src,tgt) series must collapse into one edge")
	ids := map[string]int{}
	for _, e := range res.Edges {
		ids[e.ID]++
	}
	for id, n := range ids {
		assert.Equal(t, 1, n, "edge id %s appeared %d times", id, n)
	}
}

// ---------------------------------------------------------------------------
// D27: Missing pod-UID human-label fallback (non-URL labels only).
// ---------------------------------------------------------------------------

func TestParseServiceGraph_MissingClientUID_PromotesToExternal(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client": "admin", "server": "checkout", "cluster": "cluster-alpha",
			"client_k8s_pod_uid": "", "server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopology())

	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "external/admin", e.Source)
	assert.Equal(t, "cluster-alpha/abc", e.Target)
	assert.NotContains(t, e.Labels, "cluster",
		"edge cluster label MUST be omitted when client side is external (missing-UID fallback)")

	require.Len(t, res.ExternalNodes, 1)
	ext := res.ExternalNodes[0]
	assert.Equal(t, "external/admin", ext.IDValue)
	assert.Equal(t, "admin", ext.NameValue)
	assert.Empty(t, ext.LabelsValue)
}

func TestParseServiceGraph_MissingServerUID_PromotesToExternal(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client": "checkout", "server": "payments", "cluster": "cluster-alpha",
			"client_k8s_pod_uid": "abc", "server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopology())

	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "cluster-alpha/abc", e.Source)
	assert.Equal(t, "external/payments", e.Target)
	assert.Equal(t, "cluster-alpha", e.Labels["cluster"],
		"edge keeps labels.cluster when client side is still a pod")

	require.Len(t, res.ExternalNodes, 1)
	assert.Equal(t, "external/payments", res.ExternalNodes[0].IDValue)
	assert.Empty(t, res.ExternalNodes[0].LabelsValue)
}

func TestParseServiceGraph_BothUIDsMissing_BothLabelsPresent(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client": "admin", "server": "payments", "cluster": "cluster-alpha",
			"client_k8s_pod_uid": "", "server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopology())

	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "external/admin", e.Source)
	assert.Equal(t, "external/payments", e.Target)
	assert.NotContains(t, e.Labels, "cluster")

	require.Len(t, res.ExternalNodes, 2)
	gotIDs := map[string]bool{}
	for _, ext := range res.ExternalNodes {
		gotIDs[ext.IDValue] = true
	}
	assert.True(t, gotIDs["external/admin"])
	assert.True(t, gotIDs["external/payments"])
}

func TestParseServiceGraph_UIDAndLabelBothEmpty_EdgeDropped_ClientSide(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client": "", "server": "checkout", "cluster": "cluster-alpha",
			"client_k8s_pod_uid": "", "server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopology())
	assert.Empty(t, res.Edges, "edge MUST be dropped when both client UID and label are empty")
	assert.Empty(t, res.ExternalNodes)
}

func TestParseServiceGraph_UIDAndLabelBothEmpty_EdgeDropped_ServerSide(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client": "checkout", "server": "", "cluster": "cluster-alpha",
			"client_k8s_pod_uid": "abc", "server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, sampleTopology())
	assert.Empty(t, res.Edges, "edge MUST be dropped when both server UID and label are empty")
	assert.Empty(t, res.ExternalNodes)
}

func TestParseServiceGraph_ConnStringWinsOverMissingUIDFallback(t *testing.T) {
	// A "://" client label with empty UID is handled by connection-string
	// resolution (here unresolvable → others, labels={}); the missing-UID
	// external fallback must NOT run for it.
	alphaPod := &graph.PodNode{IDValue: "cluster-alpha/abc", LabelsValue: map[string]string{"cluster": "cluster-alpha"}}
	topo := Topology{Pods: []*graph.PodNode{alphaPod}, PodsByUID: map[string]*graph.PodNode{"abc": alphaPod}}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client": "http://api.example.com", "server": "checkout", "cluster": "cluster-alpha",
			"client_k8s_pod_uid": "", "server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, topo)

	require.Len(t, res.OthersNodes, 1)
	assert.Empty(t, res.ExternalNodes, "connection-string resolution wins; external fallback must NOT run")
	assert.Equal(t, "others/http://api.example.com", res.OthersNodes[0].IDValue)
	assert.Empty(t, res.OthersNodes[0].LabelsValue, "others node carries empty labels (no pattern key)")
}

func TestParseServiceGraph_OthersAndExternalCoexistInOneParse(t *testing.T) {
	// One parse can produce BOTH an `others` node (unresolvable "://" string)
	// AND an `external` node (non-URL missing-UID label) — disjoint maps/types.
	betaPod := &graph.PodNode{IDValue: "cluster-beta/def", LabelsValue: map[string]string{"cluster": "cluster-beta"}}
	topo := Topology{Pods: []*graph.PodNode{betaPod}, PodsByUID: map[string]*graph.PodNode{"def": betaPod}}
	vec := sampleVec(
		model.Sample{
			Metric: model.Metric{
				"client": "https://ext.partner.example/x", "server": "payments", "cluster": "cluster-alpha",
				"client_k8s_pod_uid": "", "server_k8s_pod_uid": "def",
			},
			Value: 5,
		},
		model.Sample{
			Metric: model.Metric{
				"client": "stray-caller", "server": "payments", "cluster": "cluster-alpha",
				"client_k8s_pod_uid": "", "server_k8s_pod_uid": "def",
			},
			Value: 3,
		},
	)
	res := parseServiceGraph(vec, topo)

	require.Len(t, res.OthersNodes, 1, `unresolvable "://" string → one others node`)
	require.Len(t, res.ExternalNodes, 1, "non-URL missing-UID label → one external node")
	assert.Equal(t, "others/https://ext.partner.example/x", res.OthersNodes[0].IDValue)
	assert.Empty(t, res.OthersNodes[0].LabelsValue)
	assert.Equal(t, "external/stray-caller", res.ExternalNodes[0].IDValue)
	assert.Empty(t, res.ExternalNodes[0].LabelsValue)
}

func TestProperty_ParseServiceGraph_EveryEdgeHasNonEmptyEndpoints(t *testing.T) {
	topo := sampleTopology()
	for seed := int64(1); seed <= 25; seed++ {
		r := rand.New(rand.NewSource(seed))
		samples := make([]model.Sample, 0, 20)
		for i := range 20 {
			clientUID := pickUID(r)
			serverUID := pickUID(r)
			clientLabel := pickLabel(r, "client", i)
			serverLabel := pickLabel(r, "server", i)
			if clientUID == "" && clientLabel == "" {
				continue
			}
			if serverUID == "" && serverLabel == "" {
				continue
			}
			samples = append(samples, model.Sample{
				Metric: model.Metric{
					"client":             model.LabelValue(clientLabel),
					"server":             model.LabelValue(serverLabel),
					"cluster":            "cluster-alpha",
					"client_k8s_pod_uid": model.LabelValue(clientUID),
					"server_k8s_pod_uid": model.LabelValue(serverUID),
				},
				Value: 5,
			})
		}
		res := parseServiceGraph(sampleVec(samples...), topo)
		for _, e := range res.Edges {
			require.NotEmptyf(t, e.Source, "seed=%d: edge has empty source id", seed)
			require.NotEmptyf(t, e.Target, "seed=%d: edge has empty target id", seed)
		}
	}
}

func pickUID(r *rand.Rand) string {
	switch r.Intn(4) {
	case 0:
		return "abc"
	case 1:
		return "def"
	case 2:
		return "ghost-uid"
	default:
		return ""
	}
}

func pickLabel(r *rand.Rand, side string, i int) string {
	if r.Intn(4) == 0 {
		return ""
	}
	return fmt.Sprintf("%s-%d", side, i)
}

func TestParseServiceGraph_NoForbiddenNumericLabels(t *testing.T) {
	alphaPod1 := &graph.PodNode{IDValue: "cluster-alpha/abc", LabelsValue: map[string]string{"cluster": "cluster-alpha"}}
	alphaPod2 := &graph.PodNode{IDValue: "cluster-alpha/def", LabelsValue: map[string]string{"cluster": "cluster-alpha"}}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster": "cluster-alpha", "client_k8s_pod_uid": "abc", "server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, Topology{
		Pods:      []*graph.PodNode{alphaPod1, alphaPod2},
		PodsByUID: map[string]*graph.PodNode{"abc": alphaPod1, "def": alphaPod2},
	})
	for _, e := range res.Edges {
		for _, k := range []string{"rate", "p99_ms", "error_rate"} {
			assert.NotContains(t, e.Labels, k)
		}
	}
}
