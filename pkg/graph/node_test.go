package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStorageClass_OnlyPVCsCarryIt — the StorageClass() accessor returns the
// resolved value for a PVC and "" for every other node kind (and a class-less
// PVC). It is consumed only by the Cytoscape serialiser for compound grouping
// and is never a label or serialised attribute.
func TestStorageClass_OnlyPVCsCarryIt(t *testing.T) {
	pvc := &PVCNode{IDValue: "c/n/claim", NameValue: "claim", StorageClassValue: "gp3"}
	assert.Equal(t, "gp3", pvc.StorageClass())

	classless := &PVCNode{IDValue: "c/n/claim2", NameValue: "claim2"}
	assert.Empty(t, classless.StorageClass(), "PVC with no resolved StorageClass returns empty")

	others := []GraphNode{
		&PodNode{IDValue: "c/u"},
		&K8sNode{IDValue: "c/w"},
		&ServiceNode{IDValue: "c/n/s"},
		&ExternalNode{IDValue: "external/x"},
	}
	for _, n := range others {
		assert.Emptyf(t, n.StorageClass(), "%T must return empty StorageClass", n)
	}
}
