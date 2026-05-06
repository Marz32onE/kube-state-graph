package api

import (
	"github.com/marz32one/kube-state-graph/internal/graph"
)

// ----- Cytoscape.js shape ---------------------------------------------------

type cytoscapeBody struct {
	APIVersion    string         `json:"apiVersion"`
	Start         string         `json:"start"`
	End           string         `json:"end"`
	StartActual   string         `json:"start_actual"`
	EndActual     string         `json:"end_actual"`
	BucketSeconds int            `json:"bucket_seconds"`
	Clusters      []string       `json:"clusters"`
	Elements      cytoscapeElems `json:"elements"`
}

type cytoscapeElems struct {
	Nodes []cytoscapeNode `json:"nodes"`
	Edges []cytoscapeEdge `json:"edges"`
}

type cytoscapeNode struct {
	Data cytoscapeNodeData `json:"data"`
}

type cytoscapeNodeData struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels"`
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

const rfc3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func serialiseCytoscape(req graphRequest, g *graph.Graph, view graph.View) cytoscapeBody {
	body := cytoscapeBody{
		APIVersion:    APIVersion,
		Start:         req.start.UTC().Format(rfc3339Nano),
		End:           req.end.UTC().Format(rfc3339Nano),
		StartActual:   req.window.StartActual.UTC().Format(rfc3339Nano),
		EndActual:     req.window.EndActual.UTC().Format(rfc3339Nano),
		BucketSeconds: req.window.BucketSeconds,
		Clusters:      g.ClusterNames(),
	}
	body.Elements.Nodes = make([]cytoscapeNode, 0, len(view.Nodes))
	for _, n := range view.Nodes {
		body.Elements.Nodes = append(body.Elements.Nodes, cytoscapeNode{
			Data: cytoscapeNodeData{
				ID:     n.ID(),
				Name:   n.Name(),
				Type:   string(n.Type()),
				Labels: n.Labels(),
			},
		})
	}
	body.Elements.Edges = make([]cytoscapeEdge, 0, len(view.Edges))
	for _, e := range view.Edges {
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
