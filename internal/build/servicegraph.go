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
// KSG_EXTERNAL_NAME_PATTERN substitution rule.
func ReadServiceGraph(
	ctx context.Context,
	q *promql.Client,
	window time.Duration,
	end time.Time,
	allowlistRegex string,
	externalPattern string,
	topology Topology,
) (ServiceGraphResult, error) {
	vec, err := q.Instant(ctx,
		string(promql.QServiceGraphTotal),
		promql.Render(promql.QServiceGraphTotal, window, allowlistRegex),
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

	// Build pod-UID lookup for fast resolution.
	podByID := map[string]*graph.PodNode{}
	for _, p := range topology.Pods {
		podByID[p.ID()] = p
	}

	externals := map[string]*graph.ExternalNode{}
	synthPods := map[string]*graph.PodNode{}

	// Dedup by (srcID, tgtID). Multiple upstream series can resolve to the
	// same edge identity — most commonly when `connection_type` differs
	// (`virtual_node` vs `messaging_system`) — and edge IDs are deterministic
	// only by (type, source, target). Collapsing here prevents duplicate edge
	// IDs in Cytoscape / Grafana output. Cluster labels of the surviving edge
	// are taken from the first observation per pair.
	type aggEdge struct {
		srcCluster string
		tgtCluster string
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
		clientCluster := bucketCluster(string(s.Metric["client_cluster"]))
		serverCluster := bucketCluster(string(s.Metric["server_cluster"]))
		clientUID := string(s.Metric["client_k8s_pod_uid"])
		serverUID := string(s.Metric["server_k8s_pod_uid"])
		clientNS := string(s.Metric["client_k8s_namespace_name"])
		serverNS := string(s.Metric["server_k8s_namespace_name"])

		srcID, srcCluster := resolveEndpoint(
			clientLabel, clientCluster, clientUID, clientNS,
			externalPattern, podByID, externals, synthPods,
		)
		tgtID, tgtCluster := resolveEndpoint(
			serverLabel, serverCluster, serverUID, serverNS,
			externalPattern, podByID, externals, synthPods,
		)

		if srcID == "" || tgtID == "" {
			continue
		}

		key := pairKey{src: srcID, tgt: tgtID}
		if _, dup := pairs[key]; dup {
			continue
		}
		pairs[key] = aggEdge{srcCluster: srcCluster, tgtCluster: tgtCluster}
	}

	edges := make([]*graph.Edge, 0, len(pairs))
	for k, agg := range pairs {
		edges = append(edges, graph.NewEdge(
			graph.EdgeTypePodCallsPod,
			k.src,
			k.tgt,
			map[string]string{
				"client_cluster": agg.srcCluster,
				"server_cluster": agg.tgtCluster,
			},
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

// resolveEndpoint returns the resolved (id, clusterLabelValue) for one side
// of a service-graph series. Side effects: may insert into externals or
// synthPods if a new node must be synthesised.
//
// clusterLabelValue is the value to write to labels.client_cluster /
// labels.server_cluster on the edge — empty string for external endpoints.
func resolveEndpoint(
	humanLabel, cluster, podUID, namespace string,
	externalPattern string,
	pods map[string]*graph.PodNode,
	externals map[string]*graph.ExternalNode,
	synthPods map[string]*graph.PodNode,
) (id, clusterLabel string) {
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
		return extID, ""
	}

	// Pod-UID resolution.
	if podUID == "" {
		return "", ""
	}
	id = graph.PodID(cluster, podUID)
	if _, ok := pods[id]; ok {
		return id, cluster
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
	return id, cluster
}
