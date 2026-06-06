package graph

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

var uuidV5Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestNewEdge_StableAcrossRebuilds(t *testing.T) {
	a := NewEdge(EdgeTypePodCallsPod, "cluster-alpha/abc", "cluster-beta/def", nil)
	b := NewEdge(EdgeTypePodCallsPod, "cluster-alpha/abc", "cluster-beta/def", nil)
	assert.Equal(t, a.ID, b.ID, "expected stable ID across rebuilds")
}

func TestNewEdge_UUIDv5Format(t *testing.T) {
	for _, e := range []*Edge{
		NewEdge(EdgeTypePodCallsPod, "cluster-alpha/abc", "cluster-beta/def", nil),
		NewEdge(EdgeTypeServiceSelectsPod, "cluster-alpha/ns/svc", "cluster-alpha/abc", nil),
		NewEdge(EdgeTypePodMountsPVC, "cluster-alpha/abc", "cluster-alpha/ns/claim", nil),
	} {
		assert.Regexp(t, uuidV5Re, e.ID)
	}
}

func TestNewEdge_DistinctTuplesProduceDistinctIDs(t *testing.T) {
	base := NewEdge(EdgeTypePodCallsPod, "src", "tgt", nil)
	others := []*Edge{
		NewEdge(EdgeTypeServiceSelectsPod, "src", "tgt", nil),
		NewEdge(EdgeTypePodCallsPod, "src2", "tgt", nil),
		NewEdge(EdgeTypePodCallsPod, "src", "tgt2", nil),
	}
	seen := map[string]bool{base.ID: true}
	for _, o := range others {
		assert.False(t, seen[o.ID], "expected distinct ID, got collision %s", o.ID)
		seen[o.ID] = true
	}
}

func TestNewEdge_LabelsDefaultEmpty(t *testing.T) {
	e := NewEdge(EdgeTypePodCallsPod, "a", "b", nil)
	assert.NotNil(t, e.Labels, "expected non-nil labels even when nil supplied")
	assert.Empty(t, e.Labels)
}
