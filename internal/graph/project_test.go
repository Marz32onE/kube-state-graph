package graph

import (
	"testing"
	"time"
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
	g := sampleGraph()
	v := Project(g, Scope{})
	if len(v.Nodes) != 5 {
		t.Errorf("nodes: got %d, want 5", len(v.Nodes))
	}
	if len(v.Edges) != 5 {
		t.Errorf("edges: got %d, want 5", len(v.Edges))
	}
}

func TestProject_ClusterFilter(t *testing.T) {
	g := sampleGraph()
	v := Project(g, Scope{Clusters: map[string]struct{}{"cluster-alpha": {}}})
	for _, n := range v.Nodes {
		if c := n.Labels()["cluster"]; c != "cluster-alpha" {
			t.Errorf("unexpected cluster %q in filtered view", c)
		}
	}
}

func TestProject_NamespaceFilter(t *testing.T) {
	g := sampleGraph()
	v := Project(g, Scope{Namespaces: map[string]struct{}{"shop": {}}})
	for _, n := range v.Nodes {
		if n.Type() == NodeTypePod && n.Labels()["namespace"] != "shop" {
			t.Errorf("unexpected namespace %q", n.Labels()["namespace"])
		}
	}
}

func TestProject_EdgeTypeFilter(t *testing.T) {
	g := sampleGraph()
	v := Project(g, Scope{EdgeTypes: map[EdgeType]struct{}{EdgeTypePodCallsPod: {}}})
	for _, e := range v.Edges {
		if e.Type != EdgeTypePodCallsPod {
			t.Errorf("unexpected edge type %q", e.Type)
		}
	}
	if len(v.Edges) != 2 {
		t.Errorf("expected 2 pod-calls-pod edges, got %d", len(v.Edges))
	}
}

func TestProject_TraversalBoundedByDepth(t *testing.T) {
	g := sampleGraph()
	v := Project(g, Scope{Root: "cluster-alpha/p1", Depth: 1, Direction: DirectionOut})
	ids := map[string]bool{}
	for _, n := range v.Nodes {
		ids[n.ID()] = true
	}
	// depth=1 from p1 (out): p1, worker-0, p2, p3
	want := []string{"cluster-alpha/p1", "cluster-alpha/worker-0", "cluster-alpha/p2", "cluster-beta/p3"}
	for _, w := range want {
		if !ids[w] {
			t.Errorf("expected %s in traversal result, missing", w)
		}
	}
}

func TestProject_UnknownRootEmpty(t *testing.T) {
	g := sampleGraph()
	v := Project(g, Scope{Root: "does-not-exist", Depth: 2, Direction: DirectionBoth})
	if len(v.Nodes) != 0 || len(v.Edges) != 0 {
		t.Errorf("expected empty result, got %d nodes, %d edges", len(v.Nodes), len(v.Edges))
	}
}

func TestProject_DepthZero(t *testing.T) {
	g := sampleGraph()
	v := Project(g, Scope{Root: "cluster-alpha/p1", Depth: 0, Direction: DirectionBoth})
	if len(v.Nodes) != 1 {
		t.Errorf("expected 1 node at depth 0, got %d", len(v.Nodes))
	}
	if v.Nodes[0].ID() != "cluster-alpha/p1" {
		t.Errorf("unexpected root node %q", v.Nodes[0].ID())
	}
}
