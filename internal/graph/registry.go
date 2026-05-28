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

// EdgeTypes is the in-code registry consumed by both the graph builder and
// the /v1/edge-types HTTP handler.
var EdgeTypes = []EdgeTypeDefinition{
	{
		Type:            EdgeTypePodRunsOnNode,
		Description:     "Pod scheduled on a node, derived from kube_pod_info{node=...}. Always intra-cluster.",
		SourceType:      []NodeType{NodeTypePod},
		TargetType:      []NodeType{NodeTypeK8sNode},
		Directed:        true,
		MayCrossCluster: false,
		Labels: []EdgeTypeLabel{
			{Name: "scheduled_at", ValueType: "string", Description: "RFC3339 timestamp of pod-to-node assignment within the queried window."},
		},
	},
	{
		Type:            EdgeTypePodMountsPVC,
		Description:     "Pod mounts a PVC bound on the pod's host node. Always intra-cluster.",
		SourceType:      []NodeType{NodeTypePod},
		TargetType:      []NodeType{NodeTypePVC},
		Directed:        true,
		MayCrossCluster: false,
		Labels: []EdgeTypeLabel{
			{Name: "claim_name", ValueType: "string"},
			{Name: "storage_class", ValueType: "string"},
		},
	},
	{
		Type:            EdgeTypePodCallsPod,
		Description:     "Pod-UID-resolved RPC edge from service-graph metrics. May cross clusters when the resolved source and target pods live in different clusters (recovered from the topology pod-UID index since the metric only carries the trace-source cluster). Endpoints may be 'others' nodes when KSG_OTHERS_NAME_PATTERN matches the upstream client/server label (D18), or 'external' nodes via the missing-UID human-label fallback (D27).",
		SourceType:      []NodeType{NodeTypePod, NodeTypeOthers, NodeTypeExternal},
		TargetType:      []NodeType{NodeTypePod, NodeTypeOthers, NodeTypeExternal},
		Directed:        true,
		MayCrossCluster: true,
		Labels: []EdgeTypeLabel{
			{Name: "cluster", ValueType: "string"},
		},
	},
}
