package build

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/internal/graph"
)

func sampleTopology() Topology {
	return Topology{
		Pods: []*graph.PodNode{
			{IDValue: "cluster-alpha/abc", NameValue: "checkout", LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"}},
			{IDValue: "cluster-beta/def", NameValue: "payments", LabelsValue: map[string]string{"cluster": "cluster-beta", "namespace": "billing"}},
		},
	}
}

func sampleVec(samples ...model.Sample) model.Vector {
	out := make(model.Vector, len(samples))
	for i := range samples {
		s := samples[i]
		out[i] = &s
	}
	return out
}

func TestParseServiceGraph_DropsZeroRate(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client_cluster":     "cluster-alpha",
			"server_cluster":     "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "abc",
		},
		Value: 0,
	})
	res := parseServiceGraph(vec, "", sampleTopology())
	assert.Empty(t, res.Edges)
}

func TestParseServiceGraph_CrossClusterEdge(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "payments",
			"client_cluster":     "cluster-alpha",
			"server_cluster":     "cluster-beta",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())
	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "cluster-alpha", e.Labels["client_cluster"])
	assert.Equal(t, "cluster-beta", e.Labels["server_cluster"])
	for _, k := range []string{"rate", "p99_ms", "error_rate", "cross_cluster", "ghost"} {
		assert.NotContains(t, e.Labels, k, "unexpected label %q in v1 edge labels", k)
	}
}

func TestParseServiceGraph_ExternalSubstitution_ClientSide(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "http://api.example.com",
			"server":             "checkout",
			"client_cluster":     "",
			"server_cluster":     "cluster-alpha",
			"client_k8s_pod_uid": "",
			"server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "://", sampleTopology())
	require.Len(t, res.Edges, 1)
	require.Len(t, res.ExternalNodes, 1)
	ext := res.ExternalNodes[0]
	assert.Equal(t, "http://api.example.com", ext.NameValue)
	assert.Equal(t, "://", ext.LabelsValue["pattern"])
	assert.NotContains(t, ext.LabelsValue, "cluster", "external nodes must not carry cluster label")
	e := res.Edges[0]
	assert.Equal(t, "external/http://api.example.com", e.Source)
	assert.Empty(t, e.Labels["client_cluster"], "client_cluster for external endpoint must be empty")
	assert.Equal(t, "cluster-alpha", e.Labels["server_cluster"])
}

func TestParseServiceGraph_PatternEmpty_DisablesRule(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "http://api.example.com",
			"server":             "payments",
			"client_cluster":     "cluster-alpha",
			"server_cluster":     "cluster-beta",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())
	assert.Empty(t, res.ExternalNodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "cluster-alpha/abc", res.Edges[0].Source)
}

func TestParseServiceGraph_GhostFallback(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "missing",
			"client_cluster":     "cluster-alpha",
			"server_cluster":     "cluster-beta",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "missing-uid",
		},
		Value: 1,
	})
	res := parseServiceGraph(vec, "", sampleTopology())
	require.Len(t, res.SynthPods, 1)
	sp := res.SynthPods[0]
	assert.Equal(t, "cluster-beta/missing-uid", sp.IDValue)
	assert.NotContains(t, sp.LabelsValue, "ghost", "ghost label must NOT be set in v1")
}

func TestParseServiceGraph_EmptyVectorIsNotAnError(t *testing.T) {
	res := parseServiceGraph(nil, "", sampleTopology())
	assert.Empty(t, res.Edges)
}

func TestParseServiceGraph_NoForbiddenNumericLabels(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client_cluster":     "cluster-alpha",
			"server_cluster":     "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", Topology{
		Pods: []*graph.PodNode{
			{IDValue: "cluster-alpha/abc"},
			{IDValue: "cluster-alpha/def"},
		},
	})
	for _, e := range res.Edges {
		for _, k := range []string{"rate", "p99_ms", "error_rate"} {
			assert.NotContains(t, e.Labels, k)
		}
	}
}
