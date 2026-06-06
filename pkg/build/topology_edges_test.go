package build

import (
	"testing"

	"github.com/marz32one/kube-state-graph/pkg/graph"
)

func TestTopologyEdges_PVCMountWithBinding(t *testing.T) {
	t.Parallel()
	pvc := &graph.PVCNode{
		IDValue:     "test/default/data-1",
		NameValue:   "data-1",
		LabelsValue: map[string]string{},
	}
	pod := &graph.PodNode{
		IDValue:     "test/uid-3",
		NameValue:   "db",
		LabelsValue: map[string]string{"node": "test/node-a"},
	}
	top := Topology{
		Pods: []*graph.PodNode{pod},
		PVCs: []*graph.PVCNode{pvc},
		PodPVCs: []PodPVCBinding{
			{PodID: pod.ID(), PVCID: pvc.ID()},
		},
	}

	edges := TopologyEdges(top)
	if len(edges) != 1 {
		t.Fatalf("len(edges)=%d, want 1", len(edges))
	}
	var pvcEdge *graph.Edge
	for _, e := range edges {
		if e.Type == graph.EdgeTypePodMountsPVC {
			pvcEdge = e
		}
	}
	if pvcEdge == nil {
		t.Fatalf("missing pod-mounts-pvc edge")
	}
	if pvcEdge.Source != pod.ID() || pvcEdge.Target != pvc.ID() {
		t.Errorf("pvc edge endpoints src=%q tgt=%q", pvcEdge.Source, pvcEdge.Target)
	}
	if pvcEdge.Labels["claim_name"] != "data-1" {
		t.Errorf("claim_name label=%q want data-1", pvcEdge.Labels["claim_name"])
	}
}

func TestTopologyEdges_SkipsBindingForMissingPVC(t *testing.T) {
	t.Parallel()
	pod := &graph.PodNode{
		IDValue:     "test/uid-4",
		NameValue:   "ghost",
		LabelsValue: map[string]string{"node": "test/node-a"},
	}
	top := Topology{
		Pods: []*graph.PodNode{pod},
		PodPVCs: []PodPVCBinding{
			{PodID: pod.ID(), PVCID: "test/default/missing"},
		},
	}

	edges := TopologyEdges(top)
	for _, e := range edges {
		if e.Type == graph.EdgeTypePodMountsPVC {
			t.Fatalf("unexpected pvc edge for missing PVC: %+v", e)
		}
	}
}
