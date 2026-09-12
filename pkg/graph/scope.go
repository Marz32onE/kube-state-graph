package graph

import (
	"maps"
	"slices"
)

// Scope describes the projection filter applied at response time, over the
// freshly built graph.
//
// `cluster` and `namespace` appear here AND as upstream selector dimensions
// (promql.Selector): the build narrows the topology at the source, and the
// projection applies the same two filters again as defence in depth — a node
// that reached the graph anyway (an unlabelled series bucketed to
// cluster="unknown", say) must not slip into a filtered view.
//
// There is deliberately no edge-type dimension. `edge_type` was a
// projection-only gate over the edge list that never reached node admission,
// so it emptied an edge class while leaving the infrastructure those edges
// justified; it is withdrawn, and a consumer wanting fewer edge types drops
// them by the `type` every edge already carries.
type Scope struct {
	Clusters   map[string]struct{} // empty ⇒ no cluster filter
	Namespaces map[string]struct{} // empty ⇒ no namespace filter

	// Inventory turns the default connectivity prune OFF: every pod is emitted
	// with its pod-to-node / pod-mounts-pvc / pvc-to-netapp-aggr chain
	// regardless of traffic, and an infrastructure node is admitted even when
	// nothing in scope references it (bounded by the filters that CAN exclude
	// it by its own labels — see infraNodePassesFilters).
	//
	// It is the INVERSE of the request's `prune` parameter (`prune=false` ⇒
	// Inventory=true) so the zero Scope keeps today's meaning: prune on.
	Inventory bool
}

// NewScope constructs a Scope from raw query parameter values.
//
// It returns no error: every remaining dimension is a set of opaque strings
// whose values are validated (length, control characters) by the request
// parser before they reach here, and an unknown cluster or namespace is an
// empty result rather than a rejection.
func NewScope(clusters, namespaces []string, inventory bool) Scope {
	return Scope{
		Clusters:   stringSet(clusters),
		Namespaces: stringSet(namespaces),
		Inventory:  inventory,
	}
}

func stringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v != "" {
			out[v] = struct{}{}
		}
	}
	return out
}

// SortedKeys returns keys of a map[string]struct{} in deterministic order.
func SortedKeys(m map[string]struct{}) []string {
	// Collected into a non-nil slice rather than slices.Sorted, which returns
	// nil for an empty map: an exported result may be JSON-encoded, where nil
	// renders as null instead of [].
	out := slices.AppendSeq(make([]string, 0, len(m)), maps.Keys(m))
	slices.Sort(out)
	return out
}
