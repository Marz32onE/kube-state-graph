package build

import "github.com/marz32one/kube-state-graph/internal/graph"

// TopologyEdges synthesises pod-runs-on-node and pod-mounts-pvc-on-node edges
// from a parsed Topology. Edge IDs are deterministic UUIDv5 — see graph.NewEdge.
func TopologyEdges(t Topology) []*graph.Edge {
	edges := make([]*graph.Edge, 0, len(t.Pods)+len(t.PodPVCs))

	for _, p := range t.Pods {
		nodeID := p.Labels()["node"]
		if nodeID == "" {
			continue
		}
		edges = append(edges, graph.NewEdge(
			graph.EdgeTypePodRunsOnNode,
			p.ID(),
			nodeID,
			map[string]string{},
		))
	}

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
			graph.EdgeTypePodMountsPVCOnNode,
			b.PodID,
			b.PVCID,
			labels,
		))
	}
	return edges
}
