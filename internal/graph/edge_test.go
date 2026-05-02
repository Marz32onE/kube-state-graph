package graph

import (
	"regexp"
	"testing"
)

var uuidV5Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestNewEdge_StableAcrossRebuilds(t *testing.T) {
	a := NewEdge(EdgeTypePodCallsPod, "cluster-alpha/abc", "cluster-beta/def", nil)
	b := NewEdge(EdgeTypePodCallsPod, "cluster-alpha/abc", "cluster-beta/def", nil)
	if a.ID != b.ID {
		t.Fatalf("expected stable ID across rebuilds, got %s vs %s", a.ID, b.ID)
	}
}

func TestNewEdge_UUIDv5Format(t *testing.T) {
	for _, e := range []*Edge{
		NewEdge(EdgeTypePodCallsPod, "cluster-alpha/abc", "cluster-beta/def", nil),
		NewEdge(EdgeTypePodRunsOnNode, "cluster-alpha/abc", "cluster-alpha/worker-0", nil),
		NewEdge(EdgeTypePodMountsPVCOnNode, "cluster-alpha/abc", "cluster-alpha/ns/claim", nil),
	} {
		if !uuidV5Re.MatchString(e.ID) {
			t.Errorf("ID %q does not match UUIDv5 lowercase canonical regex", e.ID)
		}
	}
}

func TestNewEdge_DistinctTuplesProduceDistinctIDs(t *testing.T) {
	base := NewEdge(EdgeTypePodCallsPod, "src", "tgt", nil)
	others := []*Edge{
		NewEdge(EdgeTypePodRunsOnNode, "src", "tgt", nil),                // type differs
		NewEdge(EdgeTypePodCallsPod, "src2", "tgt", nil),                 // source differs
		NewEdge(EdgeTypePodCallsPod, "src", "tgt2", nil),                 // target differs
	}
	seen := map[string]bool{base.ID: true}
	for _, o := range others {
		if seen[o.ID] {
			t.Errorf("expected distinct IDs but got collision %s", o.ID)
		}
		seen[o.ID] = true
	}
}

func TestNewEdge_LabelsDefaultEmpty(t *testing.T) {
	e := NewEdge(EdgeTypePodCallsPod, "a", "b", nil)
	if e.Labels == nil {
		t.Fatal("expected non-nil labels even when nil supplied")
	}
	if len(e.Labels) != 0 {
		t.Fatalf("expected empty labels, got %v", e.Labels)
	}
}
