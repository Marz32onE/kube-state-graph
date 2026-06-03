package cytoscape

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/pkg/graph"
)

// cy serialises a view into Cytoscape shape, building the *graph.Graph the
// serialiser needs (it reads only g.ClusterNames()) from the supplied nodes.
func cy(t *testing.T, nodes []graph.GraphNode, edges []*graph.Edge) Body {
	t.Helper()
	byID := make(map[string]graph.GraphNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID()] = n
	}
	return Serialise(&graph.Graph{NodesByID: byID}, graph.View{Nodes: nodes, Edges: edges})
}

// cyNodesByID indexes the Cytoscape node data by node id for assertions.
func cyNodesByID(b Body) map[string]NodeData {
	m := make(map[string]NodeData, len(b.Elements.Nodes))
	for _, n := range b.Elements.Nodes {
		m[n.Data.ID] = n.Data
	}
	return m
}

// assertNoClusterGroup fails if any emitted node is a synthetic cluster group.
func assertNoClusterGroup(t *testing.T, nodes map[string]NodeData) {
	t.Helper()
	for id := range nodes {
		assert.NotContains(t, id, "cluster/", "no cluster group node expected")
	}
}

// cluster > node > pod nesting: a synthetic cluster group node is emitted, the
// K8s node is parented to the cluster, and the pod is parented to its K8s node
// via labels.node. There is no pod-runs-on-node edge — the compound nesting is
// the sole representation of the pod→node relationship (design.md D31).
func TestSerialiseCytoscape_CompoundClusterNodePod(t *testing.T) {
	pod := &graph.PodNode{IDValue: "c1/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop", "node": "c1/worker-0"}}
	node := &graph.K8sNode{IDValue: "c1/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "c1"}}

	body := cy(t, []graph.GraphNode{node, pod}, nil)
	nodes := cyNodesByID(body)

	cl, ok := nodes["cluster/c1"]
	require.True(t, ok, "cluster group node must be synthesised")
	assert.Equal(t, "cluster", cl.Type)
	assert.Equal(t, "c1", cl.Name)
	assert.Empty(t, cl.Parent, "cluster group node is top-level")

	assert.Equal(t, "cluster/c1", nodes["c1/worker-0"].Parent, "node parented to cluster")
	assert.Equal(t, "c1/worker-0", nodes["c1/p1"].Parent, "pod parented to its node (cluster > node > pod)")
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

// End-to-end (project → serialise): under a namespace filter the host K8s node
// is retained because it hosts an in-scope pod, so the pod nests under its node
// (cluster > node > pod) instead of falling back to the cluster group. Guards
// the regression where a namespace filter dropped the node and reparented the
// pod to the cluster. See design.md D31.
func TestSerialiseCytoscape_NamespaceFilterKeepsPodUnderNode(t *testing.T) {
	pod := &graph.PodNode{IDValue: "c1/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop", "node": "c1/worker-0"}}
	node := &graph.K8sNode{IDValue: "c1/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "c1"}}
	g := graph.NewGraph([]graph.GraphNode{pod, node}, nil, time.Now())

	view := graph.Project(g, graph.Scope{Namespaces: map[string]struct{}{"shop": {}}})
	nodes := cyNodesByID(Serialise(g, view))

	require.Contains(t, nodes, "c1/worker-0", "host node retained under namespace filter")
	assert.Equal(t, "c1/worker-0", nodes["c1/p1"].Parent, "pod nests under its node, not the cluster group")
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
