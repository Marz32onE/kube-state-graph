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
		if _, ok := scope.Namespaces[labels["namespace"]]; !ok {
			return false
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
			if id, ok := scope.Nodes[labels["node"]]; ok {
				_ = id
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
	return true
}

func stripClusterPrefix(id string) string {
	for i := 0; i < len(id); i++ {
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
		if _, srcOK := nodes[e.Source]; !srcOK {
			continue
		}
		if _, tgtOK := nodes[e.Target]; !tgtOK {
			continue
		}
		out = append(out, e)
	}
	return out
}
