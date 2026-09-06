package build

import "github.com/akira-core/kube-state-graph/pkg/graph"

// attachStatus bakes the node verdict after alerts have been attached and
// before graph.NewGraph freezes the node set. It deliberately folds only each
// node's alerts, health and Ready status; raw performance counters do not
// participate.
func attachStatus(nodes []graph.GraphNode) {
	for _, n := range nodes {
		status := graph.FoldStatus(n.Alerts(), n.Health(), n.ReadyStatus())
		switch node := n.(type) {
		case *graph.PodNode:
			node.StatusValue = status
		case *graph.K8sNode:
			node.StatusValue = status
		case *graph.PVCNode:
			node.StatusValue = status
		case *graph.NetAppNode:
			node.StatusValue = status
		case *graph.NetAppAggrNode:
			node.StatusValue = status
		}
	}
}
