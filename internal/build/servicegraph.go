package build

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/common/model"

	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

// ServiceGraphResult is the typed output of the pod-service-graph reader.
type ServiceGraphResult struct {
	Edges         []*graph.Edge
	ExternalNodes []*graph.ExternalNode
	SynthPods     []*graph.PodNode
}

// ReadServiceGraph fetches service-graph series for the window and joins
// each endpoint against the supplied topology, applying the
// KSG_EXTERNAL_NAME_PATTERN substitution rule. The Renderer is accepted for
// signature symmetry with ReadTopology — the configurable metric-name prefix
// is NOT applied to traces_service_graph_request_total (different exporter
// family, see design.md D26), so r is effectively a no-op here today; passing
// it through future-proofs the wiring should a follow-up change introduce a
// service-graph prefix knob.
func ReadServiceGraph(
	ctx context.Context,
	q promql.Querier,
	r promql.Renderer,
	window time.Duration,
	end time.Time,
	externalPattern string,
	topology Topology,
) (ServiceGraphResult, error) {
	vec, err := q.Instant(ctx,
		string(promql.QServiceGraphTotal),
		r.Render(promql.QServiceGraphTotal, window),
		end,
	)
	if err != nil {
		return ServiceGraphResult{}, fmt.Errorf("service-graph query: %w", err)
	}
	return parseServiceGraph(vec, externalPattern, topology), nil
}

func parseServiceGraph(vec model.Vector, externalPattern string, topology Topology) ServiceGraphResult {
	if len(vec) == 0 {
		return ServiceGraphResult{}
	}

	// Topology indices for endpoint resolution. podByID is used by the client
	// side (we know its cluster from the metric's `cluster` label, so the
	// composite ID is constructible). podByUID is used by the server side —
	// the metric does not carry server-side cluster, so we look up by raw UID
	// against the global topology pod-UID index and use the resolved pod's
	// own cluster for the target ID.
	podByID := map[string]*graph.PodNode{}
	for _, p := range topology.Pods {
		podByID[p.ID()] = p
	}
	podByUID := topology.PodsByUID

	externals := map[string]*graph.ExternalNode{}
	synthPods := map[string]*graph.PodNode{}

	// Dedup by (srcID, tgtID). Multiple upstream series can resolve to the
	// same edge identity — most commonly when `connection_type` differs
	// (`virtual_node` vs `messaging_system`) — and edge IDs are deterministic
	// only by (type, source, target). Collapsing here prevents duplicate edge
	// IDs in Cytoscape / Grafana output. The surviving edge's `cluster` label
	// is the trace-source cluster of the first observation per pair, and is
	// recorded only when the source side resolved to a pod (external sources
	// have no cluster).
	type aggEdge struct {
		srcIsPod   bool
		srcCluster string
	}
	type pairKey struct {
		src string
		tgt string
	}
	pairs := make(map[pairKey]aggEdge, len(vec))

	for _, s := range vec {
		// Drop zero-rate series.
		if s.Value <= 0 {
			continue
		}

		clientLabel := string(s.Metric["client"])
		serverLabel := string(s.Metric["server"])
		// Single `cluster` label = trace source / client-side cluster.
		traceCluster := bucketCluster(string(s.Metric["cluster"]))
		clientUID := string(s.Metric["client_k8s_pod_uid"])
		serverUID := string(s.Metric["server_k8s_pod_uid"])
		clientNS := string(s.Metric["client_k8s_namespace_name"])
		serverNS := string(s.Metric["server_k8s_namespace_name"])

		// Client side: cluster is known from the metric.
		srcID, srcIsPod := resolveClientEndpoint(
			clientLabel, traceCluster, clientUID, clientNS,
			externalPattern, podByID, externals, synthPods,
		)
		// Server side: cluster is recovered from the topology pod-UID index
		// (the metric does not carry it).
		tgtID := resolveServerEndpoint(
			serverLabel, serverUID, serverNS,
			externalPattern, podByUID, externals, synthPods,
		)

		if srcID == "" || tgtID == "" {
			continue
		}

		key := pairKey{src: srcID, tgt: tgtID}
		if _, dup := pairs[key]; dup {
			continue
		}
		pairs[key] = aggEdge{srcIsPod: srcIsPod, srcCluster: traceCluster}
	}

	edges := make([]*graph.Edge, 0, len(pairs))
	for k, agg := range pairs {
		// Edge `cluster` label is the trace-source / client-side cluster, but
		// only when the client side is a pod. External clients have no cluster
		// scope, so the key is omitted entirely (per design D9 / Option A).
		labels := map[string]string{}
		if agg.srcIsPod {
			labels["cluster"] = agg.srcCluster
		}
		edges = append(edges, graph.NewEdge(
			graph.EdgeTypePodCallsPod,
			k.src,
			k.tgt,
			labels,
		))
	}

	out := ServiceGraphResult{Edges: edges}
	for _, ext := range externals {
		out.ExternalNodes = append(out.ExternalNodes, ext)
	}
	for _, sp := range synthPods {
		out.SynthPods = append(out.SynthPods, sp)
	}
	return out
}

// resolveClientEndpoint resolves the client side of a service-graph series.
// Returns (id, srcIsPod). srcIsPod is true when the resolved endpoint is a
// pod (real or synthesised), false when it is an external node.
//
// Side effects: may insert into externals or synthPods.
func resolveClientEndpoint(
	humanLabel, cluster, podUID, namespace string,
	externalPattern string,
	pods map[string]*graph.PodNode,
	externals map[string]*graph.ExternalNode,
	synthPods map[string]*graph.PodNode,
) (id string, srcIsPod bool) {
	// External substitution rule.
	if externalPattern != "" && humanLabel != "" && strings.Contains(humanLabel, externalPattern) {
		extID := graph.ExternalID(humanLabel)
		if _, ok := externals[extID]; !ok {
			externals[extID] = &graph.ExternalNode{
				IDValue:   extID,
				NameValue: humanLabel,
				LabelsValue: map[string]string{
					"pattern": externalPattern,
				},
			}
		}
		return extID, false
	}

	// Pod-UID resolution. Client side knows its cluster from the metric.
	if podUID == "" {
		// Missing pod-UID human-label fallback (D27 / spec §"Missing pod-UID
		// human-label fallback"). When the producer (typically Beyla / Alloy)
		// failed to resolve k8s.pod.uid for this endpoint but the human label
		// is still populated, promote to an external node rather than silently
		// dropping the edge. No labels payload — the endpoint is not a pod
		// (no cluster), and no pattern fired (no labels.pattern).
		if humanLabel != "" {
			extID := graph.ExternalID(humanLabel)
			if _, ok := externals[extID]; !ok {
				externals[extID] = &graph.ExternalNode{
					IDValue:     extID,
					NameValue:   humanLabel,
					LabelsValue: map[string]string{},
				}
			}
			return extID, false
		}
		return "", false
	}
	id = graph.PodID(cluster, podUID)
	if _, ok := pods[id]; ok {
		return id, true
	}
	// Synthesised pod (no topology entry for this UID).
	if _, ok := synthPods[id]; !ok {
		labels := map[string]string{"cluster": cluster}
		if namespace != "" {
			labels["namespace"] = namespace
		}
		synthPods[id] = &graph.PodNode{
			IDValue:     id,
			NameValue:   podUID,
			LabelsValue: labels,
		}
	}
	return id, true
}

// resolveServerEndpoint resolves the server side of a service-graph series.
// Unlike the client side, the metric does not carry server-side cluster, so
// the cluster is recovered by looking up the raw UID against the global
// topology pod-UID index. When the lookup misses, a synth pod is created
// with cluster="" (server-side cluster is unknown).
//
// Side effects: may insert into externals or synthPods.
func resolveServerEndpoint(
	humanLabel, podUID, namespace string,
	externalPattern string,
	podByUID map[string]*graph.PodNode,
	externals map[string]*graph.ExternalNode,
	synthPods map[string]*graph.PodNode,
) (id string) {
	// External substitution rule.
	if externalPattern != "" && humanLabel != "" && strings.Contains(humanLabel, externalPattern) {
		extID := graph.ExternalID(humanLabel)
		if _, ok := externals[extID]; !ok {
			externals[extID] = &graph.ExternalNode{
				IDValue:   extID,
				NameValue: humanLabel,
				LabelsValue: map[string]string{
					"pattern": externalPattern,
				},
			}
		}
		return extID
	}

	// Pod-UID resolution. Server side recovers its cluster via the topology
	// pod-UID index — the metric does not carry server_cluster.
	if podUID == "" {
		// Missing pod-UID human-label fallback (D27). Mirror of the client-side
		// rule — promote to external/<label> when the producer dropped the
		// UID but the human label survived.
		if humanLabel != "" {
			extID := graph.ExternalID(humanLabel)
			if _, ok := externals[extID]; !ok {
				externals[extID] = &graph.ExternalNode{
					IDValue:     extID,
					NameValue:   humanLabel,
					LabelsValue: map[string]string{},
				}
			}
			return extID
		}
		return ""
	}
	if pod, ok := podByUID[podUID]; ok {
		return pod.ID()
	}
	// Synthesised pod (no topology entry for this UID across any cluster).
	// We do not know the server's cluster, so the ID has an empty cluster
	// component and labels.cluster is empty as well.
	id = graph.PodID("", podUID)
	if _, ok := synthPods[id]; !ok {
		labels := map[string]string{"cluster": ""}
		if namespace != "" {
			labels["namespace"] = namespace
		}
		synthPods[id] = &graph.PodNode{
			IDValue:     id,
			NameValue:   podUID,
			LabelsValue: labels,
		}
	}
	return id
}
