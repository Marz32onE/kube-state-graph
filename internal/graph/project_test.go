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
	for _, n := range v.Nodes {
		assert.Equal(t, "cluster-alpha", n.Labels()["cluster"])
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
