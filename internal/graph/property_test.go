package graph

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// genGraph generates a deterministic random graph from rand.New(seed) for
// property-based testing.
func genGraph(seed int64, clusters, podsPerCluster, extraEdges int) *Graph {
	r := rand.New(rand.NewSource(seed))
	all := make([]GraphNode, 0, clusters*(1+podsPerCluster))
	clusterNames := make([]string, clusters)
	for i := range clusters {
		clusterNames[i] = fmt.Sprintf("cluster-%d", i)
		nodeID := K8sNodeID(clusterNames[i], "worker-0")
		all = append(all, &K8sNode{IDValue: nodeID, NameValue: "worker-0", LabelsValue: map[string]string{"cluster": clusterNames[i]}})
		for j := range podsPerCluster {
			id := PodID(clusterNames[i], fmt.Sprintf("uid-%d-%d", i, j))
			all = append(all, &PodNode{
				IDValue:   id,
				NameValue: fmt.Sprintf("pod-%d-%d", i, j),
				LabelsValue: map[string]string{
					"cluster":   clusterNames[i],
					"namespace": fmt.Sprintf("ns-%d", j%2),
					"node":      nodeID,
				},
			})
		}
	}

	edges := []*Edge{}
	pods := podsOnly(all)
	for _, p := range pods {
		nodeID := p.Labels()["node"]
		edges = append(edges, NewEdge(EdgeTypePodRunsOnNode, p.ID(), nodeID, map[string]string{}))
	}
	for i := 0; i < extraEdges && len(pods) >= 2; i++ {
		a := pods[r.Intn(len(pods))]
		b := pods[r.Intn(len(pods))]
		if a.ID() == b.ID() {
			continue
		}
		edges = append(edges, NewEdge(EdgeTypePodCallsPod, a.ID(), b.ID(), map[string]string{
			"client_cluster": a.Labels()["cluster"],
			"server_cluster": b.Labels()["cluster"],
		}))
	}
	return NewGraph(all, edges, time.Now())
}

func podsOnly(nodes []GraphNode) []*PodNode {
	out := []*PodNode{}
	for _, n := range nodes {
		if p, ok := n.(*PodNode); ok {
			out = append(out, p)
		}
	}
	return out
}

func TestProperty_EveryEdgeEndpointResolves(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		g := genGraph(seed, 3, 5, 12)
		for _, e := range g.Edges {
			_, srcOK := g.NodesByID[e.Source]
			_, tgtOK := g.NodesByID[e.Target]
			require.Truef(t, srcOK, "seed=%d: edge %s has unresolved source %s", seed, e.ID, e.Source)
			require.Truef(t, tgtOK, "seed=%d: edge %s has unresolved target %s", seed, e.ID, e.Target)
		}
	}
}

func TestProperty_FilteredSubsetUnfiltered(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		g := genGraph(seed, 3, 5, 12)
		full := Project(g, Scope{})
		filtered := Project(g, Scope{Clusters: map[string]struct{}{"cluster-0": {}}})
		fullIDs := map[string]bool{}
		for _, n := range full.Nodes {
			fullIDs[n.ID()] = true
		}
		for _, n := range filtered.Nodes {
			require.Truef(t, fullIDs[n.ID()], "seed=%d: filtered contains node %s not in unfiltered", seed, n.ID())
		}
	}
}

func TestProperty_TraversalDepthRespected(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		g := genGraph(seed, 3, 5, 12)
		var root string
		for id := range g.NodesByID {
			root = id
			break
		}
		for d := range 4 {
			v := Project(g, Scope{Root: root, Depth: d, Direction: DirectionBoth})
			dist := map[string]int{root: 0}
			frontier := []string{root}
			for hop := 0; hop < d && len(frontier) > 0; hop++ {
				next := []string{}
				for _, id := range frontier {
					for _, e := range g.Forward[id] {
						if _, seen := dist[e.Target]; !seen {
							dist[e.Target] = hop + 1
							next = append(next, e.Target)
						}
					}
					for _, e := range g.Reverse[id] {
						if _, seen := dist[e.Source]; !seen {
							dist[e.Source] = hop + 1
							next = append(next, e.Source)
						}
					}
				}
				frontier = next
			}
			for _, n := range v.Nodes {
				dd, ok := dist[n.ID()]
				assert.Truef(t, ok, "seed=%d depth=%d: node %s in view but not reachable from root", seed, d, n.ID())
				assert.LessOrEqualf(t, dd, d, "seed=%d depth=%d: node %s at distance %d > depth", seed, d, n.ID(), dd)
			}
		}
	}
}

func TestProperty_CrossClusterEdgesHaveDistinctClusterEndpoints(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		g := genGraph(seed, 3, 5, 12)
		for _, e := range g.Edges {
			if e.Type != EdgeTypePodCallsPod {
				continue
			}
			if e.Labels["client_cluster"] != e.Labels["server_cluster"] {
				assert.NotEmpty(t, e.Labels["client_cluster"])
				assert.NotEmpty(t, e.Labels["server_cluster"])
			}
		}
	}
}

func TestProperty_EdgeIDsUniquePerTuple(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		g := genGraph(seed, 3, 5, 12)
		ids := map[string]string{}
		for _, e := range g.Edges {
			tuple := string(e.Type) + "|" + e.Source + "|" + e.Target
			if existing, ok := ids[e.ID]; ok {
				require.Equalf(t, existing, tuple, "seed=%d: edge id %s shared by %q and %q", seed, e.ID, existing, tuple)
			}
			ids[e.ID] = tuple
		}
	}
}
