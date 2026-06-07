package cytoscape

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/pkg/graph"
)

// cluster > storageclass > pvc: a PVC with a resolved StorageClass nests under a
// synthetic storageclass group node, which itself nests under the cluster group.
func TestSerialiseCytoscape_StorageClassNesting(t *testing.T) {
	pvc := &graph.PVCNode{
		IDValue:           "c1/db/data-mongo-0",
		NameValue:         "data-mongo-0",
		LabelsValue:       map[string]string{"cluster": "c1", "namespace": "db"},
		StorageClassValue: "gp3",
	}
	nodes := cyNodesByID(cy(t, []graph.GraphNode{pvc}, nil))

	grp, ok := nodes["c1/storageclass/gp3"]
	require.True(t, ok, "storageclass group node must be synthesised")
	assert.Equal(t, "storageclass", grp.Type)
	assert.Equal(t, "gp3", grp.Name)
	assert.Equal(t, "cluster/c1", grp.Parent, "storageclass group nests under its cluster group")
	assert.Empty(t, grp.Labels, "storageclass group carries no labels")

	assert.Equal(t, "c1/storageclass/gp3", nodes["c1/db/data-mongo-0"].Parent,
		"pvc nests under its storageclass group (cluster > storageclass > pvc)")

	// StorageClass must NOT leak onto the PVC entry as a label. (NodeData has no
	// storageclass field at all, so data.storageclass is structurally impossible.)
	_, hasLabel := nodes["c1/db/data-mongo-0"].Labels["storageclass"]
	assert.False(t, hasLabel, "storageclass must not appear in the PVC's labels")
}

// PVC without a resolved StorageClass falls back to cluster > pvc; no
// storageclass group node is synthesised on its behalf.
func TestSerialiseCytoscape_StorageClassFallback(t *testing.T) {
	pvc := &graph.PVCNode{
		IDValue:     "c1/db/data-redis-0",
		NameValue:   "data-redis-0",
		LabelsValue: map[string]string{"cluster": "c1", "namespace": "db"},
		// no StorageClassValue
	}
	nodes := cyNodesByID(cy(t, []graph.GraphNode{pvc}, nil))

	for id := range nodes {
		assert.NotContains(t, id, "/storageclass/", "no storageclass group expected for a class-less PVC")
	}
	assert.Equal(t, "cluster/c1", nodes["c1/db/data-redis-0"].Parent, "pvc falls back to its cluster group")
}

// The storageclass group's labels must serialise as the empty object `{}` (like
// the cluster group), not null — so it carries a non-nil empty map.
func TestSerialiseCytoscape_StorageClassGroupLabelsEmptyObject(t *testing.T) {
	pvc := &graph.PVCNode{IDValue: "c1/db/x", NameValue: "x", LabelsValue: map[string]string{"cluster": "c1", "namespace": "db"}, StorageClassValue: "gp3"}
	grp := cyNodesByID(cy(t, []graph.GraphNode{pvc}, nil))["c1/storageclass/gp3"]
	require.NotNil(t, grp.Labels, "labels must be a non-nil empty map so it serialises as {}")
	assert.Empty(t, grp.Labels)
}

// Determinism: storageclass group nodes are emitted after the cluster groups and
// before the real nodes, ordered by (cluster, storageclass) (D6).
func TestSerialiseCytoscape_StorageClassGroupsSortedAfterClusters(t *testing.T) {
	pvcs := []graph.GraphNode{
		&graph.PVCNode{IDValue: "c-b/n/p1", NameValue: "p1", LabelsValue: map[string]string{"cluster": "c-b", "namespace": "n"}, StorageClassValue: "gp3"},
		&graph.PVCNode{IDValue: "c-a/n/p2", NameValue: "p2", LabelsValue: map[string]string{"cluster": "c-a", "namespace": "n"}, StorageClassValue: "gp3"},
		&graph.PVCNode{IDValue: "c-a/n/p3", NameValue: "p3", LabelsValue: map[string]string{"cluster": "c-a", "namespace": "n"}, StorageClassValue: "gp2"},
	}
	body := cy(t, pvcs, nil)

	var order []string
	for _, n := range body.Elements.Nodes {
		if n.Data.Type != "cluster" && n.Data.Type != "storageclass" {
			break // real nodes start; all group nodes are contiguous up front
		}
		order = append(order, n.Data.ID)
	}
	assert.Equal(t, []string{
		"cluster/c-a", "cluster/c-b", // cluster groups first, sorted by name
		"c-a/storageclass/gp2", "c-a/storageclass/gp3", "c-b/storageclass/gp3", // then sc groups by (cluster, sc)
	}, order)
}
