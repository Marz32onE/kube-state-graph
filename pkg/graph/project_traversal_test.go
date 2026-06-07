package graph

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chainGraph: p1 --pod-calls-pod--> p2 --pod-mounts-pvc--> data
// The PVC is reachable from p1 only by crossing a pod-mounts-pvc edge.
func chainGraph() *Graph {
	nodes := []GraphNode{
		&PodNode{IDValue: "cluster-a/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-a", "namespace": "shop"}},
		&PodNode{IDValue: "cluster-a/p2", NameValue: "cart", LabelsValue: map[string]string{"cluster": "cluster-a", "namespace": "shop"}},
		&PVCNode{IDValue: "cluster-a/shop/data", NameValue: "data", LabelsValue: map[string]string{"cluster": "cluster-a", "namespace": "shop"}},
	}
	edges := []*Edge{
		NewEdge(EdgeTypePodCallsPod, "cluster-a/p1", "cluster-a/p2", map[string]string{"cluster": "cluster-a"}),
		NewEdge(EdgeTypePodMountsPVC, "cluster-a/p2", "cluster-a/shop/data", map[string]string{}),
	}
	return NewGraph(nodes, edges, time.Now())
}

// assertNoOrphans asserts every non-root node in the view has at least one
// incident edge — the invariant N4 restores for traversal + edge-type filter.
func assertNoOrphans(t *testing.T, v View, root string) {
	t.Helper()
	incident := map[string]struct{}{}
	for _, e := range v.Edges {
		incident[e.Source] = struct{}{}
		incident[e.Target] = struct{}{}
	}
	for _, n := range v.Nodes {
		if n.ID() == root {
			continue
		}
		_, ok := incident[n.ID()]
		assert.Truef(t, ok, "node %s is an orphan (in view but no incident edge)", n.ID())
	}
}

// TestProject_TraversalEdgeTypeFilter_NoOrphan guards N4: BFS must only cross
// in-scope edge types, so a node reachable solely via a filtered-out edge type
// never enters the view as an orphan.
func TestProject_TraversalEdgeTypeFilter_NoOrphan(t *testing.T) {
	v := Project(chainGraph(), Scope{
		Root:      "cluster-a/p1",
		Depth:     2,
		Direction: DirectionOut,
		EdgeTypes: map[EdgeType]struct{}{EdgeTypePodCallsPod: {}},
	})

	ids := map[string]bool{}
	for _, n := range v.Nodes {
		ids[n.ID()] = true
	}
	assert.True(t, ids["cluster-a/p1"], "root present")
	assert.True(t, ids["cluster-a/p2"], "p2 reachable via in-scope pod-calls-pod edge")
	assert.False(t, ids["cluster-a/shop/data"],
		"PVC reachable only via a filtered-out pod-mounts-pvc edge must NOT appear")
	assert.Len(t, v.Edges, 1)
	assertNoOrphans(t, v, "cluster-a/p1")
}

// TestProject_TraversalEmptyDirectionDefaultsToBoth guards N9: a Scope built
// directly with Root + Depth but no Direction must still traverse (default
// "both"), not collapse to the root alone.
func TestProject_TraversalEmptyDirectionDefaultsToBoth(t *testing.T) {
	v := Project(chainGraph(), Scope{Root: "cluster-a/p1", Depth: 2})

	ids := map[string]bool{}
	for _, n := range v.Nodes {
		ids[n.ID()] = true
	}
	require.Len(t, v.Nodes, 3, "empty Direction must default to both and traverse the full chain")
	assert.True(t, ids["cluster-a/p2"])
	assert.True(t, ids["cluster-a/shop/data"])
}
