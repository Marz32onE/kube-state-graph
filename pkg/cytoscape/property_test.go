package cytoscape

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/pkg/graph"
)

// TestProperty_NoDanglingParent — for any serialised view, every emitted node's
// data.parent (when present) MUST reference an id that is also present in the
// same response. This includes the synthetic cluster AND storageclass group
// nodes. Guards the Cytoscape compound-nesting invariant against the new
// StorageClass grouping across randomised multi-cluster graphs.
func TestProperty_NoDanglingParent(t *testing.T) {
	// "" exercises the cluster > pvc fallback; the rest exercise grouping.
	storageClasses := []string{"", "gp2", "gp3", "io2"}
	scGroupsSeen := 0

	for seed := int64(1); seed <= 50; seed++ {
		r := rand.New(rand.NewSource(seed))
		clusters := 1 + r.Intn(3)
		var nodes []graph.GraphNode
		for c := range clusters {
			cl := fmt.Sprintf("cluster-%d", c)
			nodeID := graph.K8sNodeID(cl, "worker-0")
			nodes = append(nodes, &graph.K8sNode{IDValue: nodeID, NameValue: "worker-0", LabelsValue: map[string]string{"cluster": cl}})
			for p := range r.Intn(4) {
				labels := map[string]string{"cluster": cl, "namespace": fmt.Sprintf("ns-%d", p%2)}
				if r.Intn(2) == 0 {
					labels["node"] = nodeID // sometimes in scope, sometimes not
				}
				nodes = append(nodes, &graph.PodNode{IDValue: graph.PodID(cl, fmt.Sprintf("u-%d-%d", c, p)), NameValue: fmt.Sprintf("pod-%d-%d", c, p), LabelsValue: labels})
			}
			for v := range r.Intn(4) {
				nodes = append(nodes, &graph.PVCNode{
					IDValue:           graph.PVCID(cl, "ns-0", fmt.Sprintf("claim-%d-%d", c, v)),
					NameValue:         fmt.Sprintf("claim-%d-%d", c, v),
					LabelsValue:       map[string]string{"cluster": cl, "namespace": "ns-0"},
					StorageClassValue: storageClasses[r.Intn(len(storageClasses))],
				})
			}
			nodes = append(nodes, &graph.ServiceNode{IDValue: graph.ServiceID(cl, "ns-0", "svc"), NameValue: "svc", LabelsValue: map[string]string{"cluster": cl, "namespace": "ns-0"}})
		}
		nodes = append(nodes, &graph.ExternalNode{IDValue: graph.ExternalID("admin"), NameValue: "admin", LabelsValue: map[string]string{}})

		body := cy(t, nodes, nil)
		ids := make(map[string]struct{}, len(body.Elements.Nodes))
		for _, n := range body.Elements.Nodes {
			ids[n.Data.ID] = struct{}{}
			if n.Data.Type == nodeTypeStorageClass {
				scGroupsSeen++
			}
		}
		for _, n := range body.Elements.Nodes {
			if n.Data.Parent == "" {
				continue
			}
			_, ok := ids[n.Data.Parent]
			require.Truef(t, ok, "seed=%d: node %s has dangling parent %s", seed, n.Data.ID, n.Data.Parent)
		}
	}

	require.Positive(t, scGroupsSeen, "expected the randomised corpus to exercise storageclass group synthesis")
}
