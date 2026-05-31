package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/internal/graph"
)

var update = flag.Bool("update", false, "update golden files")

// TestGolden_GraphResponses snapshots the Cytoscape responses for several
// canned scenarios so contract drift is caught on PR.
func TestGolden_GraphResponses(t *testing.T) {
	scenarios := map[string]graph.View{
		"single-cluster":       buildSingleCluster(),
		"two-cluster-cross":    buildTwoClusterCross(),
		"with-others":          buildWithOthers(),
		"with-service":         buildWithService(),
		"name-filter":          buildNameFilter(),
		"missing-uid-fallback": buildMissingUIDFallback(),
	}

	for name, view := range scenarios {
		t.Run(name+"-cytoscape", func(t *testing.T) {
			g := &graph.Graph{BuiltAt: time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC), NodesByID: map[string]graph.GraphNode{}}
			for _, n := range view.Nodes {
				g.NodesByID[n.ID()] = n
			}
			body := serialiseCytoscape(g, view)
			compareGolden(t, name+"-cytoscape.json", body)
		})
	}
}

func TestGolden_EdgeTypes(t *testing.T) {
	body := map[string]any{
		"apiVersion": APIVersion,
		"edge_types": graph.EdgeTypes,
	}
	compareGolden(t, "edge-types.json", body)
}

func compareGolden(t *testing.T, file string, body any) {
	t.Helper()
	got, err := json.MarshalIndent(body, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", file)

	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}

	want, err := os.ReadFile(path)
	require.NoErrorf(t, err, "read golden (run with -update)")
	assert.Truef(t, bytes.Equal(want, got), "golden mismatch for %s\n--- want\n%s\n--- got\n%s", file, want, got)
}

// ----- canned scenarios -----------------------------------------------------

func buildSingleCluster() graph.View {
	pod := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop", "node": "cluster-alpha/worker-0"}}
	node := &graph.K8sNode{IDValue: "cluster-alpha/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "cluster-alpha"}}
	return graph.View{Nodes: []graph.GraphNode{node, pod}}
}

func buildTwoClusterCross() graph.View {
	a := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	b := &graph.PodNode{IDValue: "cluster-beta/p2", NameValue: "payments", LabelsValue: map[string]string{"cluster": "cluster-beta", "namespace": "billing"}}
	cross := graph.NewEdge(graph.EdgeTypePodCallsPod, a.IDValue, b.IDValue, map[string]string{
		"cluster": "cluster-alpha",
	})
	return graph.View{Nodes: []graph.GraphNode{a, b}, Edges: []*graph.Edge{cross}}
}

func buildWithOthers() graph.View {
	pod := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	oth := &graph.OthersNode{IDValue: "others/http://api.example.com", NameValue: "http://api.example.com", LabelsValue: map[string]string{}}
	// Client side is a pod, so edge keeps labels.cluster = client cluster.
	// Server side is `others` (an unresolved "://" connection string, D29;
	// labels={}, no cluster).
	edge := graph.NewEdge(graph.EdgeTypePodCallsPod, pod.IDValue, oth.IDValue, map[string]string{
		"cluster": "cluster-alpha",
	})
	return graph.View{Nodes: []graph.GraphNode{pod, oth}, Edges: []*graph.Edge{edge}}
}

// buildWithService snapshots the D29 connection-string service resolution:
// a pod-calls-pod edge whose target is a `type="service"` node (resolved from
// a `<service>.<namespace>.svc...` string, carrying cluster_ip on ipaddress),
// plus a `service-selects-pod` edge fanning out to a backing pod.
func buildWithService() graph.View {
	pod := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	svc := &graph.ServiceNode{IDValue: "cluster-alpha/shop/payments", NameValue: "payments", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}, IPAddressValue: []string{"10.0.0.5"}}
	pay0 := &graph.PodNode{IDValue: "cluster-alpha/pay0", NameValue: "payments-0", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	edges := []*graph.Edge{
		graph.NewEdge(graph.EdgeTypePodCallsPod, pod.IDValue, svc.IDValue, map[string]string{"cluster": "cluster-alpha"}),
		graph.NewEdge(graph.EdgeTypeServiceSelectsPod, svc.IDValue, pay0.IDValue, map[string]string{"namespace": "shop"}),
	}
	return graph.View{Nodes: []graph.GraphNode{pod, svc, pay0}, Edges: edges}
}

// buildMissingUIDFallback snapshots the D27 fallback shape: a service-graph
// series whose client_k8s_pod_uid is empty surfaces as `external/<label>`
// with an empty labels map (distinguishable from the pattern-matched
// external case, which carries labels.pattern). The edge omits
// labels.cluster because the client side is external.
func buildMissingUIDFallback() graph.View {
	pod := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	ext := &graph.ExternalNode{IDValue: "external/admin", NameValue: "admin", LabelsValue: map[string]string{}}
	edge := graph.NewEdge(graph.EdgeTypePodCallsPod, ext.IDValue, pod.IDValue, map[string]string{})
	return graph.View{Nodes: []graph.GraphNode{pod, ext}, Edges: []*graph.Edge{edge}}
}

// buildNameFilter snapshots the projection of a two-cluster graph through
// `?name=checkout`. The matching pod (cluster-alpha/p1) is the anchor; the
// cross-cluster partner pod (cluster-beta/p2) is re-added via the unified
// edge-endpoint partner rule on the pod-calls-pod edge. The host K8s nodes
// carry no edges (pod→node is compound nesting via labels.node only), so a
// name-filtered view does not pull them in.
func buildNameFilter() graph.View {
	a := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop", "node": "cluster-alpha/worker-0"}}
	b := &graph.PodNode{IDValue: "cluster-beta/p2", NameValue: "payments", LabelsValue: map[string]string{"cluster": "cluster-beta", "namespace": "billing", "node": "cluster-beta/worker-0"}}
	nodeA := &graph.K8sNode{IDValue: "cluster-alpha/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "cluster-alpha"}}
	nodeB := &graph.K8sNode{IDValue: "cluster-beta/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "cluster-beta"}}
	edges := []*graph.Edge{
		graph.NewEdge(graph.EdgeTypePodCallsPod, a.IDValue, b.IDValue, map[string]string{"cluster": "cluster-alpha"}),
	}
	g := graph.NewGraph([]graph.GraphNode{a, b, nodeA, nodeB}, edges, time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC))
	return graph.Project(g, graph.Scope{Names: map[string]struct{}{"checkout": {}}})
}
