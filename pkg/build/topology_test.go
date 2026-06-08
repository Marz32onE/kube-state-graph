package build

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/pkg/graph"
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
	tp := parseTopology(topologyVectors{Pod: vec})
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
	tp := parseTopology(topologyVectors{Pod: vec})
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
	tp := parseTopology(topologyVectors{Pod: vec})
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
	tp := parseTopology(topologyVectors{Pod: vec})
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
	tp := parseTopology(topologyVectors{Pod: vec})
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
	tp := parseTopology(topologyVectors{Node: nodeVec, Addr: addrVec, NodeLabels: labelVec})
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
// label_kubernetes_io_service_name, endpoint→pod via targetref).
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
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "endpointslice": "payments-x1", "targetref_kind": "Pod", "targetref_name": "payments-0", "targetref_namespace": "pay"}},
		model.Sample{Metric: model.Metric{"cluster": "c-a", "namespace": "pay", "endpointslice": "payments-x1", "targetref_kind": "Pod", "targetref_name": "payments-1", "targetref_namespace": "pay"}},
	)

	tp := parseTopology(topologyVectors{Pod: podVec, Service: svcVec, EpEndpoints: epEndpointsVec, EpLabels: epLabelsVec})

	require.Contains(t, tp.ServicesByNameNS, serviceKey{"c-a", "pay", "payments"})
	assert.Equal(t, "10.96.0.5", tp.ServicesByNameNS[serviceKey{"c-a", "pay", "payments"}].ClusterIP)
	require.Contains(t, tp.ServicesByNameNS, serviceKey{"c-a", "pay", "mongo"})
	assert.Equal(t, "None", tp.ServicesByNameNS[serviceKey{"c-a", "pay", "mongo"}].ClusterIP,
		"headless cluster_ip=None must be retained so the resolver can distinguish it")

	eps := tp.EndpointsByService[serviceKey{"c-a", "pay", "payments"}]
	require.Len(t, eps, 2, "both backing pods must resolve")
	assert.ElementsMatch(t, []string{"c-a/p1", "c-a/p2"}, []string{eps[0].Pod.ID(), eps[1].Pod.ID()})
}

// TestParseTopology_PodOwnerAttribute — D34 controller-owner resolution with the
// ReplicaSet skipped to its owning Deployment. Covers: RS→Deployment, bare RS,
// a direct non-RS controller, a pod with no controller owner, and total absence
// of the owner series.
func TestParseTopology_PodOwnerAttribute(t *testing.T) {
	pod := func(cluster, ns, name, uid string) model.Sample {
		return model.Sample{Metric: model.Metric{
			"cluster": model.LabelValue(cluster), "namespace": model.LabelValue(ns),
			"pod": model.LabelValue(name), "uid": model.LabelValue(uid), "node": "w0",
		}}
	}
	owner := func(cluster, ns, name, kind, ownerName, ctrl string) model.Sample {
		return model.Sample{Metric: model.Metric{
			"cluster": model.LabelValue(cluster), "namespace": model.LabelValue(ns),
			"pod": model.LabelValue(name), "owner_kind": model.LabelValue(kind),
			"owner_name": model.LabelValue(ownerName), "owner_is_controller": model.LabelValue(ctrl),
		}}
	}
	rsOwner := func(cluster, ns, rs, ownerName string) model.Sample {
		return model.Sample{Metric: model.Metric{
			"cluster": model.LabelValue(cluster), "namespace": model.LabelValue(ns),
			"replicaset": model.LabelValue(rs), "owner_kind": "Deployment", "owner_name": model.LabelValue(ownerName),
		}}
	}

	podVec := sampleVec(
		pod("c", "shop", "checkout-1", "u1"), // RS → Deployment
		pod("c", "shop", "adhoc-1", "u2"),    // bare RS (no Deployment owner)
		pod("c", "logs", "fluentd-x", "u3"),  // DaemonSet (direct)
		pod("c", "shop", "static-1", "u4"),   // no controller owner
	)
	ownerVec := sampleVec(
		owner("c", "shop", "checkout-1", "ReplicaSet", "checkout-7f9c", "true"),
		owner("c", "shop", "adhoc-1", "ReplicaSet", "adhoc-rs", "true"),
		owner("c", "logs", "fluentd-x", "DaemonSet", "fluentd", "true"),
		// static-1: only a non-controller owner ref → must be ignored.
		owner("c", "shop", "static-1", "Node", "w0", "false"),
	)
	rsOwnerVec := sampleVec(rsOwner("c", "shop", "checkout-7f9c", "checkout"))

	tp := parseTopology(topologyVectors{Pod: podVec, PodOwner: ownerVec, ReplicaSetOwner: rsOwnerVec})
	byName := map[string]*graph.Owner{}
	for _, p := range tp.Pods {
		byName[p.Name()] = p.Owner()
	}

	require.NotNil(t, byName["checkout-1"], "checkout-1 must carry an owner")
	assert.Equal(t, "Deployment", byName["checkout-1"].Kind, "ReplicaSet must be skipped to its Deployment")
	assert.Equal(t, "checkout", byName["checkout-1"].Name)

	require.NotNil(t, byName["adhoc-1"])
	assert.Equal(t, "ReplicaSet", byName["adhoc-1"].Kind, "bare ReplicaSet with no Deployment owner stays as-is")
	assert.Equal(t, "adhoc-rs", byName["adhoc-1"].Name)

	require.NotNil(t, byName["fluentd-x"])
	assert.Equal(t, "DaemonSet", byName["fluentd-x"].Kind, "non-RS controller surfaced verbatim")
	assert.Equal(t, "fluentd", byName["fluentd-x"].Name)

	assert.Nil(t, byName["static-1"], "pod with no controller owner must carry no owner (nil, not empty)")

	// owner_* must NEVER leak into labels — it lives on the typed Owner attribute.
	for _, p := range tp.Pods {
		_, k := p.Labels()["owner_kind"]
		_, n := p.Labels()["owner_name"]
		assert.Falsef(t, k || n, "owner must not appear in labels for pod %q", p.Name())
	}

	// Owner series absent entirely → valid topology, no owner.
	tp2 := parseTopology(topologyVectors{Pod: podVec})
	for _, p := range tp2.Pods {
		assert.Nilf(t, p.Owner(), "no owner series → pod %q must carry no owner", p.Name())
	}
}

// TestParseTopology_PodOwnerDeterministic — when a pod reports multiple
// controller owners, the lexically-smallest (kind, name) wins regardless of
// the upstream vector order, so the emitted entity is stable across rebuilds.
func TestParseTopology_PodOwnerDeterministic(t *testing.T) {
	pod := model.Sample{Metric: model.Metric{"cluster": "c", "namespace": "n", "pod": "p", "uid": "u", "node": "w0"}}
	ctrlA := model.Sample{Metric: model.Metric{"cluster": "c", "namespace": "n", "pod": "p", "owner_kind": "DaemonSet", "owner_name": "a", "owner_is_controller": "true"}}
	ctrlB := model.Sample{Metric: model.Metric{"cluster": "c", "namespace": "n", "pod": "p", "owner_kind": "StatefulSet", "owner_name": "b", "owner_is_controller": "true"}}

	forward := parseTopology(topologyVectors{Pod: sampleVec(pod), PodOwner: sampleVec(ctrlA, ctrlB)})
	reverse := parseTopology(topologyVectors{Pod: sampleVec(pod), PodOwner: sampleVec(ctrlB, ctrlA)})

	require.Len(t, forward.Pods, 1)
	require.Len(t, reverse.Pods, 1)
	require.NotNil(t, forward.Pods[0].Owner())
	require.NotNil(t, reverse.Pods[0].Owner())
	assert.Equal(t, "DaemonSet", forward.Pods[0].Owner().Kind, "lexically-smallest kind wins")
	assert.Equal(t, forward.Pods[0].Owner().Kind, reverse.Pods[0].Owner().Kind, "order-independent")
	assert.Equal(t, forward.Pods[0].Owner().Name, reverse.Pods[0].Owner().Name, "order-independent")
}

// TestParseTopology_NoServiceSeriesYieldsEmptyIndexes — absence of
// service/endpointslice series (KSM without those resources) yields empty
// indexes and never errors.
func TestParseTopology_NoServiceSeriesYieldsEmptyIndexes(t *testing.T) {
	podVec := sampleVec(model.Sample{Metric: model.Metric{"cluster": "c", "namespace": "n", "pod": "p", "uid": "u", "node": "w"}})
	tp := parseTopology(topologyVectors{Pod: podVec})
	assert.Empty(t, tp.ServicesByNameNS)
	assert.Empty(t, tp.EndpointsByService)
}

// TestParseTopology_PVCStorageClass — StorageClass resolution from
// kube_persistentvolumeclaim_info, joined on (cluster, namespace, claim) to PVC
// nodes that already exist (from the binding metric). Covers: resolved (never a
// label), no matching info series (empty), and the info metric absent entirely
// (all empty, build succeeds).
func TestParseTopology_PVCStorageClass(t *testing.T) {
	binding := func(cluster, ns, pod, claim string) model.Sample {
		return model.Sample{Metric: model.Metric{
			"cluster": model.LabelValue(cluster), "namespace": model.LabelValue(ns),
			"pod": model.LabelValue(pod), "persistentvolumeclaim": model.LabelValue(claim),
		}}
	}
	info := func(cluster, ns, claim, sc string) model.Sample {
		return model.Sample{Metric: model.Metric{
			"cluster": model.LabelValue(cluster), "namespace": model.LabelValue(ns),
			"persistentvolumeclaim": model.LabelValue(claim), "storageclass": model.LabelValue(sc),
		}}
	}

	pvcVec := sampleVec(
		binding("c-a", "db", "mongo-0", "data-mongo-0"), // matched by info → gp3
		binding("c-a", "db", "redis-0", "data-redis-0"), // no matching info → empty
	)
	infoVec := sampleVec(info("c-a", "db", "data-mongo-0", "gp3"))

	tp := parseTopology(topologyVectors{PVC: pvcVec, PVCInfo: infoVec})
	byID := map[string]*graph.PVCNode{}
	for _, p := range tp.PVCs {
		byID[p.ID()] = p
	}

	mongo := byID["c-a/db/data-mongo-0"]
	require.NotNil(t, mongo)
	assert.Equal(t, "gp3", mongo.StorageClass(), "storageclass resolved from kube_persistentvolumeclaim_info")
	_, hasLabel := mongo.Labels()["storageclass"]
	assert.False(t, hasLabel, "storageclass must NEVER appear in labels")

	redis := byID["c-a/db/data-redis-0"]
	require.NotNil(t, redis)
	assert.Empty(t, redis.StorageClass(), "PVC with no matching info series carries empty StorageClass")

	// Info metric absent entirely → every PVC empty, build still succeeds.
	tp2 := parseTopology(topologyVectors{PVC: pvcVec})
	require.Len(t, tp2.PVCs, 2)
	for _, p := range tp2.PVCs {
		assert.Emptyf(t, p.StorageClass(), "no info series → PVC %q must be empty", p.ID())
	}
}

// TestParseTopology_PVCStorageClassDeterministic — a duplicate
// (cluster, namespace, claim) in kube_persistentvolumeclaim_info resolves to the
// lexically-smallest storageclass regardless of upstream vector order.
func TestParseTopology_PVCStorageClassDeterministic(t *testing.T) {
	binding := model.Sample{Metric: model.Metric{"cluster": "c", "namespace": "n", "pod": "p", "persistentvolumeclaim": "claim"}}
	scGP3 := model.Sample{Metric: model.Metric{"cluster": "c", "namespace": "n", "persistentvolumeclaim": "claim", "storageclass": "gp3"}}
	scGP2 := model.Sample{Metric: model.Metric{"cluster": "c", "namespace": "n", "persistentvolumeclaim": "claim", "storageclass": "gp2"}}

	fwd := parseTopology(topologyVectors{PVC: sampleVec(binding), PVCInfo: sampleVec(scGP3, scGP2)})
	rev := parseTopology(topologyVectors{PVC: sampleVec(binding), PVCInfo: sampleVec(scGP2, scGP3)})
	require.Len(t, fwd.PVCs, 1)
	require.Len(t, rev.PVCs, 1)
	assert.Equal(t, "gp2", fwd.PVCs[0].StorageClass(), "lexically-smallest storageclass wins")
	assert.Equal(t, fwd.PVCs[0].StorageClass(), rev.PVCs[0].StorageClass(), "order-independent")
}
