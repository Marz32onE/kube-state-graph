package graph

// EdgeTypeLabel describes a single label key an edge of this type may emit.
type EdgeTypeLabel struct {
	Name        string `json:"name"`
	ValueType   string `json:"value_type"`
	Description string `json:"description,omitempty"`
}

// EdgeTypeDefinition is one entry in the static catalogue served by
// GET /v1/edge-types.
type EdgeTypeDefinition struct {
	Type            EdgeType        `json:"type"`
	Description     string          `json:"description"`
	SourceType      []NodeType      `json:"source_type"`
	TargetType      []NodeType      `json:"target_type"`
	Directed        bool            `json:"directed"`
	MayCrossCluster bool            `json:"may_cross_cluster"`
	Labels          []EdgeTypeLabel `json:"labels"`
}

// validEdgeTypes is the lookup set derived from EdgeTypes at init. Because it
// is built from the registry itself, it can never drift from what the builder
// produces and /v1/edge-types advertises.
var validEdgeTypes = func() map[EdgeType]struct{} {
	out := make(map[EdgeType]struct{}, len(EdgeTypes))
	for _, def := range EdgeTypes {
		out[def.Type] = struct{}{}
	}
	return out
}()

// ValidEdgeType reports whether t is a registered edge type — i.e. present in
// the EdgeTypes registry served by /v1/edge-types. Request parsers use it to
// reject unknown ?edge_type= filter values instead of silently matching no
// edges.
func ValidEdgeType(t EdgeType) bool {
	_, ok := validEdgeTypes[t]
	return ok
}

// EdgeTypes is the in-code registry consumed by both the graph builder and
// the /v1/edge-types HTTP handler.
var EdgeTypes = []EdgeTypeDefinition{
	{
		Type:            EdgeTypePodMountsPVC,
		Description:     "Pod mounts a PVC bound on the pod's host node. Always intra-cluster.",
		SourceType:      []NodeType{NodeTypePod},
		TargetType:      []NodeType{NodeTypePVC},
		Directed:        true,
		MayCrossCluster: false,
		Labels: []EdgeTypeLabel{
			{Name: "claim_name", ValueType: "string"},
		},
	},
	{
		Type:            EdgeTypePodCallsPod,
		Description:     "Pod-UID-resolved RPC edge from service-graph metrics. May cross clusters when the resolved source and target pods live in different clusters (recovered from the topology pod-UID index since the metric only carries the trace-source cluster). An endpoint whose client/server label is a '://' connection string resolving to an in-cluster Kubernetes Service produces a 'pod-calls-service' edge instead (see that type); a '://' string whose service resolution yields no surviving candidate (no family match and no eligible unknown-family fallback) falls back to an 'external' node, and endpoints with a missing pod UID and a non-URL label become 'external' nodes via the human-label fallback (D27).",
		SourceType:      []NodeType{NodeTypePod, NodeTypeService, NodeTypeExternal},
		TargetType:      []NodeType{NodeTypePod, NodeTypeExternal},
		Directed:        true,
		MayCrossCluster: true,
		Labels: []EdgeTypeLabel{
			{Name: "cluster", ValueType: "string"},
		},
	},
	{
		Type:            EdgeTypePodCallsService,
		Description:     "Service-graph call edge whose target resolves to an in-cluster Kubernetes Service node (from a '://' connection string per D29). The addressed (namespace, service) is resolved against every loaded cluster in the caller's family (cluster names equal after normalising digit runs; anchored on the UID-recovered client-pod cluster when available, else the trace-source label), emitting one edge per surviving family cluster — so the edge may cross clusters. Candidates provably without backing pods (zero endpoints in an endpoint-visible cluster) are pruned when an endpoint-backed sibling exists; an anchor naming no loaded family falls back to the single loaded family holding the service (ambiguous multi-family names stay external). Each resolved Service fans out service-selects-pod edges to its own cluster's backing pods. Carries labels.cluster when the client side is a pod (D9); cross-cluster status is derived by comparing the source and target nodes' labels.cluster.",
		SourceType:      []NodeType{NodeTypePod, NodeTypeService, NodeTypeExternal},
		TargetType:      []NodeType{NodeTypeService},
		Directed:        true,
		MayCrossCluster: true,
		Labels: []EdgeTypeLabel{
			{Name: "cluster", ValueType: "string"},
		},
	},
	{
		Type:            EdgeTypeServiceSelectsPod,
		Description:     "A Kubernetes Service routes to a backing pod, derived from kube_endpointslice_endpoints joined to topology pods (D29). Materialised on demand only for services referenced by a '://' connection-string endpoint. Always intra-cluster within the resolved service's own cluster — a Service and its backing pods share a cluster by construction.",
		SourceType:      []NodeType{NodeTypeService},
		TargetType:      []NodeType{NodeTypePod},
		Directed:        true,
		MayCrossCluster: false,
		Labels: []EdgeTypeLabel{
			{Name: "namespace", ValueType: "string", Description: "Namespace of the service and its backing pod (optional)."},
		},
	},
}
