package graph

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func sampleGraph() *Graph {
	pods := []GraphNode{
		&PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop", "node": "cluster-alpha/worker-0"}},
		&PodNode{IDValue: "cluster-alpha/p2", NameValue: "cart", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop", "node": "cluster-alpha/worker-0"}},
		&PodNode{IDValue: "cluster-beta/p3", NameValue: "payments", LabelsValue: map[string]string{"cluster": "cluster-beta", "namespace": "billing", "node": "cluster-beta/worker-0"}},
	}
	nodes := []GraphNode{
		&K8sNode{IDValue: "cluster-alpha/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "cluster-alpha"}},
		&K8sNode{IDValue: "cluster-beta/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "cluster-beta"}},
	}
	all := append([]GraphNode{}, pods...)
	all = append(all, nodes...)

	edges := []*Edge{
		NewEdge(EdgeTypePodRunsOnNode, "cluster-alpha/p1", "cluster-alpha/worker-0", nil),
		NewEdge(EdgeTypePodRunsOnNode, "cluster-alpha/p2", "cluster-alpha/worker-0", nil),
		NewEdge(EdgeTypePodRunsOnNode, "cluster-beta/p3", "cluster-beta/worker-0", nil),
		NewEdge(EdgeTypePodCallsPod, "cluster-alpha/p1", "cluster-alpha/p2", map[string]string{"client_cluster": "cluster-alpha", "server_cluster": "cluster-alpha"}),
		NewEdge(EdgeTypePodCallsPod, "cluster-alpha/p1", "cluster-beta/p3", map[string]string{"client_cluster": "cluster-alpha", "server_cluster": "cluster-beta"}),
	}
	return NewGraph(all, edges, time.Now())
}

func TestProject_NoFilter(t *testing.T) {
	v := Project(sampleGraph(), Scope{})
	assert.Len(t, v.Nodes, 5)
	assert.Len(t, v.Edges, 5)
}

func TestProject_ClusterFilter(t *testing.T) {
	v := Project(sampleGraph(), Scope{Clusters: map[string]struct{}{"cluster-alpha": {}}})

	ids := map[string]bool{}
	for _, n := range v.Nodes {
		ids[n.ID()] = true
	}
	// All cluster-alpha nodes present.
	for _, want := range []string{"cluster-alpha/p1", "cluster-alpha/p2", "cluster-alpha/worker-0"} {
		assert.Truef(t, ids[want], "expected %s in result", want)
	}
	// Cross-cluster partner cluster-beta/p3 preserved (graph-api spec
	// §"Cross-cluster edge representation"); the K8s node cluster-beta/worker-0
	// is not on a cross-cluster edge so MUST stay out.
	assert.True(t, ids["cluster-beta/p3"], "cross-cluster pod partner must be preserved")
	assert.False(t, ids["cluster-beta/worker-0"], "intra-cluster cluster-beta node must be filtered out")
}

func TestProject_ClusterFilter_PreservesCrossClusterEdge(t *testing.T) {
	v := Project(sampleGraph(), Scope{Clusters: map[string]struct{}{"cluster-alpha": {}}})

	var crossEdges int
	for _, e := range v.Edges {
		if e.Type == EdgeTypePodCallsPod && e.Labels["client_cluster"] != e.Labels["server_cluster"] {
			crossEdges++
			assert.Equal(t, "cluster-alpha/p1", e.Source)
			assert.Equal(t, "cluster-beta/p3", e.Target)
		}
	}
	assert.Equal(t, 1, crossEdges, "cross-cluster edge must survive cluster filter")
}

func TestProject_ClusterFilter_NamespaceStillStrict(t *testing.T) {
	// Namespace filter is AND-combined: cross-cluster partner whose namespace
	// does not match the filter MUST be dropped (and so must the edge).
	v := Project(sampleGraph(), Scope{
		Clusters:   map[string]struct{}{"cluster-alpha": {}},
		Namespaces: map[string]struct{}{"shop": {}},
	})

	ids := map[string]bool{}
	for _, n := range v.Nodes {
		ids[n.ID()] = true
	}
	assert.False(t, ids["cluster-beta/p3"], "partner with namespace=billing must not be re-added when namespace filter excludes it")
	for _, e := range v.Edges {
		if e.Type == EdgeTypePodCallsPod {
			assert.Equal(t, e.Labels["client_cluster"], e.Labels["server_cluster"], "no cross-cluster edge should survive namespace mismatch")
		}
	}
}

func TestProject_NamespaceFilter(t *testing.T) {
	v := Project(sampleGraph(), Scope{Namespaces: map[string]struct{}{"shop": {}}})
	for _, n := range v.Nodes {
		if n.Type() == NodeTypePod {
			assert.Equal(t, "shop", n.Labels()["namespace"])
		}
	}
}

func TestProject_EdgeTypeFilter(t *testing.T) {
	v := Project(sampleGraph(), Scope{EdgeTypes: map[EdgeType]struct{}{EdgeTypePodCallsPod: {}}})
	for _, e := range v.Edges {
		assert.Equal(t, EdgeTypePodCallsPod, e.Type)
	}
	assert.Len(t, v.Edges, 2)
}

func TestProject_TraversalBoundedByDepth(t *testing.T) {
	v := Project(sampleGraph(), Scope{Root: "cluster-alpha/p1", Depth: 1, Direction: DirectionOut})
	ids := map[string]bool{}
	for _, n := range v.Nodes {
		ids[n.ID()] = true
	}
	for _, want := range []string{"cluster-alpha/p1", "cluster-alpha/worker-0", "cluster-alpha/p2", "cluster-beta/p3"} {
		assert.Truef(t, ids[want], "expected %s in traversal result", want)
	}
}

func TestProject_UnknownRootEmpty(t *testing.T) {
	v := Project(sampleGraph(), Scope{Root: "does-not-exist", Depth: 2, Direction: DirectionBoth})
	assert.Empty(t, v.Nodes)
	assert.Empty(t, v.Edges)
}

func TestProject_DepthZero(t *testing.T) {
	v := Project(sampleGraph(), Scope{Root: "cluster-alpha/p1", Depth: 0, Direction: DirectionBoth})
	assert.Len(t, v.Nodes, 1)
	assert.Equal(t, "cluster-alpha/p1", v.Nodes[0].ID())
}

// Node filter exercises both branches of nodePassesFilters: pods are matched by
// their `node` label (cluster-scoped or plain name) while K8sNodes are matched
// by their plain Name(). worker-0 exists in two clusters; selecting "worker-0"
// must keep both K8sNodes and the pods running on either, but drop intra-cluster
// nodes that don't match.
func TestProject_NodeFilter_K8sNodeBranch(t *testing.T) {
	g := sampleGraph()
	scope := Scope{Nodes: map[string]struct{}{"worker-0": {}}}
	v := Project(g, scope)

	ids := map[string]bool{}
	for _, n := range v.Nodes {
		ids[n.ID()] = true
	}
	// Both worker-0 K8sNodes survive (K8sNode branch matches by Name()).
	assert.True(t, ids["cluster-alpha/worker-0"], "alpha worker-0 K8sNode missing")
	assert.True(t, ids["cluster-beta/worker-0"], "beta worker-0 K8sNode missing")
	// Pods scheduled on those nodes also survive (Pod branch matches by labels.node suffix).
	assert.True(t, ids["cluster-alpha/p1"], "pod on alpha worker-0 missing")
	assert.True(t, ids["cluster-beta/p3"], "pod on beta worker-0 missing")
}

// Namespace filter must NOT drop K8sNode (cluster-scoped, no namespace label).
// Otherwise pod-runs-on-node edges all vanish whenever a caller narrows by
// namespace — discovered against the kind-local rig where namespace=ksg gave
// 4 nodes and 0 edges.
func TestProject_NamespaceFilter_KeepsK8sNode(t *testing.T) {
	g := sampleGraph()
	v := Project(g, Scope{Namespaces: map[string]struct{}{"shop": {}}})

	var k8sCount, podCount, runEdges int
	for _, n := range v.Nodes {
		switch n.Type() {
		case NodeTypeK8sNode:
			k8sCount++
		case NodeTypePod:
			podCount++
		}
	}
	for _, e := range v.Edges {
		if e.Type == EdgeTypePodRunsOnNode {
			runEdges++
		}
	}
	assert.Equal(t, 2, podCount, "expected 2 pods in shop namespace")
	assert.GreaterOrEqual(t, k8sCount, 1, "K8sNode must survive namespace filter")
	assert.GreaterOrEqual(t, runEdges, 1, "pod-runs-on-node edges must survive when both endpoints in scope")
}

func TestProject_NodeFilter_K8sNode_NoMatch(t *testing.T) {
	g := sampleGraph()
	scope := Scope{Nodes: map[string]struct{}{"nonexistent-node": {}}}
	v := Project(g, scope)

	for _, n := range v.Nodes {
		if n.Type() == NodeTypeK8sNode {
			t.Errorf("unexpected K8sNode survived filter: %s", n.ID())
		}
	}
}
