package build

import (
	"testing"

	"github.com/prometheus/common/model"
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
	if len(tp.Pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(tp.Pods))
	}
	if len(tp.RestartEdges) != 1 {
		t.Fatalf("expected 1 pod-replaced-by edge, got %d", len(tp.RestartEdges))
	}
	e := tp.RestartEdges[0]
	if e.Target != "cluster-alpha/uid-2" {
		t.Errorf("expected newest UID as target, got %q", e.Target)
	}
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
	if len(tp.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(tp.Pods))
	}
	if tp.Pods[0].Labels()["cluster"] != "unknown" {
		t.Errorf("expected cluster=\"unknown\", got %q", tp.Pods[0].Labels()["cluster"])
	}
	found := false
	for _, c := range tp.ClustersObserved {
		if c == "unknown" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected \"unknown\" in ClustersObserved, got %v", tp.ClustersObserved)
	}
}

func TestParseTopology_K8sNodeLabelsFlattened(t *testing.T) {
	nodeVec := sampleVec(model.Sample{Metric: model.Metric{"cluster": "cluster-alpha", "node": "worker-0"}})
	addrVec := sampleVec(model.Sample{Metric: model.Metric{"cluster": "cluster-alpha", "node": "worker-0", "type": "ExternalIP", "address": "203.0.113.10"}})
	labelVec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster":                       "cluster-alpha",
			"node":                          "worker-0",
			"label_topology_kubernetes_io_zone": "us-east-1a",
			"label_kubernetes_io_arch":          "amd64",
		},
	})
	tp := parseTopology(nil, nodeVec, addrVec, nil, labelVec)
	if len(tp.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(tp.Nodes))
	}
	n := tp.Nodes[0]
	if n.Labels()["external_ip"] != "203.0.113.10" {
		t.Errorf("external_ip not surfaced")
	}
	if n.Labels()["topology.kubernetes.io/zone"] != "us-east-1a" {
		t.Errorf("expected flattened topology.kubernetes.io/zone label, got labels %v", n.Labels())
	}
	if n.Labels()["kubernetes.io/arch"] != "amd64" {
		t.Errorf("expected flattened kubernetes.io/arch label, got labels %v", n.Labels())
	}
}
