package build

import (
	"math/rand"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/pkg/graph"
)

// familyTopology models the cluster-family fan-out environment (D29):
//   - clusters prod-1 and prod-2 (one family: "prod-0") both deploy
//     messaging/nats and data/cache, each with one backing pod of its own;
//   - out-of-family staging-1 ("staging-0") also deploys messaging/nats —
//     it must never be matched by a prod-* trace.
func familyTopology() Topology {
	client := &graph.PodNode{IDValue: "prod-1/abc", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "prod-1", "namespace": "shop"}}
	natsP1 := &graph.PodNode{IDValue: "prod-1/n1", NameValue: "nats-0", LabelsValue: map[string]string{"cluster": "prod-1", "namespace": "messaging"}}
	natsP2 := &graph.PodNode{IDValue: "prod-2/n2", NameValue: "nats-0", LabelsValue: map[string]string{"cluster": "prod-2", "namespace": "messaging"}}
	cacheP1 := &graph.PodNode{IDValue: "prod-1/c1", NameValue: "cache-0", LabelsValue: map[string]string{"cluster": "prod-1", "namespace": "data"}}
	cacheP2 := &graph.PodNode{IDValue: "prod-2/c2", NameValue: "cache-0", LabelsValue: map[string]string{"cluster": "prod-2", "namespace": "data"}}
	natsS1 := &graph.PodNode{IDValue: "staging-1/sn", NameValue: "nats-0", LabelsValue: map[string]string{"cluster": "staging-1", "namespace": "messaging"}}
	return Topology{
		Pods:      []*graph.PodNode{client, natsP1, natsP2, cacheP1, cacheP2, natsS1},
		PodsByUID: map[string]*graph.PodNode{"abc": client, "n1": natsP1, "n2": natsP2, "c1": cacheP1, "c2": cacheP2, "sn": natsS1},
		ServicesByNameNS: map[serviceKey]ServiceObs{
			{"prod-1", "messaging", "nats"}:    {ClusterIP: "10.1.0.5"},
			{"prod-2", "messaging", "nats"}:    {ClusterIP: "10.2.0.5"},
			{"staging-1", "messaging", "nats"}: {ClusterIP: "10.9.0.5"},
			{"prod-1", "data", "cache"}:        {ClusterIP: "10.1.0.6"},
			{"prod-2", "data", "cache"}:        {ClusterIP: "10.2.0.6"},
		},
		EndpointsByService: map[serviceKey][]EndpointObs{
			{"prod-1", "messaging", "nats"}:    {{Pod: natsP1}},
			{"prod-2", "messaging", "nats"}:    {{Pod: natsP2}},
			{"staging-1", "messaging", "nats"}: {{Pod: natsS1}},
			{"prod-1", "data", "cache"}:        {{Pod: cacheP1}},
			{"prod-2", "data", "cache"}:        {{Pod: cacheP2}},
		},
		ClustersObserved: []string{"prod-1", "prod-2", "staging-1"},
	}
}

// famSample builds one service-graph sample. An empty cluster omits the
// `cluster` label entirely (exercising the "unknown" bucketing).
func famSample(client, server, cluster, clientUID, serverUID string) model.Sample {
	m := model.Metric{
		"client":             model.LabelValue(client),
		"server":             model.LabelValue(server),
		"client_k8s_pod_uid": model.LabelValue(clientUID),
		"server_k8s_pod_uid": model.LabelValue(serverUID),
	}
	if cluster != "" {
		m["cluster"] = model.LabelValue(cluster)
	}
	return model.Sample{Metric: m, Value: 5}
}

func TestParseServiceGraph_FamilyFanout_MultiClusterServiceEmitsOneEdgePerFamilyMatch(t *testing.T) {
	vec := sampleVec(famSample("checkout", "nats://nats.messaging.svc:4222", "prod-1", "abc", ""))
	res := parseServiceGraph(vec, familyTopology())

	// Both family clusters' service nodes materialise; out-of-family staging-1
	// must not (its family key "staging-0" differs from "prod-0").
	require.Len(t, res.ServiceNodes, 2, "one service node per family cluster holding the service")
	svcIDs := make([]string, 0, len(res.ServiceNodes))
	for _, s := range res.ServiceNodes {
		svcIDs = append(svcIDs, s.IDValue)
	}
	assert.ElementsMatch(t, []string{"prod-1/messaging/nats", "prod-2/messaging/nats"}, svcIDs)

	pcs := edgesByType(res, graph.EdgeTypePodCallsService)
	require.Len(t, pcs, 2, "one pod-calls-service edge per family match")
	targets := make([]string, 0, len(pcs))
	for _, e := range pcs {
		assert.Equal(t, "prod-1/abc", e.Source)
		assert.Equal(t, "prod-1", e.Labels["cluster"], "client side is a pod → every fan-out edge carries the trace cluster")
		targets = append(targets, e.Target)
	}
	assert.ElementsMatch(t, []string{"prod-1/messaging/nats", "prod-2/messaging/nats"}, targets)

	// Each service node fans out service-selects-pod ONLY to its own cluster's
	// backing pods (intra-cluster by construction).
	ssp := edgesByType(res, graph.EdgeTypeServiceSelectsPod)
	require.Len(t, ssp, 2)
	fanout := map[string]string{}
	for _, e := range ssp {
		fanout[e.Source] = e.Target
	}
	assert.Equal(t, map[string]string{
		"prod-1/messaging/nats": "prod-1/n1",
		"prod-2/messaging/nats": "prod-2/n2",
	}, fanout)

	assert.Empty(t, res.ExternalNodes, "resolved endpoint must not also produce an external node")
}

func TestParseServiceGraph_FamilyFanout_ZeroFamilyMatchesFallsBackToExternal(t *testing.T) {
	// data/queue exists in no cluster at all → external fallback, edge stays
	// pod-calls-pod (target is not a service node) — exactly the pre-fan-out shape.
	vec := sampleVec(famSample("checkout", "amqp://queue.data.svc:5672", "prod-1", "abc", ""))
	res := parseServiceGraph(vec, familyTopology())

	assert.Empty(t, res.ServiceNodes)
	require.Len(t, res.ExternalNodes, 1)
	assert.Equal(t, "external/amqp://queue.data.svc:5672", res.ExternalNodes[0].IDValue)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.EdgeTypePodCallsPod, res.Edges[0].Type)
	assert.Equal(t, "external/amqp://queue.data.svc:5672", res.Edges[0].Target)
}

func TestParseServiceGraph_FamilyFanout_OutOfFamilyOnlyServiceFallsBackToExternal(t *testing.T) {
	// messaging/nats exists ONLY in staging-1; the trace comes from prod-1.
	// staging-0 is not prod-0's family → zero candidates → external (D-C).
	topo := familyTopology()
	delete(topo.ServicesByNameNS, serviceKey{"prod-1", "messaging", "nats"})
	delete(topo.ServicesByNameNS, serviceKey{"prod-2", "messaging", "nats"})
	vec := sampleVec(famSample("checkout", "nats://nats.messaging.svc:4222", "prod-1", "abc", ""))
	res := parseServiceGraph(vec, topo)

	assert.Empty(t, res.ServiceNodes, "out-of-family staging-1 service must not be matched")
	require.Len(t, res.ExternalNodes, 1)
	assert.Equal(t, "external/nats://nats.messaging.svc:4222", res.ExternalNodes[0].IDValue)
}

func TestParseServiceGraph_FamilyFanout_BothSidesConnString_CrossProduct(t *testing.T) {
	// Client resolves to 2 nats services, server to 2 cache services → 4 edges.
	// A "://" client side never resolves to a pod, so no edge carries cluster.
	vec := sampleVec(famSample("nats://nats.messaging.svc:4222", "redis://cache.data.svc:6379", "prod-1", "", ""))
	res := parseServiceGraph(vec, familyTopology())

	require.Len(t, res.ServiceNodes, 4, "2 client-side + 2 server-side service nodes")
	pcs := edgesByType(res, graph.EdgeTypePodCallsService)
	require.Len(t, pcs, 4, "cross product 2×2")
	type pair struct{ src, tgt string }
	got := make([]pair, 0, len(pcs))
	for _, e := range pcs {
		assert.NotContains(t, e.Labels, "cluster", "client side is non-pod → cluster key omitted")
		got = append(got, pair{e.Source, e.Target})
	}
	assert.ElementsMatch(t, []pair{
		{"prod-1/messaging/nats", "prod-1/data/cache"},
		{"prod-1/messaging/nats", "prod-2/data/cache"},
		{"prod-2/messaging/nats", "prod-1/data/cache"},
		{"prod-2/messaging/nats", "prod-2/data/cache"},
	}, got)
}

func TestParseServiceGraph_FamilyFanout_MissingClusterLabelRecoversFamilyFromClientPod(t *testing.T) {
	// The series is missing its cluster label (bucketed to "unknown"), but the
	// client UID resolves to the prod-1 pod via the global UID index — the
	// server-side "://" family scope is recovered from the resolved pod's
	// cluster, so the fan-out fires across the prod-0 family. The edge's
	// labels.cluster stays the raw trace label ("unknown", per D9).
	vec := sampleVec(famSample("checkout", "nats://nats.messaging.svc:4222", "", "abc", ""))
	res := parseServiceGraph(vec, familyTopology())

	require.Len(t, res.ServiceNodes, 2, "family recovered from the UID-resolved client pod")
	pcs := edgesByType(res, graph.EdgeTypePodCallsService)
	require.Len(t, pcs, 2)
	for _, e := range pcs {
		assert.Equal(t, "prod-1/abc", e.Source)
		assert.Equal(t, "unknown", e.Labels["cluster"], "edge cluster label stays the raw trace label (D9)")
	}
	assert.Empty(t, res.ExternalNodes)
}

func TestParseServiceGraph_FamilyFanout_WrongClusterLabelRecoversFamilyFromClientPod(t *testing.T) {
	// The trace label disagrees with topology ("legacy-7" is no family member),
	// but the client UID resolves to the prod-1 pod — family scoping follows
	// the pod's authoritative cluster, not the label.
	vec := sampleVec(famSample("checkout", "nats://nats.messaging.svc:4222", "legacy-7", "abc", ""))
	res := parseServiceGraph(vec, familyTopology())

	require.Len(t, res.ServiceNodes, 2, "family recovered from the UID-resolved client pod, not the wrong label")
	assert.Empty(t, res.ExternalNodes)
}

func TestParseServiceGraph_FamilyFanout_UnknownClusterNonPodClientFallsBackToExternal(t *testing.T) {
	// Missing cluster label AND the client side is not a pod (non-URL human
	// label, no UID): no client cluster to recover, family("unknown") matches
	// no real cluster → the "://" server degrades to external (D-I).
	vec := sampleVec(famSample("admin", "nats://nats.messaging.svc:4222", "", "", ""))
	res := parseServiceGraph(vec, familyTopology())

	assert.Empty(t, res.ServiceNodes)
	extIDs := make([]string, 0, len(res.ExternalNodes))
	for _, ext := range res.ExternalNodes {
		extIDs = append(extIDs, ext.IDValue)
	}
	assert.ElementsMatch(t, []string{"external/admin", "external/nats://nats.messaging.svc:4222"}, extIDs)
}

func TestParseServiceGraph_SelfLoopUID_ConnStringSide_FansOutAcrossFamily(t *testing.T) {
	// D33 guard: both UIDs equal and the server label is a "://" string — the
	// server UID is cleared and that side now enjoys the widened family scope.
	vec := sampleVec(famSample("checkout", "nats://nats.messaging.svc:4222", "prod-1", "abc", "abc"))
	res := parseServiceGraph(vec, familyTopology())

	pcs := edgesByType(res, graph.EdgeTypePodCallsService)
	require.Len(t, pcs, 2, "cleared '://' side fans out across the family")
	for _, e := range pcs {
		assert.Equal(t, "prod-1/abc", e.Source, "non-'://' side keeps the shared UID and resolves to its pod")
	}
	assert.Empty(t, edgesByType(res, graph.EdgeTypePodCallsPod), "no self-loop pod edge")
}

func TestParseServiceGraph_EmptySideDropsSeriesWithoutMaterialisation(t *testing.T) {
	// A series with a wholly empty side (no UID, no label) is dropped BEFORE
	// resolution: the other side's "://" label must not leak service nodes or
	// fan-out edges as an orphan subgraph.
	t.Run("empty client side", func(t *testing.T) {
		vec := sampleVec(famSample("", "nats://nats.messaging.svc:4222", "prod-1", "", ""))
		res := parseServiceGraph(vec, familyTopology())
		assert.Empty(t, res.Edges)
		assert.Empty(t, res.ServiceNodes, "server-side fan-out must not materialise for a dropped series")
		assert.Empty(t, res.ExternalNodes)
	})
	t.Run("empty server side", func(t *testing.T) {
		vec := sampleVec(famSample("nats://nats.messaging.svc:4222", "", "prod-1", "", ""))
		res := parseServiceGraph(vec, familyTopology())
		assert.Empty(t, res.Edges)
		assert.Empty(t, res.ServiceNodes, "client-side fan-out must not materialise for a dropped series")
		assert.Empty(t, res.ExternalNodes)
	})
}

func TestParseServiceGraph_FamilyFanout_Deterministic(t *testing.T) {
	// Same fixture in two shuffled arrival orders → identical node and edge
	// SETS (IDs, UUIDv5 edge identities, multiplicity). Output slice order is
	// legitimately unspecified — the serialiser's graph.SortNodes/SortEdges
	// owns ordering — so the comparison is content-based (ElementsMatch), not
	// positional (D6 determinism of content, not of emission order).
	mkVec := func(seed int64) model.Vector {
		samples := []model.Sample{
			famSample("checkout", "nats://nats.messaging.svc:4222", "prod-1", "abc", ""),
			famSample("nats://nats.messaging.svc:4222", "redis://cache.data.svc:6379", "prod-2", "", ""),
			famSample("checkout", "amqp://queue.data.svc:5672", "prod-1", "abc", ""),
		}
		rng := rand.New(rand.NewSource(seed))
		rng.Shuffle(len(samples), func(i, j int) { samples[i], samples[j] = samples[j], samples[i] })
		return sampleVec(samples...)
	}

	summarise := func(res ServiceGraphResult) (nodes []string, edges []string) {
		for _, s := range res.ServiceNodes {
			nodes = append(nodes, s.IDValue)
		}
		for _, ext := range res.ExternalNodes {
			nodes = append(nodes, ext.IDValue)
		}
		for _, e := range res.Edges {
			edges = append(edges, string(e.Type)+"|"+e.Source+"|"+e.Target+"|"+e.ID)
		}
		return nodes, edges
	}

	n1, e1 := summarise(parseServiceGraph(mkVec(1), familyTopology()))
	n2, e2 := summarise(parseServiceGraph(mkVec(99), familyTopology()))
	assert.ElementsMatch(t, n1, n2, "node set must be arrival-order independent")
	assert.ElementsMatch(t, e1, e2, "edge set (incl. UUIDv5 IDs) must be arrival-order independent")
}
