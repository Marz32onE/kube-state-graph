package build

import "github.com/marz32one/kube-state-graph/pkg/graph"

// TopologyEdges synthesises pod-mounts-pvc edges from a parsed Topology.
// Edge IDs are deterministic UUIDv5 — see graph.NewEdge. The pod→node
// relationship is carried by each pod's labels.node (rendered as Cytoscape
// compound nesting), not by an edge — see design.md D31.
func TopologyEdges(t Topology) []*graph.Edge {
	edges := make([]*graph.Edge, 0, len(t.PodPVCs))

	pvcByID := map[string]*graph.PVCNode{}
	for _, pv := range t.PVCs {
		pvcByID[pv.ID()] = pv
	}
	for _, b := range t.PodPVCs {
		if _, ok := pvcByID[b.PVCID]; !ok {
			continue
		}
		labels := map[string]string{}
		// Best-effort claim name from PVC labels.
		if pv, ok := pvcByID[b.PVCID]; ok {
			labels["claim_name"] = pv.Name()
		}
		edges = append(edges, graph.NewEdge(
			graph.EdgeTypePodMountsPVC,
			b.PodID,
			b.PVCID,
			labels,
		))
	}
	return edges
}
