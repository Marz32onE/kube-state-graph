package graph

// View is a read-only projection of a Graph after a Scope has been applied.
// It holds slices of pointers into the underlying Graph; callers MUST NOT
// mutate the returned slices' elements.
type View struct {
	Nodes []GraphNode
	Edges []*Edge
}

// Project returns a View of g constrained by scope. It does not mutate g.
//
// Order of operations:
//  1. If scope.Root is set, run a bounded BFS to determine the reachable
//     node set; otherwise consider all nodes.
//  2. Apply edge-type filter to edges among the reachable set.
//  3. Apply cluster / namespace / node filters to nodes.
//  4. Drop edges whose endpoints are no longer present.
func Project(g *Graph, scope Scope) View {
	if g == nil {
		return View{}
	}

	reachable := traverse(g, scope)
	nodes := filterNodes(g, scope, reachable)
	edges := filterEdges(g, scope, nodes)

	out := View{
		Nodes: make([]GraphNode, 0, len(nodes)),
		Edges: edges,
	}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, n)
	}
	SortNodes(out.Nodes)
	SortEdges(out.Edges)
	return out
}

func traverse(g *Graph, scope Scope) map[string]struct{} {
	if scope.Root == "" {
		return nil // sentinel: no traversal restriction
	}
	if _, ok := g.NodesByID[scope.Root]; !ok {
		return map[string]struct{}{} // empty: unknown root
	}

	visited := map[string]struct{}{scope.Root: {}}
	frontier := []string{scope.Root}
	for depth := 0; depth < scope.Depth && len(frontier) > 0; depth++ {
		next := make([]string, 0, len(frontier))
		for _, id := range frontier {
			if scope.Direction == DirectionOut || scope.Direction == DirectionBoth {
				for _, e := range g.Forward[id] {
					if _, seen := visited[e.Target]; !seen {
						visited[e.Target] = struct{}{}
						next = append(next, e.Target)
					}
				}
			}
			if scope.Direction == DirectionIn || scope.Direction == DirectionBoth {
				for _, e := range g.Reverse[id] {
					if _, seen := visited[e.Source]; !seen {
						visited[e.Source] = struct{}{}
						next = append(next, e.Source)
					}
				}
			}
		}
		frontier = next
	}
	return visited
}

func filterNodes(g *Graph, scope Scope, reachable map[string]struct{}) map[string]GraphNode {
	out := make(map[string]GraphNode, len(g.NodesByID))
	for id, n := range g.NodesByID {
		if reachable != nil {
			if _, ok := reachable[id]; !ok {
				continue
			}
		}
		if !nodePassesFilters(n, scope) {
			continue
		}
		out[id] = n
	}
	return out
}

func nodePassesFilters(n GraphNode, scope Scope) bool {
	labels := n.Labels()
	if len(scope.Clusters) > 0 {
		if n.Type() == NodeTypeExternal {
			// External nodes have no cluster; exclude when caller scoped to clusters.
			return false
		}
		if _, ok := scope.Clusters[labels["cluster"]]; !ok {
			return false
		}
	}
	if len(scope.Namespaces) > 0 {
		// K8sNode and ExternalNode are cluster-scoped; they have no namespace
		// label. Excluding them here would drop pod-runs-on-node edges whenever
		// a caller narrows by namespace, which defeats the purpose of the view.
		switch n.Type() {
		case NodeTypeK8sNode, NodeTypeExternal:
			// pass-through
		default:
			if _, ok := scope.Namespaces[labels["namespace"]]; !ok {
				return false
			}
		}
	}
	if len(scope.Nodes) > 0 {
		switch n.Type() {
		case NodeTypeK8sNode:
			if _, ok := scope.Nodes[n.Name()]; !ok {
				return false
			}
		case NodeTypePod:
			// Pods carry their cluster-scoped node ID; match by either suffix
			// (raw node name) or full ID.
			matched := false
			if _, ok := scope.Nodes[labels["node"]]; ok {
				matched = true
			} else {
				// Allow plain node-name match (strip cluster prefix).
				if name := stripClusterPrefix(labels["node"]); name != "" {
					if _, ok := scope.Nodes[name]; ok {
						matched = true
					}
				}
			}
			if !matched {
				return false
			}
		default:
			return false
		}
	}
	if scope.PodFilterActive() {
		if n.Type() != NodeTypePod {
			// Non-pod node types survive only as edge endpoints of in-scope pods,
			// re-added during filterEdges. Drop here in the primary pass.
			return false
		}
		if !podMatches(n, scope) {
			return false
		}
	}
	return true
}

func podMatches(n GraphNode, scope Scope) bool {
	if len(scope.Pods) > 0 {
		if _, ok := scope.Pods[n.Name()]; !ok {
			return false
		}
	}
	if len(scope.PodUIDs) > 0 {
		uid := stripClusterPrefix(n.ID())
		if uid == "" {
			return false
		}
		if _, ok := scope.PodUIDs[uid]; !ok {
			return false
		}
	}
	return true
}

func stripClusterPrefix(id string) string {
	for i := range len(id) {
		if id[i] == '/' {
			return id[i+1:]
		}
	}
	return ""
}

func filterEdges(g *Graph, scope Scope, nodes map[string]GraphNode) []*Edge {
	out := make([]*Edge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if len(scope.EdgeTypes) > 0 {
			if _, ok := scope.EdgeTypes[e.Type]; !ok {
				continue
			}
		}
		_, srcOK := nodes[e.Source]
		_, tgtOK := nodes[e.Target]
		if srcOK && tgtOK {
			out = append(out, e)
			continue
		}
		// Cross-cluster pod-calls-pod preservation. When a client narrows by
		// cluster (and not by pod / pod_uid), a cross-cluster service-graph
		// edge whose other endpoint sits in an out-of-scope cluster MUST still
		// resolve (graph-api spec § "Cross-cluster edge representation"). When
		// a pod-side filter IS set the caller has named the exact pod set, so
		// partner re-hydration is suppressed.
		if preserveCrossClusterEdge(g, e, scope, srcOK, tgtOK) {
			if !readdEdgePartners(g, e, nodes, srcOK, tgtOK, scope, nodePassesNonClusterFilters) {
				continue
			}
			out = append(out, e)
			continue
		}
		// Pod-filter partner re-add: non-pod endpoints (K8sNode / PVC /
		// External) get re-added when the other end is an in-scope pod, so
		// pod-runs-on-node, pod-mounts-pvc, and external pod-calls-* edges
		// remain visible. Pod-pod edges where one pod is out of scope are
		// dropped (caller named the pod set explicitly).
		if scope.PodFilterActive() && preservePodFilterPartner(g, e, nodes, srcOK, tgtOK) {
			if !readdEdgePartners(g, e, nodes, srcOK, tgtOK, scope, nodePassesNonClusterFilters) {
				continue
			}
			out = append(out, e)
			continue
		}
	}
	return out
}

// readdEdgePartners brings in the missing endpoint(s) of e via g.NodesByID,
// gated by pred (must accept the partner under scope). Returns false if any
// missing endpoint cannot be re-added.
func readdEdgePartners(
	g *Graph,
	e *Edge,
	nodes map[string]GraphNode,
	srcOK, tgtOK bool,
	scope Scope,
	pred func(GraphNode, Scope) bool,
) bool {
	if !srcOK {
		partner, ok := g.NodesByID[e.Source]
		if !ok || !pred(partner, scope) {
			return false
		}
		nodes[e.Source] = partner
	}
	if !tgtOK {
		partner, ok := g.NodesByID[e.Target]
		if !ok || !pred(partner, scope) {
			return false
		}
		nodes[e.Target] = partner
	}
	return true
}

func preserveCrossClusterEdge(g *Graph, e *Edge, scope Scope, srcOK, tgtOK bool) bool {
	if e.Type != EdgeTypePodCallsPod {
		return false
	}
	if len(scope.Clusters) == 0 {
		return false
	}
	if scope.PodFilterActive() {
		// Caller named the exact pod set; do not re-hydrate the partner pod.
		return false
	}
	if !srcOK && !tgtOK {
		return false
	}
	// Cross-cluster status is derived from the resolved endpoints' cluster
	// labels (the edge only carries the trace-source / client-side cluster).
	return g.isCrossCluster(e)
}

// preservePodFilterPartner reports whether the missing endpoint of e is a
// non-pod (K8sNode / PVC / External) that should be re-added because the
// other endpoint is an in-scope pod. Pod-pod edges with one pod out of scope
// are dropped — caller's pod set is exact.
func preservePodFilterPartner(g *Graph, e *Edge, nodes map[string]GraphNode, srcOK, tgtOK bool) bool {
	if srcOK == tgtOK {
		// Both missing or both present; latter handled earlier.
		return false
	}
	missingID := e.Target
	presentID := e.Source
	if !srcOK {
		missingID = e.Source
		presentID = e.Target
	}
	present, ok := nodes[presentID]
	if !ok || present.Type() != NodeTypePod {
		return false
	}
	missing, ok := g.NodesByID[missingID]
	if !ok {
		return false
	}
	switch missing.Type() {
	case NodeTypeK8sNode, NodeTypePVC, NodeTypeExternal:
		return true
	default:
		return false
	}
}

func nodePassesNonClusterFilters(n GraphNode, scope Scope) bool {
	labels := n.Labels()
	if len(scope.Namespaces) > 0 {
		switch n.Type() {
		case NodeTypeK8sNode, NodeTypeExternal:
			// pass-through; cluster-scoped entities carry no namespace.
		default:
			if _, ok := scope.Namespaces[labels["namespace"]]; !ok {
				return false
			}
		}
	}
	if len(scope.Nodes) > 0 {
		switch n.Type() {
		case NodeTypeK8sNode:
			if _, ok := scope.Nodes[n.Name()]; !ok {
				return false
			}
		case NodeTypePod:
			if _, ok := scope.Nodes[labels["node"]]; ok {
				return true
			}
			if name := stripClusterPrefix(labels["node"]); name != "" {
				if _, ok := scope.Nodes[name]; ok {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return true
}
