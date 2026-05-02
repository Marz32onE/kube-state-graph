package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marz32one/kube-state-graph/internal/cache"
	"github.com/marz32one/kube-state-graph/internal/graph"
)

var update = flag.Bool("update", false, "update golden files")

// TestGolden_GraphResponses snapshots the Cytoscape and Grafana Node Graph
// responses for several canned scenarios so contract drift is caught on PR.
func TestGolden_GraphResponses(t *testing.T) {
	scenarios := map[string]graph.View{
		"single-cluster":     buildSingleCluster(),
		"two-cluster-cross":  buildTwoClusterCross(),
		"with-external":      buildWithExternal(),
	}

	for name, view := range scenarios {
		t.Run(name+"-cytoscape", func(t *testing.T) {
			req := graphRequest{
				start: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
				end:   time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC),
				bucket: cache.Bucketing{
					BucketSeconds: 15,
					StartActual:   time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
					EndActual:     time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC),
				},
			}
			g := &graph.Graph{BuiltAt: time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC), NodesByID: map[string]graph.GraphNode{}}
			for _, n := range view.Nodes {
				g.NodesByID[n.ID()] = n
			}
			body := serialiseCytoscape(req, g, view)
			compareGolden(t, name+"-cytoscape.json", body)
		})
		t.Run(name+"-nodegraph", func(t *testing.T) {
			body := serialiseGrafanaNodeGraph(view)
			compareGolden(t, name+"-nodegraph.json", body)
		})
	}
}

func TestGolden_EdgeTypes(t *testing.T) {
	body := map[string]any{
		"apiVersion": APIVersion,
		"edge_types": graph.EdgeTypes,
	}
	compareGolden(t, "edge-types.json", body)
}

func compareGolden(t *testing.T, file string, body any) {
	t.Helper()
	got, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", file)

	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s\n--- want\n%s\n--- got\n%s", file, want, got)
	}
}

// ----- canned scenarios -----------------------------------------------------

func buildSingleCluster() graph.View {
	pod := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop", "node": "cluster-alpha/worker-0"}}
	node := &graph.K8sNode{IDValue: "cluster-alpha/worker-0", NameValue: "worker-0", LabelsValue: map[string]string{"cluster": "cluster-alpha"}}
	edge := graph.NewEdge(graph.EdgeTypePodRunsOnNode, pod.IDValue, node.IDValue, map[string]string{})
	return graph.View{
		Nodes: []graph.GraphNode{node, pod},
		Edges: []*graph.Edge{edge},
	}
}

func buildTwoClusterCross() graph.View {
	a := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	b := &graph.PodNode{IDValue: "cluster-beta/p2", NameValue: "payments", LabelsValue: map[string]string{"cluster": "cluster-beta", "namespace": "billing"}}
	cross := graph.NewEdge(graph.EdgeTypePodCallsPod, a.IDValue, b.IDValue, map[string]string{
		"client_cluster": "cluster-alpha",
		"server_cluster": "cluster-beta",
	})
	return graph.View{Nodes: []graph.GraphNode{a, b}, Edges: []*graph.Edge{cross}}
}

func buildWithExternal() graph.View {
	pod := &graph.PodNode{IDValue: "cluster-alpha/p1", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}}
	ext := &graph.ExternalNode{IDValue: "external/http://api.example.com", NameValue: "http://api.example.com", LabelsValue: map[string]string{"pattern": "://"}}
	edge := graph.NewEdge(graph.EdgeTypePodCallsPod, pod.IDValue, ext.IDValue, map[string]string{
		"client_cluster": "cluster-alpha",
		"server_cluster": "",
	})
	return graph.View{Nodes: []graph.GraphNode{pod, ext}, Edges: []*graph.Edge{edge}}
}
