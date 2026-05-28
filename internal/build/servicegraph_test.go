package build

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/internal/graph"
)

func sampleTopology() Topology {
	alphaPod := &graph.PodNode{
		IDValue:     "cluster-alpha/abc",
		NameValue:   "checkout",
		LabelsValue: map[string]string{"cluster": "cluster-alpha", "namespace": "shop"},
	}
	betaPod := &graph.PodNode{
		IDValue:     "cluster-beta/def",
		NameValue:   "payments",
		LabelsValue: map[string]string{"cluster": "cluster-beta", "namespace": "billing"},
	}
	return Topology{
		Pods: []*graph.PodNode{alphaPod, betaPod},
		PodsByUID: map[string]*graph.PodNode{
			"abc": alphaPod,
			"def": betaPod,
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
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "abc",
		},
		Value: 0,
	})
	res := parseServiceGraph(vec, "", sampleTopology())
	assert.Empty(t, res.Edges)
}

func TestParseServiceGraph_CrossClusterEdge(t *testing.T) {
	// Trace produced in cluster-alpha (the client side); server pod UID `def`
	// resolves via the topology pod-UID index to a pod in cluster-beta.
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "payments",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())
	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "cluster-alpha/abc", e.Source)
	assert.Equal(t, "cluster-beta/def", e.Target, "server-side cluster recovered via UID index")
	assert.Equal(t, "cluster-alpha", e.Labels["cluster"], "edge cluster label = trace source cluster")
	for _, k := range []string{"client_cluster", "server_cluster", "rate", "p99_ms", "error_rate", "cross_cluster", "ghost"} {
		assert.NotContains(t, e.Labels, k, "unexpected label %q in v1 edge labels", k)
	}
}

func TestParseServiceGraph_IntraClusterEdge(t *testing.T) {
	// Both endpoints land in cluster-alpha.
	alphaPod1 := &graph.PodNode{
		IDValue: "cluster-alpha/abc", NameValue: "checkout",
		LabelsValue: map[string]string{"cluster": "cluster-alpha"},
	}
	alphaPod2 := &graph.PodNode{
		IDValue: "cluster-alpha/xyz", NameValue: "cart",
		LabelsValue: map[string]string{"cluster": "cluster-alpha"},
	}
	topo := Topology{
		Pods:      []*graph.PodNode{alphaPod1, alphaPod2},
		PodsByUID: map[string]*graph.PodNode{"abc": alphaPod1, "xyz": alphaPod2},
	}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "xyz",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", topo)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "cluster-alpha", res.Edges[0].Labels["cluster"])
}

func TestParseServiceGraph_OthersSubstitution_ClientSide(t *testing.T) {
	// Client side is an others endpoint (pattern-matched, D18). Server resolves
	// by UID to a pod in cluster-alpha. The edge's `cluster` label is omitted
	// (client is not a pod).
	alphaPod := &graph.PodNode{
		IDValue:     "cluster-alpha/abc",
		NameValue:   "checkout",
		LabelsValue: map[string]string{"cluster": "cluster-alpha"},
	}
	topo := Topology{
		Pods:      []*graph.PodNode{alphaPod},
		PodsByUID: map[string]*graph.PodNode{"abc": alphaPod},
	}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "http://api.example.com",
			"server":             "checkout",
			"cluster":            "",
			"client_k8s_pod_uid": "",
			"server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "://", topo)
	require.Len(t, res.Edges, 1)
	require.Len(t, res.OthersNodes, 1)
	assert.Empty(t, res.ExternalNodes, "pattern rule must NOT populate the external map")
	oth := res.OthersNodes[0]
	assert.Equal(t, "http://api.example.com", oth.NameValue)
	assert.Equal(t, "://", oth.LabelsValue["pattern"])
	assert.NotContains(t, oth.LabelsValue, "cluster", "others nodes must not carry cluster label")
	e := res.Edges[0]
	assert.Equal(t, "others/http://api.example.com", e.Source)
	assert.Equal(t, "cluster-alpha/abc", e.Target)
	assert.NotContains(t, e.Labels, "cluster", "edge cluster label MUST be omitted when client side is non-pod (others)")
	assert.NotContains(t, e.Labels, "client_cluster")
	assert.NotContains(t, e.Labels, "server_cluster")
}

func TestParseServiceGraph_OthersSubstitution_ServerSide(t *testing.T) {
	// Server side is others; client is a pod in cluster-alpha. Edge keeps
	// labels.cluster = "cluster-alpha".
	alphaPod := &graph.PodNode{
		IDValue:     "cluster-alpha/abc",
		NameValue:   "checkout",
		LabelsValue: map[string]string{"cluster": "cluster-alpha"},
	}
	topo := Topology{
		Pods:      []*graph.PodNode{alphaPod},
		PodsByUID: map[string]*graph.PodNode{"abc": alphaPod},
	}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "https://payments.partner.example/api",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "://", topo)
	require.Len(t, res.Edges, 1)
	require.Len(t, res.OthersNodes, 1)
	assert.Empty(t, res.ExternalNodes)
	e := res.Edges[0]
	assert.Equal(t, "cluster-alpha/abc", e.Source)
	assert.Equal(t, "others/https://payments.partner.example/api", e.Target)
	assert.Equal(t, "cluster-alpha", e.Labels["cluster"])
}

func TestParseServiceGraph_PatternEmpty_DisablesRule(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "http://api.example.com",
			"server":             "payments",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())
	assert.Empty(t, res.OthersNodes)
	assert.Empty(t, res.ExternalNodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "cluster-alpha/abc", res.Edges[0].Source)
}

func TestParseServiceGraph_GhostFallback_ServerUIDUnknown(t *testing.T) {
	// Server UID does not exist in topology global UID index. The synth pod's
	// cluster is empty (we cannot know the remote cluster from the metric).
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "missing",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "missing-uid",
		},
		Value: 1,
	})
	res := parseServiceGraph(vec, "", sampleTopology())
	require.Len(t, res.SynthPods, 1)
	sp := res.SynthPods[0]
	assert.Equal(t, "/missing-uid", sp.IDValue, "synth pod ID has empty cluster prefix when server cluster unknown")
	assert.Empty(t, sp.LabelsValue["cluster"], "server-side synth pod has empty cluster label")
	assert.NotContains(t, sp.LabelsValue, "ghost", "ghost label must NOT be set in v1")
}

func TestParseServiceGraph_EmptyVectorIsNotAnError(t *testing.T) {
	res := parseServiceGraph(nil, "", sampleTopology())
	assert.Empty(t, res.Edges)
}

// TestParseServiceGraph_DedupSamePair guards the edge-ID collision fix:
// multiple upstream series for the same (client, server) pair — typically
// `connection_type=virtual_node` and `connection_type=messaging_system` —
// MUST collapse into a single edge. Edge IDs are derived only from
// (type, source, target), so emitting two would produce duplicate IDs.
func TestParseServiceGraph_DedupSamePair(t *testing.T) {
	vec := sampleVec(
		model.Sample{
			Metric: model.Metric{
				"client":             "checkout",
				"server":             "payments",
				"cluster":            "cluster-alpha",
				"client_k8s_pod_uid": "abc",
				"server_k8s_pod_uid": "def",
				"connection_type":    "virtual_node",
			},
			Value: 5,
		},
		model.Sample{
			Metric: model.Metric{
				"client":             "checkout",
				"server":             "payments",
				"cluster":            "cluster-alpha",
				"client_k8s_pod_uid": "abc",
				"server_k8s_pod_uid": "def",
				"connection_type":    "messaging_system",
			},
			Value: 3,
		},
	)
	res := parseServiceGraph(vec, "", sampleTopology())
	require.Len(t, res.Edges, 1, "duplicate (src,tgt) series must collapse into one edge")

	// Edge IDs must be unique.
	ids := map[string]int{}
	for _, e := range res.Edges {
		ids[e.ID]++
	}
	for id, n := range ids {
		assert.Equal(t, 1, n, "edge id %s appeared %d times", id, n)
	}
}

// ---------------------------------------------------------------------------
// Section 31 (D27): Missing pod-UID human-label fallback.
//
// When a service-graph series has an empty pod UID for an endpoint but the
// corresponding human label (`client` or `server`) is non-empty, that side
// MUST be promoted to an external node (`external/<label>`, labels={})
// rather than dropping the edge.
// ---------------------------------------------------------------------------

func TestParseServiceGraph_MissingClientUID_PromotesToExternal(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "admin",
			"server":             "checkout",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "",
			"server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())

	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "external/admin", e.Source)
	assert.Equal(t, "cluster-alpha/abc", e.Target)
	assert.NotContains(t, e.Labels, "cluster",
		"edge cluster label MUST be omitted when client side is external (missing-UID fallback)")

	require.Len(t, res.ExternalNodes, 1)
	ext := res.ExternalNodes[0]
	assert.Equal(t, "external/admin", ext.IDValue)
	assert.Equal(t, "admin", ext.NameValue)
	assert.NotContains(t, ext.LabelsValue, "pattern",
		"missing-UID fallback must NOT carry labels.pattern (no pattern fired)")
	assert.NotContains(t, ext.LabelsValue, "cluster")
}

func TestParseServiceGraph_MissingServerUID_PromotesToExternal(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "payments",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())

	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "cluster-alpha/abc", e.Source)
	assert.Equal(t, "external/payments", e.Target)
	assert.Equal(t, "cluster-alpha", e.Labels["cluster"],
		"edge keeps labels.cluster when client side is still a pod")

	require.Len(t, res.ExternalNodes, 1)
	ext := res.ExternalNodes[0]
	assert.Equal(t, "external/payments", ext.IDValue)
	assert.Equal(t, "payments", ext.NameValue)
	assert.NotContains(t, ext.LabelsValue, "pattern")
	assert.NotContains(t, ext.LabelsValue, "cluster")
}

func TestParseServiceGraph_BothUIDsMissing_BothLabelsPresent(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "admin",
			"server":             "payments",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())

	require.Len(t, res.Edges, 1)
	e := res.Edges[0]
	assert.Equal(t, "external/admin", e.Source)
	assert.Equal(t, "external/payments", e.Target)
	assert.NotContains(t, e.Labels, "cluster",
		"edge cluster label MUST be omitted when client side is external")

	require.Len(t, res.ExternalNodes, 2)
	gotIDs := map[string]bool{}
	for _, ext := range res.ExternalNodes {
		gotIDs[ext.IDValue] = true
	}
	assert.True(t, gotIDs["external/admin"])
	assert.True(t, gotIDs["external/payments"])
}

func TestParseServiceGraph_UIDAndLabelBothEmpty_EdgeDropped_ClientSide(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "",
			"server":             "checkout",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "",
			"server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())

	assert.Empty(t, res.Edges, "edge MUST be dropped when both client UID and label are empty")
	assert.Empty(t, res.ExternalNodes, "no external node may be synthesised when client identity is wholly absent")
}

func TestParseServiceGraph_UIDAndLabelBothEmpty_EdgeDropped_ServerSide(t *testing.T) {
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "",
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", sampleTopology())

	assert.Empty(t, res.Edges, "edge MUST be dropped when both server UID and label are empty")
	assert.Empty(t, res.ExternalNodes)
}

func TestParseServiceGraph_PatternWinsOverMissingUIDFallback(t *testing.T) {
	// KSG_OTHERS_NAME_PATTERN="://" + client label contains "://" + empty UID.
	// The pattern rule fires FIRST and produces an `others` node carrying
	// labels.pattern. The missing-UID fallback (which would emit `external/`
	// with empty labels) must NOT run.
	alphaPod := &graph.PodNode{
		IDValue:     "cluster-alpha/abc",
		LabelsValue: map[string]string{"cluster": "cluster-alpha"},
	}
	topo := Topology{
		Pods:      []*graph.PodNode{alphaPod},
		PodsByUID: map[string]*graph.PodNode{"abc": alphaPod},
	}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "http://api.example.com",
			"server":             "checkout",
			"cluster":            "",
			"client_k8s_pod_uid": "",
			"server_k8s_pod_uid": "abc",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "://", topo)

	require.Len(t, res.OthersNodes, 1)
	assert.Empty(t, res.ExternalNodes, "pattern rule wins; fallback must NOT run")
	oth := res.OthersNodes[0]
	assert.Equal(t, "others/http://api.example.com", oth.IDValue)
	assert.Equal(t, "://", oth.LabelsValue["pattern"],
		"pattern rule fired first; labels.pattern proves precedence over missing-UID fallback")
}

func TestParseServiceGraph_OthersAndExternalCoexistInOneParse(t *testing.T) {
	// Disjoint dedupe maps (section 33). One parse can produce BOTH an
	// `others` node (via pattern rule) AND an `external` node (via missing-UID
	// fallback) without interference — the two maps are independent.
	//
	// Note: for the SAME label string, the pattern rule always wins (resolution
	// order 1 > 3). To exercise both code paths in a single parse, the two
	// series carry different label values: series A's client matches the
	// pattern; series B's client does not but has an empty UID.
	betaPod := &graph.PodNode{
		IDValue:     "cluster-beta/def",
		LabelsValue: map[string]string{"cluster": "cluster-beta"},
	}
	topo := Topology{
		Pods:      []*graph.PodNode{betaPod},
		PodsByUID: map[string]*graph.PodNode{"def": betaPod},
	}
	vec := sampleVec(
		model.Sample{
			Metric: model.Metric{
				"client":             "admin-cli",
				"server":             "payments",
				"cluster":            "cluster-alpha",
				"client_k8s_pod_uid": "some-uid",
				"server_k8s_pod_uid": "def",
			},
			Value: 5,
		},
		model.Sample{
			Metric: model.Metric{
				"client":             "stray-caller",
				"server":             "payments",
				"cluster":            "cluster-alpha",
				"client_k8s_pod_uid": "",
				"server_k8s_pod_uid": "def",
			},
			Value: 3,
		},
	)
	res := parseServiceGraph(vec, "admin", topo)

	require.Len(t, res.OthersNodes, 1, "pattern-matched series produces one others node")
	require.Len(t, res.ExternalNodes, 1, "missing-UID-fallback series produces one external node")
	assert.Equal(t, "others/admin-cli", res.OthersNodes[0].IDValue)
	assert.Equal(t, "admin", res.OthersNodes[0].LabelsValue["pattern"])
	assert.Equal(t, "external/stray-caller", res.ExternalNodes[0].IDValue)
	assert.Empty(t, res.ExternalNodes[0].LabelsValue, "external (fallback) carries empty labels")
}

// TestProperty_ParseServiceGraph_EveryEdgeHasNonEmptyEndpoints enforces the
// fallback invariant from D27 / spec §"Missing pod-UID human-label fallback":
// for randomised service-graph fixtures, every emitted edge MUST have
// non-empty source and target IDs. With the missing-UID fallback in place,
// a series whose endpoint UID is empty no longer drops the edge — it surfaces
// as `external/<label>`. Only series where BOTH the UID and the human label
// are empty for an endpoint may drop.
func TestProperty_ParseServiceGraph_EveryEdgeHasNonEmptyEndpoints(t *testing.T) {
	topo := sampleTopology()
	for seed := int64(1); seed <= 25; seed++ {
		r := rand.New(rand.NewSource(seed))
		samples := make([]model.Sample, 0, 20)
		for i := range 20 {
			clientUID := pickUID(r)
			serverUID := pickUID(r)
			clientLabel := pickLabel(r, "client", i)
			serverLabel := pickLabel(r, "server", i)
			// Skip series where BOTH sides are wholly unidentified — those are
			// the only legitimate drops; they would deflate the invariant
			// without exercising the fallback.
			if clientUID == "" && clientLabel == "" {
				continue
			}
			if serverUID == "" && serverLabel == "" {
				continue
			}
			samples = append(samples, model.Sample{
				Metric: model.Metric{
					"client":             model.LabelValue(clientLabel),
					"server":             model.LabelValue(serverLabel),
					"cluster":            "cluster-alpha",
					"client_k8s_pod_uid": model.LabelValue(clientUID),
					"server_k8s_pod_uid": model.LabelValue(serverUID),
				},
				Value: 5,
			})
		}
		res := parseServiceGraph(sampleVec(samples...), "", topo)
		for _, e := range res.Edges {
			require.NotEmptyf(t, e.Source, "seed=%d: edge has empty source id", seed)
			require.NotEmptyf(t, e.Target, "seed=%d: edge has empty target id", seed)
		}
	}
}

// pickUID returns one of {"abc" (resolves), "def" (resolves), "ghost-uid"
// (synth-pod fallback), ""} with roughly even probability so empty UIDs are
// well-represented in the corpus.
func pickUID(r *rand.Rand) string {
	switch r.Intn(4) {
	case 0:
		return "abc"
	case 1:
		return "def"
	case 2:
		return "ghost-uid"
	default:
		return ""
	}
}

// pickLabel returns either a fresh non-empty label or "" with 1/4 probability.
func pickLabel(r *rand.Rand, side string, i int) string {
	if r.Intn(4) == 0 {
		return ""
	}
	return fmt.Sprintf("%s-%d", side, i)
}

func TestParseServiceGraph_NoForbiddenNumericLabels(t *testing.T) {
	alphaPod1 := &graph.PodNode{
		IDValue:     "cluster-alpha/abc",
		LabelsValue: map[string]string{"cluster": "cluster-alpha"},
	}
	alphaPod2 := &graph.PodNode{
		IDValue:     "cluster-alpha/def",
		LabelsValue: map[string]string{"cluster": "cluster-alpha"},
	}
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"cluster":            "cluster-alpha",
			"client_k8s_pod_uid": "abc",
			"server_k8s_pod_uid": "def",
		},
		Value: 5,
	})
	res := parseServiceGraph(vec, "", Topology{
		Pods:      []*graph.PodNode{alphaPod1, alphaPod2},
		PodsByUID: map[string]*graph.PodNode{"abc": alphaPod1, "def": alphaPod2},
	})
	for _, e := range res.Edges {
		for _, k := range []string{"rate", "p99_ms", "error_rate"} {
			assert.NotContains(t, e.Labels, k)
		}
	}
}
