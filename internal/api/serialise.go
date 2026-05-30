package api

import (
	"maps"
	"slices"

	"github.com/marz32one/kube-state-graph/internal/graph"
)

// ----- Cytoscape.js shape ---------------------------------------------------

type cytoscapeBody struct {
	APIVersion string         `json:"apiVersion"`
	Clusters   []string       `json:"clusters"`
	Elements   cytoscapeElems `json:"elements"`
}

type cytoscapeElems struct {
	Nodes []cytoscapeNode `json:"nodes"`
	Edges []cytoscapeEdge `json:"edges"`
}

type cytoscapeNode struct {
	Data cytoscapeNodeData `json:"data"`
}

type cytoscapeNodeData struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Parent    string            `json:"parent,omitempty"`
	IPAddress []string          `json:"ipaddress,omitempty"`
	Labels    map[string]string `json:"labels"`
}

type cytoscapeEdge struct {
	Data cytoscapeEdgeData `json:"data"`
}

type cytoscapeEdgeData struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Source string            `json:"source"`
	Target string            `json:"target"`
	Labels map[string]string `json:"labels"`
}

// nodeTypeCluster is the synthetic node type used for Cytoscape compound
// grouping. Cluster group nodes exist only in the Cytoscape presentation (to
// satisfy data.parent references); they are not graph.GraphNodes and never
// appear in the Grafana Node Graph output. See design.md D31.
const nodeTypeCluster = "cluster"

// clusterParentID is the synthetic group-node id for a cluster.
func clusterParentID(cluster string) string { return "cluster/" + cluster }

func serialiseCytoscape(g *graph.Graph, view graph.View) cytoscapeBody {
	body := cytoscapeBody{
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

	body.Elements.Nodes = make([]cytoscapeNode, 0, len(view.Nodes)+len(clusterSeen))

	// Synthetic cluster group nodes first, sorted by name (determinism, D6).
	for _, c := range slices.Sorted(maps.Keys(clusterSeen)) {
		body.Elements.Nodes = append(body.Elements.Nodes, cytoscapeNode{
			Data: cytoscapeNodeData{
				ID:     clusterParentID(c),
				Name:   c,
				Type:   nodeTypeCluster,
				Labels: map[string]string{},
			},
		})
	}

	for _, n := range view.Nodes {
		body.Elements.Nodes = append(body.Elements.Nodes, cytoscapeNode{
			Data: cytoscapeNodeData{
				ID:        n.ID(),
				Name:      n.Name(),
				Type:      string(n.Type()),
				Parent:    compoundParent(n, present),
				IPAddress: n.IPAddress(),
				Labels:    n.Labels(),
			},
		})
	}

	body.Elements.Edges = make([]cytoscapeEdge, 0, len(view.Edges))
	for _, e := range view.Edges {
		// pod-runs-on-node is expressed by compound nesting (cluster > node >
		// pod), so the redundant edge is omitted from the Cytoscape view. It is
		// retained in the core graph, in projection / traversal, and in the
		// Grafana Node Graph output (which cannot nest). See design.md D31.
		if e.Type == graph.EdgeTypePodRunsOnNode {
			continue
		}
		body.Elements.Edges = append(body.Elements.Edges, cytoscapeEdge{
			Data: cytoscapeEdgeData{
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
//	others/external  → "" (no cluster identity)
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
	// under their cluster group. others / external carry no cluster label
	// (labels={}), so they fall through to no parent.
	if c := labels["cluster"]; c != "" {
		return clusterParentID(c)
	}
	return ""
}

// ----- Grafana Node Graph shape ---------------------------------------------

type grafanaBody struct {
	APIVersion  string         `json:"apiVersion"`
	NodesFields []grafanaField `json:"nodes_fields"`
	Nodes       []grafanaRow   `json:"nodes"`
	EdgesFields []grafanaField `json:"edges_fields"`
	Edges       []grafanaRow   `json:"edges"`
}

type grafanaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type grafanaRow map[string]any

func serialiseGrafanaNodeGraph(view graph.View) grafanaBody {
	out := grafanaBody{
		APIVersion: APIVersion,
		NodesFields: []grafanaField{
			{Name: "id", Type: "string"},
			{Name: "title", Type: "string"},
			{Name: "subTitle", Type: "string"},
			{Name: "mainStat", Type: "string"},
		},
		EdgesFields: []grafanaField{
			{Name: "id", Type: "string"},
			{Name: "source", Type: "string"},
			{Name: "target", Type: "string"},
			{Name: "mainStat", Type: "string"},
		},
	}
	for _, n := range view.Nodes {
		labels := n.Labels()
		sub := labels["cluster"]
		if ns := labels["namespace"]; ns != "" {
			sub = sub + " · " + ns
		}
		out.Nodes = append(out.Nodes, grafanaRow{
			"id":       n.ID(),
			"title":    n.Name(),
			"subTitle": sub,
			"mainStat": string(n.Type()),
		})
	}
	for _, e := range view.Edges {
		out.Edges = append(out.Edges, grafanaRow{
			"id":       e.ID,
			"source":   e.Source,
			"target":   e.Target,
			"mainStat": string(e.Type),
		})
	}
	return out
}
