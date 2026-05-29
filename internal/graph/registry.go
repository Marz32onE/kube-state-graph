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
		Description:     "Pod-UID-resolved RPC edge from service-graph metrics. May cross clusters when the resolved source and target pods live in different clusters (recovered from the topology pod-UID index since the metric only carries the trace-source cluster). An endpoint whose client/server label is a '://' connection string is resolved to a 'service' node or a real pod when it names an in-cluster Kubernetes Service / headless pod, and otherwise to an 'others' node (D29); endpoints with a missing pod UID and a non-URL label become 'external' nodes via the human-label fallback (D27).",
		SourceType:      []NodeType{NodeTypePod, NodeTypeService, NodeTypeOthers, NodeTypeExternal},
		TargetType:      []NodeType{NodeTypePod, NodeTypeService, NodeTypeOthers, NodeTypeExternal},
		Directed:        true,
		MayCrossCluster: true,
		Labels: []EdgeTypeLabel{
			{Name: "cluster", ValueType: "string"},
		},
	},
	{
		Type:            EdgeTypeServiceSelectsPod,
		Description:     "A Kubernetes Service routes to a backing pod, derived from kube_endpointslice_endpoints joined to topology pods (D29). Materialised on demand only for services referenced by a '://' connection-string endpoint. Always intra-cluster.",
		SourceType:      []NodeType{NodeTypeService},
		TargetType:      []NodeType{NodeTypePod},
		Directed:        true,
		MayCrossCluster: false,
		Labels: []EdgeTypeLabel{
			{Name: "namespace", ValueType: "string", Description: "Namespace of the service and its backing pod (optional)."},
		},
	},
}
