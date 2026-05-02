package build

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTopology_PodRestartHandling(t *testing.T) {
	pod := func(uid string, ts model.Time) model.Sample {
		return model.Sample{
			Metric: model.Metric{
				"cluster":   "cluster-alpha",
				"namespace": "shop",
				"pod":       "checkout",
				"uid":       model.LabelValue(uid),
				"node":      "worker-0",
			},
			Value:     1,
			Timestamp: ts,
		}
	}
	vec := sampleVec(pod("uid-1", 100), pod("uid-2", 200))
	tp := parseTopology(vec, nil, nil, nil, nil)
	require.Len(t, tp.Pods, 2, "expected 2 pods")
	require.Len(t, tp.RestartEdges, 1, "expected 1 pod-replaced-by edge")
	assert.Equal(t, "cluster-alpha/uid-2", tp.RestartEdges[0].Target, "expected newest UID as target")
}

func TestParseTopology_MissingClusterBucketed(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"namespace": "shop",
			"pod":       "checkout",
			"uid":       "abc",
			"node":      "worker-0",
		},
	})
	tp := parseTopology(vec, nil, nil, nil, nil)
	require.Len(t, tp.Pods, 1)
	assert.Equal(t, "unknown", tp.Pods[0].Labels()["cluster"])
	assert.Contains(t, tp.ClustersObserved, "unknown")
}

func TestParseTopology_K8sNodeLabelsFlattened(t *testing.T) {
	nodeVec := sampleVec(model.Sample{Metric: model.Metric{"cluster": "cluster-alpha", "node": "worker-0"}})
	addrVec := sampleVec(model.Sample{Metric: model.Metric{"cluster": "cluster-alpha", "node": "worker-0", "type": "ExternalIP", "address": "203.0.113.10"}})
	labelVec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster":                           "cluster-alpha",
			"node":                              "worker-0",
			"label_topology_kubernetes_io_zone": "us-east-1a",
			"label_kubernetes_io_arch":          "amd64",
		},
	})
	tp := parseTopology(nil, nodeVec, addrVec, nil, labelVec)
	require.Len(t, tp.Nodes, 1)
	n := tp.Nodes[0]
	assert.Equal(t, "203.0.113.10", n.Labels()["external_ip"])
	assert.Equal(t, "us-east-1a", n.Labels()["topology.kubernetes.io/zone"])
	assert.Equal(t, "amd64", n.Labels()["kubernetes.io/arch"])
}
