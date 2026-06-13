package graph

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Cross-cluster classification must cover every edge type that can cross
// clusters — including pod-calls-service via the D29 cluster-family fan-out —
// not just pod-calls-pod (review finding: the old hardcoded type gate bucketed
// all cross-cluster service edges as cross_cluster="false").
func TestEdgeCountByType_CrossClusterBuckets(t *testing.T) {
	clientPod := &PodNode{IDValue: "prod-1/abc", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "prod-1"}}
	remotePod := &PodNode{IDValue: "prod-2/def", NameValue: "payments", LabelsValue: map[string]string{"cluster": "prod-2"}}
	remoteSvc := &ServiceNode{IDValue: "prod-2/messaging/nats", NameValue: "nats", LabelsValue: map[string]string{"cluster": "prod-2", "namespace": "messaging"}}
	localSvc := &ServiceNode{IDValue: "prod-1/messaging/nats", NameValue: "nats", LabelsValue: map[string]string{"cluster": "prod-1", "namespace": "messaging"}}
	backing := &PodNode{IDValue: "prod-2/n2", NameValue: "nats-0", LabelsValue: map[string]string{"cluster": "prod-2"}}
	ext := &ExternalNode{IDValue: "external/admin", NameValue: "admin", LabelsValue: map[string]string{}}

	edges := []*Edge{
		// Cross-cluster pod-calls-pod (server pod recovered via UID index).
		NewEdge(EdgeTypePodCallsPod, clientPod.IDValue, remotePod.IDValue, map[string]string{"cluster": "prod-1"}),
		// Cross-cluster pod-calls-service (family fan-out into prod-2).
		NewEdge(EdgeTypePodCallsService, clientPod.IDValue, remoteSvc.IDValue, map[string]string{"cluster": "prod-1"}),
		// Intra-cluster pod-calls-service.
		NewEdge(EdgeTypePodCallsService, clientPod.IDValue, localSvc.IDValue, map[string]string{"cluster": "prod-1"}),
		// service-selects-pod is intra-cluster by construction.
		NewEdge(EdgeTypeServiceSelectsPod, remoteSvc.IDValue, backing.IDValue, map[string]string{}),
		// External endpoints can never prove a cluster boundary → "false".
		NewEdge(EdgeTypePodCallsPod, ext.IDValue, clientPod.IDValue, map[string]string{}),
	}
	g := NewGraph([]GraphNode{clientPod, remotePod, remoteSvc, localSvc, backing, ext}, edges, time.Unix(0, 0).UTC())

	counts := g.EdgeCountByType()
	assert.Equal(t, 1, counts[[2]string{string(EdgeTypePodCallsPod), "true"}])
	assert.Equal(t, 1, counts[[2]string{string(EdgeTypePodCallsPod), "false"}], "external endpoint buckets as false")
	assert.Equal(t, 1, counts[[2]string{string(EdgeTypePodCallsService), "true"}], "cross-cluster pod-calls-service must bucket as true")
	assert.Equal(t, 1, counts[[2]string{string(EdgeTypePodCallsService), "false"}])
	assert.Equal(t, 1, counts[[2]string{string(EdgeTypeServiceSelectsPod), "false"}])

	// The cross-cluster total is the sum of the "true" buckets — the single
	// EdgeCountByType scan is the one source for both metrics and the log line.
	cross := 0
	for k, n := range counts {
		if k[1] == "true" {
			cross += n
		}
	}
	assert.Equal(t, 2, cross, "one cross-cluster pod edge + one cross-cluster service edge")
}
