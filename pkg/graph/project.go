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
	// K8sNode admission is deferred: a K8s node carries no namespace label, so
	// under a namespace filter it is retained iff some in-scope pod is scheduled
	// on it (labels.node). We first resolve every other node — recording the
	// host-node ids of the pods that survived — then admit the K8s nodes that
	// host one of them. hostNodes is only needed under a namespace filter, so it
	// is left nil (and unpopulated) otherwise. See design.md D31.
	var deferred []GraphNode
	var hostNodes map[string]struct{}
	if len(scope.Namespaces) > 0 {
		hostNodes = map[string]struct{}{}
	}
	for id, n := range g.NodesByID {
		if reachable != nil {
			if _, ok := reachable[id]; !ok {
				continue
			}
		}
		t := n.Type()
		if t == NodeTypeK8sNode {
			deferred = append(deferred, n)
			continue
		}
		if !nodePassesFilters(n, scope) {
			continue
		}
		out[id] = n
		if hostNodes != nil && t == NodeTypePod {
			if hn := n.Labels()["node"]; hn != "" {
				hostNodes[hn] = struct{}{}
			}
		}
	}
	for _, n := range deferred {
		if !k8sNodePassesFilters(n, scope, hostNodes) {
			continue
		}
		out[n.ID()] = n
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
		// ExternalNode is cluster-unscoped (no namespace label) and only ever
		// enters a view as the re-added partner of a pod-calls-pod edge, so it
		// is exempt from the namespace match. K8sNode is also namespace-less but
		// is admitted separately by k8sNodePassesFilters (host-of-in-scope-pod
		// rule), so it never reaches this predicate. Every other node type must
		// match the requested namespace.
		switch n.Type() {
		case NodeTypeExternal:
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

// k8sNodePassesFilters decides whether a K8sNode is admitted to a view. K8s
// nodes carry no namespace label, so namespace scoping is expressed indirectly:
// under a namespace filter a node is kept iff hostNodes contains its id — i.e.
// some pod that survived the namespace filter is scheduled on it (labels.node).
// With no namespace filter, the node is kept (subject to cluster / name), so
// the full-topology view still lists every node. cluster and name filters apply
// exactly as for other node types (a node's own labels carry cluster and name).
func k8sNodePassesFilters(n GraphNode, scope Scope, hostNodes map[string]struct{}) bool {
	labels := n.Labels()
	if len(scope.Clusters) > 0 {
		if _, ok := scope.Clusters[labels["cluster"]]; !ok {
			return false
		}
	}
	if scope.NameFilterActive() {
		if _, ok := scope.Names[n.Name()]; !ok {
			return false
		}
	}
	if len(scope.Namespaces) > 0 {
		if _, ok := hostNodes[n.ID()]; !ok {
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
		case NodeTypeExternal:
			// pass-through; external endpoints carry no namespace.
		default:
			if _, ok := scope.Namespaces[labels["namespace"]]; !ok {
				return false
			}
		}
	}
	return true
}
