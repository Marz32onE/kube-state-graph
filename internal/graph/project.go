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
//  3. Apply cluster / namespace / name filters to nodes.
//  4. Drop edges whose endpoints are no longer present.
func Project(g *Graph, scope Scope) View {
	if g == nil {
		return View{}
	}

	reachable := traverse(g, scope)
	nodes := filterNodes(g, scope, reachable)
	edges := filterEdges(g, scope, nodes, reachable)

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
	if scope.NameFilterActive() {
		if _, ok := scope.Names[n.Name()]; !ok {
			return false
		}
	}
	return true
}

func filterEdges(g *Graph, scope Scope, nodes map[string]GraphNode, reachable map[string]struct{}) []*Edge {
	out := make([]*Edge, 0, len(g.Edges))
	// Snapshot the in-scope set at entry. Re-adds during this pass MUST NOT
	// promote a re-added partner into a new in-scope anchor, otherwise name
	// or cluster anchors would cascade through the graph indefinitely.
	primary := make(map[string]struct{}, len(nodes))
	for id := range nodes {
		primary[id] = struct{}{}
	}
	for _, e := range g.Edges {
		if len(scope.EdgeTypes) > 0 {
			if _, ok := scope.EdgeTypes[e.Type]; !ok {
				continue
			}
		}
		_, srcOK := primary[e.Source]
		_, tgtOK := primary[e.Target]
		if srcOK && tgtOK {
			out = append(out, e)
			continue
		}
		if !srcOK && !tgtOK {
			continue
		}
		// Unified partner re-add: exactly one endpoint is in scope, re-add the
		// other from g.NodesByID provided it passes the non-cluster filters.
		// This single rule covers (a) cross-cluster pod-calls-pod partner
		// preservation, (b) non-pod endpoints incident on in-scope pods, and
		// (c) name-anchored views that need to render incident edges with
		// their partner endpoints. When traversal is active, the partner must
		// also lie within the reachable set so the depth bound is respected.
		if reachable != nil {
			missing := e.Target
			if !srcOK {
				missing = e.Source
			}
			if _, ok := reachable[missing]; !ok {
				continue
			}
		}
		if !readdEdgePartners(g, e, nodes, srcOK, tgtOK, scope, nodePassesNonClusterFilters) {
			continue
		}
		out = append(out, e)
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
	return true
}
