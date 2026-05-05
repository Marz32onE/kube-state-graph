package graph

import (
	"errors"
	"sort"
)

// Direction governs traversal direction.
type Direction string

const (
	DirectionIn   Direction = "in"
	DirectionOut  Direction = "out"
	DirectionBoth Direction = "both"
)

// MaxTraversalDepth is the hard upper bound on traversal depth (D7 / spec).
const MaxTraversalDepth = 6

// Scope describes the projection filter applied at response time.
type Scope struct {
	Clusters   map[string]struct{}   // empty ⇒ no cluster filter
	Namespaces map[string]struct{}   // empty ⇒ no namespace filter
	EdgeTypes  map[EdgeType]struct{} // empty ⇒ all edge types
	Pods       map[string]struct{}   // empty ⇒ no pod-name filter

	Root      string    // empty ⇒ no traversal
	Depth     int       // 0..MaxTraversalDepth
	Direction Direction // in | out | both
}

// NewScope constructs a Scope from raw query parameter values, validating ranges.
func NewScope(clusters, namespaces, edgeTypes, pods []string, root string, depth int, direction string) (Scope, error) {
	if depth < 0 {
		return Scope{}, errors.New("depth must be non-negative")
	}
	if depth > MaxTraversalDepth {
		return Scope{}, errors.New("depth exceeds maximum")
	}
	if root != "" {
		if depth == 0 {
			depth = 2
		}
		switch Direction(direction) {
		case "":
			direction = string(DirectionBoth)
		case DirectionIn, DirectionOut, DirectionBoth:
		default:
			return Scope{}, errors.New("invalid direction")
		}
	}
	return Scope{
		Clusters:   stringSet(clusters),
		Namespaces: stringSet(namespaces),
		EdgeTypes:  edgeTypeSet(edgeTypes),
		Pods:       stringSet(pods),
		Root:       root,
		Depth:      depth,
		Direction:  Direction(direction),
	}, nil
}

// PodFilterActive reports whether the scope restricts to a named pod set.
func (s Scope) PodFilterActive() bool {
	return len(s.Pods) > 0
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

func edgeTypeSet(values []string) map[EdgeType]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[EdgeType]struct{}, len(values))
	for _, v := range values {
		if v != "" {
			out[EdgeType(v)] = struct{}{}
		}
	}
	return out
}

// SortedKeys returns keys of a map[string]struct{} in deterministic order.
func SortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
