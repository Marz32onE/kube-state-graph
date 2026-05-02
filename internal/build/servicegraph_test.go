package build

import (
	"testing"

	"github.com/prometheus/common/model"

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
	if len(res.Edges) != 0 {
		t.Errorf("expected 0 edges for zero-rate series, got %d", len(res.Edges))
	}
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
	if len(res.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(res.Edges))
	}
	e := res.Edges[0]
	if e.Labels["client_cluster"] != "cluster-alpha" || e.Labels["server_cluster"] != "cluster-beta" {
		t.Errorf("cluster labels mismatch: %v", e.Labels)
	}
	for _, k := range []string{"rate", "p99_ms", "error_rate", "cross_cluster", "ghost"} {
		if _, ok := e.Labels[k]; ok {
			t.Errorf("unexpected label %q in v1 edge labels", k)
		}
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
	if len(res.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(res.Edges))
	}
	if len(res.ExternalNodes) != 1 {
		t.Fatalf("expected 1 external node, got %d", len(res.ExternalNodes))
	}
	ext := res.ExternalNodes[0]
	if ext.NameValue != "http://api.example.com" {
		t.Errorf("name: got %q, want http://api.example.com", ext.NameValue)
	}
	if ext.LabelsValue["pattern"] != "://" {
		t.Errorf("pattern label missing or wrong: %v", ext.LabelsValue)
	}
	if _, ok := ext.LabelsValue["cluster"]; ok {
		t.Errorf("external nodes must not carry cluster label")
	}
	e := res.Edges[0]
	if e.Source != "external/http://api.example.com" {
		t.Errorf("source: got %q", e.Source)
	}
	if e.Labels["client_cluster"] != "" {
		t.Errorf("client_cluster for external endpoint must be empty, got %q", e.Labels["client_cluster"])
	}
	if e.Labels["server_cluster"] != "cluster-alpha" {
		t.Errorf("server_cluster: got %q", e.Labels["server_cluster"])
	}
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
	if len(res.ExternalNodes) != 0 {
		t.Errorf("expected no external nodes when pattern is empty, got %d", len(res.ExternalNodes))
	}
	if len(res.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(res.Edges))
	}
	if res.Edges[0].Source != "cluster-alpha/abc" {
		t.Errorf("source: got %q, want cluster-alpha/abc (pod resolution)", res.Edges[0].Source)
	}
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
	if len(res.SynthPods) != 1 {
		t.Fatalf("expected 1 synthesised pod, got %d", len(res.SynthPods))
	}
	sp := res.SynthPods[0]
	if sp.IDValue != "cluster-beta/missing-uid" {
		t.Errorf("id: got %q", sp.IDValue)
	}
	if _, ok := sp.LabelsValue["ghost"]; ok {
		t.Errorf("ghost label must NOT be set in v1")
	}
}

func TestParseServiceGraph_EmptyVectorIsNotAnError(t *testing.T) {
	res := parseServiceGraph(nil, "", sampleTopology())
	if len(res.Edges) != 0 {
		t.Errorf("expected zero edges, got %d", len(res.Edges))
	}
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
			if _, ok := e.Labels[k]; ok {
				t.Errorf("v1 edges must not carry numeric label %q", k)
			}
		}
	}
}
