package graph

import "sort"

// NodeType is the canonical type field on every graph node.
type NodeType string

const (
	NodeTypePod      NodeType = "pod"
	NodeTypeK8sNode  NodeType = "node"
	NodeTypePVC      NodeType = "pvc"
	NodeTypeExternal NodeType = "external"
)

// GraphNode is the sealed interface implemented by every node kind.
//
// Implementations expose the canonical wire fields directly so the API
// serialiser can iterate without type switches.
type GraphNode interface {
	ID() string
	Name() string
	Type() NodeType
	Labels() map[string]string

	isGraphNode()
}

// PodNode represents a Kubernetes pod entity (or a synthesised pod when the
// service-graph reader observes a pod UID with no topology).
type PodNode struct {
	IDValue     string
	NameValue   string
	LabelsValue map[string]string
}

func (p *PodNode) ID() string                { return p.IDValue }
func (p *PodNode) Name() string              { return p.NameValue }
func (p *PodNode) Type() NodeType            { return NodeTypePod }
func (p *PodNode) Labels() map[string]string { return p.LabelsValue }
func (p *PodNode) isGraphNode()              {}

// K8sNode represents a Kubernetes node entity.
type K8sNode struct {
	IDValue     string
	NameValue   string
	LabelsValue map[string]string
}

func (n *K8sNode) ID() string                { return n.IDValue }
func (n *K8sNode) Name() string              { return n.NameValue }
func (n *K8sNode) Type() NodeType            { return NodeTypeK8sNode }
func (n *K8sNode) Labels() map[string]string { return n.LabelsValue }
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
func (p *PVCNode) isGraphNode()              {}

// ExternalNode represents a non-pod endpoint surfaced when the
// KSG_EXTERNAL_NAME_PATTERN substring matches the upstream client/server label.
type ExternalNode struct {
	IDValue     string
	NameValue   string
	LabelsValue map[string]string
}

func (e *ExternalNode) ID() string                { return e.IDValue }
func (e *ExternalNode) Name() string              { return e.NameValue }
func (e *ExternalNode) Type() NodeType            { return NodeTypeExternal }
func (e *ExternalNode) Labels() map[string]string { return e.LabelsValue }
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

// ExternalID returns the unprefixed external node ID.
func ExternalID(value string) string { return "external/" + value }
