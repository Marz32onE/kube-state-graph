// Package cytoscape serialises a built graph.Graph (projected to a graph.View)
// into the deterministic Cytoscape.js response body served at /v1/graph. It is
// part of the reusable graph engine: an embedding application calls Serialise
// to obtain the exact same wire shape kube-state-graph emits, with no HTTP or
// JSON round-trip. The serialiser is presentation-only — it adds synthetic
// cluster group nodes and data.parent compound nesting (design.md D31) without
// touching the core graph types.
package cytoscape

import (
	"maps"
	"slices"

	"github.com/marz32one/kube-state-graph/pkg/graph"
)

// APIVersion is the value stamped on the body's apiVersion field (design.md D14).
const APIVersion = "v1"

// ----- Cytoscape.js shape ---------------------------------------------------

// Body is the top-level /v1/graph response envelope.
type Body struct {
	APIVersion string   `json:"apiVersion"`
	Clusters   []string `json:"clusters"`
	Elements   Elements `json:"elements"`
}

// Elements holds the node and edge collections.
type Elements struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Node wraps a node's data in the Cytoscape `{ "data": {...} }` shape.
type Node struct {
	Data NodeData `json:"data"`
}

// NodeData is the serialised form of a graph node (plus synthetic cluster
// group nodes and the presentation-only parent field).
type NodeData struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Parent    string            `json:"parent,omitempty"`
	IPAddress []string          `json:"ipaddress,omitempty"`
	Labels    map[string]string `json:"labels"`
}

// Edge wraps an edge's data in the Cytoscape `{ "data": {...} }` shape.
type Edge struct {
	Data EdgeData `json:"data"`
}

// EdgeData is the serialised form of a graph edge.
type EdgeData struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Source string            `json:"source"`
	Target string            `json:"target"`
	Labels map[string]string `json:"labels"`
}

// nodeTypeCluster is the synthetic node type used for Cytoscape compound
// grouping. Cluster group nodes exist only in the Cytoscape presentation (to
// satisfy data.parent references); they are not graph.GraphNodes. See
// design.md D31.
const nodeTypeCluster = "cluster"

// clusterParentID is the synthetic group-node id for a cluster.
func clusterParentID(cluster string) string { return "cluster/" + cluster }

// Serialise renders a projected view into the deterministic Cytoscape body.
// g supplies only ClusterNames(); view supplies the in-scope nodes and edges.
func Serialise(g *graph.Graph, view graph.View) Body {
	body := Body{
		APIVersion: APIVersion,
		Clusters:   g.ClusterNames(),
	}

	// Index emitted node ids so a pod's parent (its K8s node) is referenced
	// only when that node is actually present in elements.nodes — a data.parent
	// pointing at an absent node would dangle in Cytoscape. Collect the
	// distinct clusters to synthesise group nodes for.
	present := make(map[string]struct{}, len(view.Nodes))
	clusterSeen := map[string]struct{}{}
	for _, n := range view.Nodes {
		present[n.ID()] = struct{}{}
		if c := n.Labels()["cluster"]; c != "" {
			clusterSeen[c] = struct{}{}
		}
	}

	body.Elements.Nodes = make([]Node, 0, len(view.Nodes)+len(clusterSeen))

	// Synthetic cluster group nodes first, sorted by name (determinism, D6).
	for _, c := range slices.Sorted(maps.Keys(clusterSeen)) {
		body.Elements.Nodes = append(body.Elements.Nodes, Node{
			Data: NodeData{
				ID:     clusterParentID(c),
				Name:   c,
				Type:   nodeTypeCluster,
				Labels: map[string]string{},
			},
		})
	}

	for _, n := range view.Nodes {
		body.Elements.Nodes = append(body.Elements.Nodes, Node{
			Data: NodeData{
				ID:        n.ID(),
				Name:      n.Name(),
				Type:      string(n.Type()),
				Parent:    compoundParent(n, present),
				IPAddress: n.IPAddress(),
				Labels:    n.Labels(),
			},
		})
	}

	body.Elements.Edges = make([]Edge, 0, len(view.Edges))
	for _, e := range view.Edges {
		body.Elements.Edges = append(body.Elements.Edges, Edge{
			Data: EdgeData{
				ID:     e.ID,
				Type:   string(e.Type),
				Source: e.Source,
				Target: e.Target,
				Labels: e.Labels,
			},
		})
	}
	return body
}

// compoundParent returns the Cytoscape data.parent for a node, per design D31:
//
//	pod              → its K8s node (labels.node) when present in the view,
//	                   else its cluster group, else "" (unknown cluster)
//	node/service/pvc → its cluster group (cluster/<cluster>)
//	external         → "" (no cluster identity)
func compoundParent(n graph.GraphNode, present map[string]struct{}) string {
	labels := n.Labels()
	// A pod nests under its scheduling K8s node (labels.node) when that node is
	// present in the view; otherwise it falls through to its cluster group.
	if n.Type() == graph.NodeTypePod {
		if node := labels["node"]; node != "" {
			if _, ok := present[node]; ok {
				return node
			}
		}
	}
	// node / service / pvc — and any pod whose node is out of scope — nest
	// under their cluster group. external nodes carry no cluster label
	// (labels={}), so they fall through to no parent.
	if c := labels["cluster"]; c != "" {
		return clusterParentID(c)
	}
	return ""
}
