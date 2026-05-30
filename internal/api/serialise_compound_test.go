package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/internal/graph"
)

// cy serialises a view into Cytoscape shape, building the *graph.Graph the
// serialiser needs (it reads only g.ClusterNames()) from the supplied nodes.
func cy(t *testing.T, nodes []graph.GraphNode, edges []*graph.Edge) cytoscapeBody {
	t.Helper()
	byID := make(map[string]graph.GraphNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID()] = n
	}
	return serialiseCytoscape(&graph.Graph{NodesByID: byID}, graph.View{Nodes: nodes, Edges: edges})
}

// cyNodesByID indexes the Cytoscape node data by node id for assertions.
func cyNodesByID(b cytoscapeBody) map[string]cytoscapeNodeData {
	m := make(map[string]cytoscapeNodeData, len(b.Elements.Nodes))
	for _, n := range b.Elements.Nodes {
		m[n.Data.ID] = n.Data
	}
	return m
}

// assertNoClusterGroup fails if any emitted node is a synthetic cluster group.
func assertNoClusterGroup(t *testing.T, nodes map[string]cytoscapeNodeData) {
	t.Helper()
	for id := range nodes {
		assert.NotContains(t, id, "cluster/", "no cluster group node expected")
	}
}

// cluster > node > pod nesting: a synthetic cluster group node is emitted, the
// K8s node is parented to the cluster, the pod is parented to its K8s node
// (via labels.node), and the redundant pod-runs-on-node edge is omitted from
// the Cytoscape output (the nesting expresses it).
func TestSerialiseCytoscape_CompoundClusterNodePod(t *testing.T) {
	pod := &graph.PodNode{IDValue: "c1/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop", "node": "c1/worker-0"}}
	node := &graph.K8sNode{IDValue: "c1/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "c1"}}
	runsOn := graph.NewEdge(graph.EdgeTypePodRunsOnNode, pod.IDValue, node.IDValue, nil)

	body := cy(t, []graph.GraphNode{node, pod}, []*graph.Edge{runsOn})
	nodes := cyNodesByID(body)

	cl, ok := nodes["cluster/c1"]
	require.True(t, ok, "cluster group node must be synthesised")
	assert.Equal(t, "cluster", cl.Type)
	assert.Equal(t, "c1", cl.Name)
	assert.Empty(t, cl.Parent, "cluster group node is top-level")

	assert.Equal(t, "cluster/c1", nodes["c1/worker-0"].Parent, "node parented to cluster")
	assert.Equal(t, "c1/worker-0", nodes["c1/p1"].Parent, "pod parented to its node (cluster > node > pod)")

	for _, e := range body.Elements.Edges {
		assert.NotEqual(t, string(graph.EdgeTypePodRunsOnNode), e.Data.Type,
			"pod-runs-on-node edge must be omitted from Cytoscape output")
	}
}

// Parent assignment across the remaining cases: pod fall-back when its node is
// out of scope, service/pvc → cluster, and the label-less endpoints (others /
// external / unknown-cluster pod) that get no parent and synthesise no group.
func TestSerialiseCytoscape_Parents(t *testing.T) {
	cases := []struct {
		name              string
		nodes             []graph.GraphNode
		wantParent        map[string]string // node id → expected data.parent ("" = none)
		wantNoClusterNode bool
	}{
		{
			name: "pod falls back to cluster when its node is absent from the view",
			nodes: []graph.GraphNode{
				&graph.PodNode{IDValue: "c1/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop", "node": "c1/worker-0"}},
			},
			wantParent: map[string]string{"c1/p1": "cluster/c1"},
		},
		{
			name: "service and pvc parented to cluster (siblings, never pod containers)",
			nodes: []graph.GraphNode{
				&graph.ServiceNode{IDValue: "c1/shop/payments", NameValue: "payments", LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop"}},
				&graph.PVCNode{IDValue: "c1/shop/data", NameValue: "data", LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop"}},
			},
			wantParent: map[string]string{"c1/shop/payments": "cluster/c1", "c1/shop/data": "cluster/c1"},
		},
		{
			name: "others and external have no parent and no cluster group",
			nodes: []graph.GraphNode{
				&graph.OthersNode{IDValue: "others/http://api.example.com", NameValue: "http://api.example.com", LabelsValue: map[string]string{}},
				&graph.ExternalNode{IDValue: "external/admin", NameValue: "admin", LabelsValue: map[string]string{}},
			},
			wantParent:        map[string]string{"others/http://api.example.com": "", "external/admin": ""},
			wantNoClusterNode: true,
		},
		{
			name: "synth pod with unknown cluster has no parent and no cluster group",
			nodes: []graph.GraphNode{
				&graph.PodNode{IDValue: "/orphan-uid", NameValue: "orphan-uid", LabelsValue: map[string]string{"cluster": ""}},
			},
			wantParent:        map[string]string{"/orphan-uid": ""},
			wantNoClusterNode: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes := cyNodesByID(cy(t, tc.nodes, nil))
			for id, want := range tc.wantParent {
				assert.Equal(t, want, nodes[id].Parent, "parent of %s", id)
			}
			if tc.wantNoClusterNode {
				assertNoClusterGroup(t, nodes)
			}
		})
	}
}

// Cluster group nodes are emitted first, sorted by cluster name, so the body
// stays byte-deterministic (D6).
func TestSerialiseCytoscape_ClusterNodesSortedFirst(t *testing.T) {
	a := &graph.PodNode{IDValue: "c-beta/p1", NameValue: "p", LabelsValue: map[string]string{"cluster": "c-beta"}}
	b := &graph.PodNode{IDValue: "c-alpha/p2", NameValue: "p", LabelsValue: map[string]string{"cluster": "c-alpha"}}

	body := cy(t, []graph.GraphNode{a, b}, nil)
	require.GreaterOrEqual(t, len(body.Elements.Nodes), 2)
	assert.Equal(t, "cluster/c-alpha", body.Elements.Nodes[0].Data.ID)
	assert.Equal(t, "cluster/c-beta", body.Elements.Nodes[1].Data.ID)
}

// The Grafana Node Graph serialiser is unaffected by compound: it emits no
// cluster group node and KEEPS the pod-runs-on-node edge (it cannot nest).
func TestSerialiseGrafana_UnaffectedByCompound(t *testing.T) {
	pod := &graph.PodNode{IDValue: "c1/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop", "node": "c1/worker-0"}}
	node := &graph.K8sNode{IDValue: "c1/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "c1"}}
	runsOn := graph.NewEdge(graph.EdgeTypePodRunsOnNode, pod.IDValue, node.IDValue, nil)
	view := graph.View{Nodes: []graph.GraphNode{node, pod}, Edges: []*graph.Edge{runsOn}}

	body := serialiseGrafanaNodeGraph(view)
	for _, n := range body.Nodes {
		assert.NotEqual(t, "cluster", n["mainStat"], "Grafana emits no cluster group node")
		assert.NotEqual(t, "cluster/c1", n["id"])
	}
	found := false
	for _, e := range body.Edges {
		if e["mainStat"] == string(graph.EdgeTypePodRunsOnNode) {
			found = true
		}
	}
	assert.True(t, found, "Grafana retains the pod-runs-on-node edge")
}
