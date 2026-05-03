package build

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"golang.org/x/sync/errgroup"

	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

// PodPVCBinding records that a pod mounts a specific PVC. The reader emits
// these so the edge builder can wire pod-mounts-pvc.
type PodPVCBinding struct {
	PodID string
	PVCID string
}

// podKey groups pod samples by their cluster-scoped namespace/name. Multiple
// UIDs under one key indicate restarts.
type podKey struct{ cluster, namespace, pod string }

// podObs is one parsed kube_pod_info sample.
type podObs struct {
	uid     string
	nodeID  string
	ts      model.Time
	labels  map[string]string
	nodeRaw string
}

// Topology is the typed result of reading kube-state-metrics-style series for
// a single time window across all clusters in scope.
type Topology struct {
	Pods         []*graph.PodNode
	Nodes        []*graph.K8sNode
	PVCs         []*graph.PVCNode
	PodPVCs      []PodPVCBinding
	RestartEdges []*graph.Edge // pod-replaced-by edges from in-window pod restarts.

	ClustersObserved []string // sorted unique cluster values
}

// ReadTopology runs the five topology queries in parallel and assembles the
// result.
func ReadTopology(ctx context.Context, q *promql.Client, window time.Duration, end time.Time, allowlistRegex string) (Topology, error) {
	var podVec, nodeVec, addrVec, pvcVec, labelVec model.Vector

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QPodInfo), promql.Render(promql.QPodInfo, window, allowlistRegex), end)
		podVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QNodeInfo), promql.Render(promql.QNodeInfo, window, allowlistRegex), end)
		nodeVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QNodeAddresses), promql.Render(promql.QNodeAddresses, window, allowlistRegex), end)
		addrVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QPVCBindings), promql.Render(promql.QPVCBindings, window, allowlistRegex), end)
		pvcVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QNodeLabels), promql.Render(promql.QNodeLabels, window, allowlistRegex), end)
		labelVec = v
		return err
	})
	if err := g.Wait(); err != nil {
		return Topology{}, fmt.Errorf("topology fan-out: %w", err)
	}

	return parseTopology(podVec, nodeVec, addrVec, pvcVec, labelVec), nil
}

func parseTopology(podVec, nodeVec, addrVec, pvcVec, labelVec model.Vector) Topology {
	clusters := map[string]struct{}{}

	// External IP map: (cluster, node-name) -> IP.
	externalIPs := map[[2]string]string{}
	for _, s := range addrVec {
		cluster := bucketCluster(string(s.Metric["cluster"]))
		nodeName := string(s.Metric["node"])
		typ := string(s.Metric["type"])
		addr := string(s.Metric["address"])
		if typ == "ExternalIP" && addr != "" {
			externalIPs[[2]string{cluster, nodeName}] = addr
		}
		clusters[cluster] = struct{}{}
	}

	// K8s node label map: (cluster, node-name) -> labels (with `label_` prefix removed).
	nodeLabels := map[[2]string]map[string]string{}
	for _, s := range labelVec {
		cluster := bucketCluster(string(s.Metric["cluster"]))
		nodeName := string(s.Metric["node"])
		key := [2]string{cluster, nodeName}
		if _, ok := nodeLabels[key]; !ok {
			nodeLabels[key] = map[string]string{}
		}
		for ln, lv := range s.Metric {
			name := string(ln)
			if strings.HasPrefix(name, "label_") {
				nodeLabels[key][unflattenLabel(name)] = string(lv)
			}
		}
		clusters[cluster] = struct{}{}
	}

	// K8s nodes.
	nodes := make([]*graph.K8sNode, 0, len(nodeVec))
	for _, s := range nodeVec {
		cluster := bucketCluster(string(s.Metric["cluster"]))
		nodeName := string(s.Metric["node"])
		if nodeName == "" {
			continue
		}
		labels := map[string]string{"cluster": cluster}
		if ip, ok := externalIPs[[2]string{cluster, nodeName}]; ok {
			labels["external_ip"] = ip
		}
		for k, v := range nodeLabels[[2]string{cluster, nodeName}] {
			labels[k] = v
		}
		nodes = append(nodes, &graph.K8sNode{
			IDValue:     graph.K8sNodeID(cluster, nodeName),
			NameValue:   nodeName,
			LabelsValue: labels,
		})
		clusters[cluster] = struct{}{}
	}

	// Pods (group by (cluster, namespace, pod) for restart handling).
	podGroups := map[podKey][]podObs{}
	for _, s := range podVec {
		cluster := bucketCluster(string(s.Metric["cluster"]))
		ns := string(s.Metric["namespace"])
		name := string(s.Metric["pod"])
		uid := string(s.Metric["uid"])
		nodeName := string(s.Metric["node"])
		if uid == "" {
			continue
		}
		labels := map[string]string{
			"cluster":   cluster,
			"namespace": ns,
		}
		if nodeName != "" {
			labels["node"] = graph.K8sNodeID(cluster, nodeName)
		}
		if ip := string(s.Metric["pod_ip"]); ip != "" {
			labels["pod_ip"] = ip
		}
		if ip := string(s.Metric["host_ip"]); ip != "" {
			labels["host_ip"] = ip
		}
		k := podKey{cluster, ns, name}
		podGroups[k] = append(podGroups[k], podObs{
			uid:     uid,
			nodeID:  graph.K8sNodeID(cluster, nodeName),
			ts:      s.Timestamp,
			labels:  labels,
			nodeRaw: nodeName,
		})
		clusters[cluster] = struct{}{}
	}

	pods := make([]*graph.PodNode, 0, len(podVec))
	restartEdges := []*graph.Edge{}
	for k, group := range podGroups {
		// Newest sample first.
		sort.SliceStable(group, func(i, j int) bool { return group[i].ts > group[j].ts })
		// kube-state-metrics emits multiple series per pod-UID as labels evolve
		// during scheduling (e.g. node, pod_ip arrive after the first scrape).
		// Merge labels across same-UID samples — newer values win — so the
		// emitted PodNode reflects the most informative observation.
		merged := mergeSameUIDLabels(group)
		canonical := group[0]
		pods = append(pods, &graph.PodNode{
			IDValue:     graph.PodID(k.cluster, canonical.uid),
			NameValue:   k.pod,
			LabelsValue: merged[canonical.uid],
		})
		// Emit prior pods + replacement edges.
		seen := map[string]bool{canonical.uid: true}
		for _, prior := range group[1:] {
			if seen[prior.uid] {
				continue
			}
			seen[prior.uid] = true
			pods = append(pods, &graph.PodNode{
				IDValue:     graph.PodID(k.cluster, prior.uid),
				NameValue:   k.pod,
				LabelsValue: merged[prior.uid],
			})
			restartEdges = append(restartEdges, graph.NewEdge(
				graph.EdgeTypePodReplacedBy,
				graph.PodID(k.cluster, prior.uid),
				graph.PodID(k.cluster, canonical.uid),
				map[string]string{"cluster": k.cluster, "namespace": k.namespace, "pod": k.pod},
			))
		}
	}

	// PVCs + pod-PVC bindings.
	// Each kube_pod_spec_volumes_persistentvolumeclaims_info series wires one
	// pod to one PVC via (cluster, namespace, pod, persistentvolumeclaim).
	pvcSeen := map[string]bool{}
	pvcs := make([]*graph.PVCNode, 0, len(pvcVec))
	bindings := make([]PodPVCBinding, 0, len(pvcVec))
	canonicalPodUID := map[[3]string]string{}
	for k, group := range podGroups {
		canonicalPodUID[[3]string{k.cluster, k.namespace, k.pod}] = group[0].uid
	}
	for _, s := range pvcVec {
		cluster := bucketCluster(string(s.Metric["cluster"]))
		ns := string(s.Metric["namespace"])
		podName := string(s.Metric["pod"])
		claim := string(s.Metric["persistentvolumeclaim"])
		if claim == "" {
			claim = string(s.Metric["claim_name"])
		}
		if claim == "" {
			continue
		}
		id := graph.PVCID(cluster, ns, claim)
		if !pvcSeen[id] {
			pvcSeen[id] = true
			labels := map[string]string{"cluster": cluster, "namespace": ns}
			if vol := string(s.Metric["volume"]); vol != "" {
				labels["volume"] = vol
			}
			pvcs = append(pvcs, &graph.PVCNode{
				IDValue:     id,
				NameValue:   claim,
				LabelsValue: labels,
			})
		}
		if podName != "" {
			if uid, ok := canonicalPodUID[[3]string{cluster, ns, podName}]; ok {
				bindings = append(bindings, PodPVCBinding{
					PodID: graph.PodID(cluster, uid),
					PVCID: id,
				})
			}
		}
		clusters[cluster] = struct{}{}
	}

	clusterList := make([]string, 0, len(clusters))
	for c := range clusters {
		clusterList = append(clusterList, c)
	}
	sort.Strings(clusterList)

	return Topology{
		Pods:             pods,
		Nodes:            nodes,
		PVCs:             pvcs,
		PodPVCs:          bindings,
		RestartEdges:     restartEdges,
		ClustersObserved: clusterList,
	}
}

// bucketCluster returns "unknown" when the upstream cluster label is missing.
func bucketCluster(c string) string {
	if c == "" {
		return "unknown"
	}
	return c
}

// unflattenLabel inverts kube-state-metrics' `label_*` flattening.
//
// Examples:
//
//	"label_topology_kubernetes_io_zone" -> "topology.kubernetes.io/zone"
//	"label_kubernetes_io_arch"          -> "kubernetes.io/arch"
//	"label_app"                          -> "app"
//
// Heuristic: strip the `label_` prefix, then convert underscores to dots
// except the underscore preceding the LAST segment, which becomes a slash if
// the label key contains a domain prefix.
func unflattenLabel(flattened string) string {
	s := strings.TrimPrefix(flattened, "label_")
	// kube-state-metrics replaces invalid label-name characters with `_`.
	// We can't perfectly invert that, but the dominant case is
	// `<dns-prefix>/<segment>` where the prefix uses dots. We approximate:
	// replace all `_` with `.`, then turn the last `.` into `/` if any prior
	// `.` exists.
	withDots := strings.ReplaceAll(s, "_", ".")
	if i := strings.LastIndex(withDots, "."); i > 0 && strings.Contains(withDots[:i], ".") {
		return withDots[:i] + "/" + withDots[i+1:]
	}
	return withDots
}

// mergeSameUIDLabels returns one label map per UID, formed by merging labels
// from every sample with that UID. group is assumed sorted newest-first; older
// samples fill in keys the newer ones omit. This handles kube-state-metrics
// emitting multiple kube_pod_info series per UID as state evolves (node /
// pod_ip / host_ip arrive on later scrapes).
func mergeSameUIDLabels(group []podObs) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, obs := range group {
		merged, ok := out[obs.uid]
		if !ok {
			merged = map[string]string{}
			out[obs.uid] = merged
		}
		for k, v := range obs.labels {
			if v == "" {
				continue
			}
			if _, present := merged[k]; !present {
				merged[k] = v
			}
		}
	}
	return out
}
