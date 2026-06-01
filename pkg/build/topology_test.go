package build

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseTopology_PodRestartCollapsesToLatestUID — when the same
// (cluster, namespace, pod-name) churns UIDs within the window, only the
// newest UID survives. There is no reliable way to map old → new pod
// identity once kubelet has deleted the pod, so the API does not attempt it.
func TestParseTopology_PodRestartCollapsesToLatestUID(t *testing.T) {
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
	tp := parseTopology(vec, nil, nil, nil, nil, nil, nil, nil)
	require.Len(t, tp.Pods, 1, "older UID must be discarded; only newest survives")
	assert.Equal(t, "cluster-alpha/uid-2", tp.Pods[0].ID(), "newest UID must be canonical pod")
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
	tp := parseTopology(vec, nil, nil, nil, nil, nil, nil, nil)
	require.Len(t, tp.Pods, 1)
	assert.Equal(t, "unknown", tp.Pods[0].Labels()["cluster"])
	assert.Contains(t, tp.ClustersObserved, "unknown")
}

func TestParseTopology_PodIPAttribute(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster":   "cluster-alpha",
			"namespace": "shop",
			"pod":       "checkout",
			"uid":       "uid-1",
			"node":      "worker-0",
			"pod_ip":    "10.244.0.42",
			"host_ip":   "10.0.0.7",
		},
		Value: 1,
	})
	tp := parseTopology(vec, nil, nil, nil, nil, nil, nil, nil)
	require.Len(t, tp.Pods, 1)
	pod := tp.Pods[0]
	assert.Equal(t, []string{"10.244.0.42"}, pod.IPAddress(),
		"pod_ip must surface as a top-level IPAddress attribute")
	labels := pod.Labels()
	_, hasPodIP := labels["pod_ip"]
	_, hasHostIP := labels["host_ip"]
	assert.False(t, hasPodIP, "pod_ip must not appear in labels")
	assert.False(t, hasHostIP, "host_ip is dropped — it is the node's IP, not the pod's")
}

// kube-state-metrics emits multiple kube_pod_info series for a single pod-UID
// while the pod is being scheduled — early scrapes lack node/pod_ip/host_ip.
// parseTopology must merge labels across same-UID samples so the emitted
// PodNode reflects the most informative observation, regardless of sample
// order or timestamp ties.
func TestParseTopology_MergesSameUIDPartialLabels(t *testing.T) {
	// Two samples for the same UID, identical timestamp:
	// 1. early scrape — no node, no pod_ip, no host_ip
	// 2. later scrape — full labels
	vec := sampleVec(
		model.Sample{
			Metric: model.Metric{
				"cluster":   "cluster-alpha",
				"namespace": "shop",
				"pod":       "checkout",
				"uid":       "uid-1",
			},
			Value: 1, Timestamp: 100,
		},
		model.Sample{
			Metric: model.Metric{
				"cluster":   "cluster-alpha",
				"namespace": "shop",
				"pod":       "checkout",
				"uid":       "uid-1",
				"node":      "worker-0",
				"pod_ip":    "10.244.0.42",
				"host_ip":   "10.0.0.7",
			},
			Value: 1, Timestamp: 100,
		},
	)
	tp := parseTopology(vec, nil, nil, nil, nil, nil, nil, nil)
	require.Len(t, tp.Pods, 1)
	pod := tp.Pods[0]
	assert.Equal(t, []string{"10.244.0.42"}, pod.IPAddress(),
		"pod_ip must survive merge from richer sample and surface on IPAddress")
	labels := pod.Labels()
	assert.Equal(t, "cluster-alpha/worker-0", labels["node"], "node must survive merge from richer sample")
}

func TestParseTopology_PodIPAbsentWhenMetricMissing(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster":   "cluster-alpha",
			"namespace": "shop",
			"pod":       "checkout",
			"uid":       "uid-1",
			"node":      "worker-0",
		},
		Value: 1,
	})
	tp := parseTopology(vec, nil, nil, nil, nil, nil, nil, nil)
	require.Len(t, tp.Pods, 1)
	assert.Nil(t, tp.Pods[0].IPAddress(), "IPAddress should be nil when pod_ip is absent")
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
	tp := parseTopology(nil, nodeVec, addrVec, nil, labelVec, nil, nil, nil)
	require.Len(t, tp.Nodes, 1)
	n := tp.Nodes[0]
	assert.Equal(t, []string{"203.0.113.10"}, n.IPAddress(),
		"ExternalIP must surface on the K8sNode IPAddress attribute")
	_, hasExternalIP := n.Labels()["external_ip"]
	assert.False(t, hasExternalIP, "external_ip must not appear in labels")
	assert.Equal(t, "us-east-1a", n.Labels()["topology.kubernetes.io/zone"])
	assert.Equal(t, "amd64", n.Labels()["kubernetes.io/arch"])
}

// TestUnflattenLabel_HeuristicLimits documents both the supported case (DNS
// domain prefix label_<dns>_<segment> ⇒ <dns>/<segment>) and the known
// limitation: single-segment underscored labels collapse to dots because the
// heuristic cannot distinguish "domain prefix" from "underscored name".
//
// If you change the algorithm, expect to coordinate with kube-state-metrics
// label-flattening behaviour (k8s.io/kube-state-metrics/internal/store/utils).
// The lossy single-segment case is accepted because the dominant in-the-wild
// shape is the DNS-prefixed one.
func TestUnflattenLabel_HeuristicLimits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		// DNS-prefixed (correct round-trip)
		{"label_topology_kubernetes_io_zone", "topology.kubernetes.io/zone"},
		{"label_kubernetes_io_arch", "kubernetes.io/arch"},
		{"label_app_kubernetes_io_name", "app.kubernetes.io/name"},
		// Single-segment (no underscore in original) — round-trips fine.
		{"label_app", "app"},
		{"label_simple", "simple"},
		// KNOWN LIMITATION: underscored single-segment labels become dotted.
		// Documented here so the behaviour is intentional, not accidental.
		{"label_app_version", "app.version"},
	}
	for _, tc := range cases {
		if got := unflattenLabel(tc.in); got != tc.want {
			t.Errorf("unflattenLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParseTopology_ServiceAndEndpointSliceIndexes — D29 connection-string
// resolution indexes: kube_service_info → ServicesByNameNS (cluster_ip retained,
// including the headless "None" sentinel); kube_endpointslice_labels +
// kube_endpointslice_endpoints → EndpointsByService (slice→service via
// label_kubernetes_io_service_name, endpoint→pod via targetref); kube_pod_info
// → PodsByNameNS.
func TestParseTopology_ServiceAndEndpointSliceIndexes(t *testing.T) {
	podVec := sampleVec(
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "pod": "payments-0", "uid": "p1", "node": "w0", "pod_ip": "10.0.1.1"}},
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "pod": "payments-1", "uid": "p2", "node": "w1", "pod_ip": "10.0.1.2"}},
	)
	svcVec := sampleVec(
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "service": "payments", "cluster_ip": "10.96.0.5"}},
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "service": "mongo", "cluster_ip": "None"}}, // headless
	)
	epLabelsVec := sampleVec(
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "endpointslice": "payments-x1", "label_kubernetes_io_service_name": "payments"}},
	)
	epEndpointsVec := sampleVec(
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "endpointslice": "payments-x1", "targetref_kind": "Pod", "targetref_name": "payments-0", "targetref_namespace": "pay", "hostname": "payments-0"}},
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "endpointslice": "payments-x1", "targetref_kind": "Pod", "targetref_name": "payments-1", "targetref_namespace": "pay", "hostname": "payments-1"}},
	)

	tp := parseTopology(podVec, nil, nil, nil, nil, svcVec, epEndpointsVec, epLabelsVec)

	require.Contains(t, tp.ServicesByNameNS, serviceKey{"c-a", "pay", "payments"})
	assert.Equal(t, "10.96.0.5", tp.ServicesByNameNS[serviceKey{"c-a", "pay", "payments"}].ClusterIP)
	require.Contains(t, tp.ServicesByNameNS, serviceKey{"c-a", "pay", "mongo"})
	assert.Equal(t, "None", tp.ServicesByNameNS[serviceKey{"c-a", "pay", "mongo"}].ClusterIP,
		"headless cluster_ip=None must be retained so the resolver can distinguish it")

	eps := tp.EndpointsByService[serviceKey{"c-a", "pay", "payments"}]
	require.Len(t, eps, 2, "both backing pods must resolve")
	assert.ElementsMatch(t, []string{"c-a/p1", "c-a/p2"}, []string{eps[0].Pod.ID(), eps[1].Pod.ID()})

	require.Contains(t, tp.PodsByNameNS, podNameKey{"c-a", "pay", "payments-0"})
	assert.Equal(t, "c-a/p1", tp.PodsByNameNS[podNameKey{"c-a", "pay", "payments-0"}].ID())
}

// TestParseTopology_NoServiceSeriesYieldsEmptyIndexes — absence of
// service/endpointslice series (KSM without those resources) yields empty
// indexes and never errors; PodsByNameNS is still built from kube_pod_info.
func TestParseTopology_NoServiceSeriesYieldsEmptyIndexes(t *testing.T) {
	podVec := sampleVec(model.Sample{Metric: model.Metric{"cluster": "c", "namespace": "n", "pod": "p", "uid": "u", "node": "w"}})
	tp := parseTopology(podVec, nil, nil, nil, nil, nil, nil, nil)
	assert.Empty(t, tp.ServicesByNameNS)
	assert.Empty(t, tp.EndpointsByService)
	assert.Len(t, tp.PodsByNameNS, 1)
}
