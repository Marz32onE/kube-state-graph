package graph

import "sort"

// NodeType is the canonical type field on every graph node.
type NodeType string

const (
	NodeTypePod      NodeType = "pod"
	NodeTypeK8sNode  NodeType = "node"
	NodeTypePVC      NodeType = "pvc"
	NodeTypeService  NodeType = "service"
	NodeTypeOthers   NodeType = "others"
	NodeTypeExternal NodeType = "external"
	NodeTypeSwitch   NodeType = "switch"
)

// GraphNode is the sealed interface implemented by every node kind.
//
// Implementations expose the canonical wire fields directly so the API
// serialiser can iterate without type switches. IPAddress carries the
// observed IPv4/IPv6 strings for nodes that have them (pods → pod_ip,
// K8s nodes → ExternalIP, services → cluster_ip); other node kinds
// return nil.
type GraphNode interface {
	ID() string
	Name() string
	Type() NodeType
	Labels() map[string]string
	IPAddress() []string

	isGraphNode()
}

// PodNode represents a Kubernetes pod entity (or a synthesised pod when the
// service-graph reader observes a pod UID with no topology).
type PodNode struct {
	IDValue        string
	NameValue      string
	LabelsValue    map[string]string
	IPAddressValue []string
}

func (p *PodNode) ID() string                { return p.IDValue }
func (p *PodNode) Name() string              { return p.NameValue }
func (p *PodNode) Type() NodeType            { return NodeTypePod }
func (p *PodNode) Labels() map[string]string { return p.LabelsValue }
func (p *PodNode) IPAddress() []string       { return p.IPAddressValue }
func (p *PodNode) isGraphNode()              {}

// K8sNode represents a Kubernetes node entity.
type K8sNode struct {
	IDValue        string
	NameValue      string
	LabelsValue    map[string]string
	IPAddressValue []string
}

func (n *K8sNode) ID() string                { return n.IDValue }
func (n *K8sNode) Name() string              { return n.NameValue }
func (n *K8sNode) Type() NodeType            { return NodeTypeK8sNode }
func (n *K8sNode) Labels() map[string]string { return n.LabelsValue }
func (n *K8sNode) IPAddress() []string       { return n.IPAddressValue }
func (n *K8sNode) isGraphNode()              {}

// PVCNode represents a PersistentVolumeClaim entity.
type PVCNode struct {
	IDValue     string
	NameValue   string
	LabelsValue map[string]string
}

func (p *PVCNode) ID() string                { return p.IDValue }
func (p *PVCNode) Name() string              { return p.NameValue }
func (p *PVCNode) Type() NodeType            { return NodeTypePVC }
func (p *PVCNode) Labels() map[string]string { return p.LabelsValue }
func (p *PVCNode) IPAddress() []string       { return nil }
func (p *PVCNode) isGraphNode()              {}

// ServiceNode represents a Kubernetes Service surfaced when a service-graph
// connection string (`<service>.<namespace>.svc.<domain>`) resolves to an
// in-cluster Service via `kube_service_info` (see design.md D29). Its backing
// pods are wired with `service-selects-pod` edges. IPAddressValue carries the
// service's `cluster_ip` (single-element slice) when it is not the headless
// sentinel `"None"`; headless services carry nil.
type ServiceNode struct {
	IDValue        string
	NameValue      string
	LabelsValue    map[string]string
	IPAddressValue []string
}

func (s *ServiceNode) ID() string                { return s.IDValue }
func (s *ServiceNode) Name() string              { return s.NameValue }
func (s *ServiceNode) Type() NodeType            { return NodeTypeService }
func (s *ServiceNode) Labels() map[string]string { return s.LabelsValue }
func (s *ServiceNode) IPAddress() []string       { return s.IPAddressValue }
func (s *ServiceNode) isGraphNode()              {}

// OthersNode represents a non-pod endpoint surfaced when the
// KSG_OTHERS_NAME_PATTERN substring matches the upstream client/server label
// (operator-declared third-party endpoint; see design.md D18).
type OthersNode struct {
	IDValue     string
	NameValue   string
	LabelsValue map[string]string
}

func (o *OthersNode) ID() string                { return o.IDValue }
func (o *OthersNode) Name() string              { return o.NameValue }
func (o *OthersNode) Type() NodeType            { return NodeTypeOthers }
func (o *OthersNode) Labels() map[string]string { return o.LabelsValue }
func (o *OthersNode) IPAddress() []string       { return nil }
func (o *OthersNode) isGraphNode()              {}

// ExternalNode represents a non-pod endpoint surfaced by the missing-UID
// human-label fallback (D27): the service-graph producer dropped
// client_k8s_pod_uid or server_k8s_pod_uid, but the human-readable
// client/server label survived. Disjoint from OthersNode.
type ExternalNode struct {
	IDValue     string
	NameValue   string
	LabelsValue map[string]string
}

func (e *ExternalNode) ID() string                { return e.IDValue }
func (e *ExternalNode) Name() string              { return e.NameValue }
func (e *ExternalNode) Type() NodeType            { return NodeTypeExternal }
func (e *ExternalNode) Labels() map[string]string { return e.LabelsValue }
func (e *ExternalNode) IPAddress() []string       { return nil }
func (e *ExternalNode) isGraphNode()              {}

// SortNodes orders nodes deterministically by ID for stable output.
func SortNodes(nodes []GraphNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodes[i].ID() < nodes[j].ID()
	})
}

// PodID returns the cluster-scoped pod ID.
func PodID(cluster, uid string) string { return cluster + "/" + uid }

// K8sNodeID returns the cluster-scoped node ID.
func K8sNodeID(cluster, name string) string { return cluster + "/" + name }

// PVCID returns the cluster-scoped PVC ID.
func PVCID(cluster, namespace, claim string) string {
	return cluster + "/" + namespace + "/" + claim
}

// ServiceID returns the cluster-scoped Service ID (mirrors PVC keying).
func ServiceID(cluster, namespace, service string) string {
	return cluster + "/" + namespace + "/" + service
}

// OthersID returns the pattern-matched others node ID.
func OthersID(value string) string { return "others/" + value }

// ExternalID returns the missing-UID-fallback external node ID.
func ExternalID(value string) string { return "external/" + value }

// Reserved: the "cluster/" ID prefix is owned by the Cytoscape presentation
// layer (api.clusterParentID, design.md D31) for synthetic compound group
// nodes. Those are NOT GraphNodes and are never minted here — do not reuse the
// prefix for a real node kind.
