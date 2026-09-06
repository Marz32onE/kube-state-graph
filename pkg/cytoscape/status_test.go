package cytoscape

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akira-core/kube-state-graph/pkg/graph"
)

func TestSerialise_StatusAttributeKinds(t *testing.T) {
	nodes := []graph.GraphNode{
		&graph.PodNode{
			IDValue:     "c1/uid-1",
			NameValue:   "orders-0",
			LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop"},
			StatusValue: graph.StatusNormal,
		},
		&graph.ServiceNode{
			IDValue:     "c1/shop/orders",
			NameValue:   "orders",
			LabelsValue: map[string]string{"cluster": "c1", "namespace": "shop"},
		},
		&graph.ExternalNode{IDValue: "external/example", NameValue: "example", LabelsValue: map[string]string{}},
		&graph.NetAppSVMNode{
			IDValue:     graph.NetAppSVMID("ontap-prod", "svm_shop"),
			NameValue:   "svm_shop",
			LabelsValue: map[string]string{"ontap_cluster": "ontap-prod"},
		},
	}

	body := cy(t, nodes, nil)
	byID := cyNodesByID(body)
	assert.Equal(t, graph.StatusNormal, byID["c1/uid-1"].Status)
	for _, id := range []string{"c1/shop/orders", "external/example", graph.NetAppSVMID("ontap-prod", "svm_shop")} {
		assert.Empty(t, byID[id].Status, id)
	}

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	var decoded Body
	require.NoError(t, json.Unmarshal(raw, &decoded))
	for _, node := range decoded.Elements.Nodes {
		if node.Data.Type == nodeTypeCluster ||
			node.Data.Type == nodeTypeStorageCluster ||
			node.Data.Type == nodeTypeNamespace ||
			node.Data.Type == nodeTypeApplication ||
			node.Data.Type == nodeTypeController {
			nodeRaw, marshalErr := json.Marshal(node.Data)
			require.NoError(t, marshalErr)
			assert.NotContains(t, string(nodeRaw), `"status"`, node.Data.ID)
		}
	}
}
