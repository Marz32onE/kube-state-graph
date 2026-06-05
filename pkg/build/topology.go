package build

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"golang.org/x/sync/errgroup"

	"github.com/marz32one/kube-state-graph/pkg/graph"
	"github.com/marz32one/kube-state-graph/pkg/promql"
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
	podIP   string
}

// serviceKey identifies a Service by its cluster-scoped namespace/name (D29).
type serviceKey struct{ cluster, namespace, service string }

// podNameKey identifies a pod by its cluster-scoped namespace/name. Used
// internally to join an endpointslice `targetref_name` to its backing pod when
// building EndpointsByService (D29).
type podNameKey struct{ cluster, namespace, pod string }

// ServiceObs carries the kube_service_info facts needed to materialise a
// ServiceNode on demand. ClusterIP is retained verbatim — the headless
// sentinel "None" distinguishes a headless service from a ClusterIP one.
type ServiceObs struct {
	ClusterIP string
}

// EndpointObs is one resolved backing pod of a Service (from
// kube_endpointslice_endpoints, joined to topology pods by targetref).
type EndpointObs struct {
	Pod *graph.PodNode
}

// Topology is the typed result of reading kube-state-metrics-style series for
// a single time window across all clusters in scope.
type Topology struct {
	Pods    []*graph.PodNode
	Nodes   []*graph.K8sNode
	PVCs    []*graph.PVCNode
	PodPVCs []PodPVCBinding

	// PodsByUID indexes every pod in Pods by its raw Kubernetes UID (without
	// the cluster prefix). K8s pod UIDs are UUIDv4 and unique across clusters
	// in practice, so this is the join key the service-graph reader uses to
	// recover the server-side cluster for `pod-calls-pod` edges (the metric
	// only carries the trace-source / client-side `cluster` label).
	//
	// On duplicate UIDs across clusters (data anomaly), the first-inserted
	// pod wins; downstream resolution would otherwise be ambiguous.
	PodsByUID map[string]*graph.PodNode

	// D29 connection-string resolution indexes. Built only when KSM exports
	// services / endpointslices (and, for the slice→service join, allowlists
	// the kubernetes.io/service-name label); empty otherwise. These are
	// INDEXES ONLY — ServiceNodes and service-selects-pod edges are
	// materialised on demand by the service-graph reader for referenced
	// services, not emitted wholesale here.
	//
	//   ServicesByNameNS   — (cluster, namespace, service) → cluster_ip facts
	//   EndpointsByService — (cluster, namespace, service) → backing pods
	ServicesByNameNS   map[serviceKey]ServiceObs
	EndpointsByService map[serviceKey][]EndpointObs

	ClustersObserved []string // sorted unique cluster values

	// RawSeriesCount records how many raw upstream series each topology query
	// returned BEFORE parsing/filtering, keyed by query name. Diagnostic only:
	// the build pipeline uses it to enrich the outside-retention error so an
	// operator can tell which upstream metric came back empty (0 raw series)
	// versus returned rows that were all filtered out (raw > 0 but parsed 0,
	// e.g. kube_pod_info samples with an empty uid).
	RawSeriesCount map[string]int
}

// ReadTopology runs the topology queries in parallel and assembles the
// result. The Renderer carries the configurable upstream metric-name prefix
// (see design.md D26) so deployments using a fork of kube-state-metrics or a
// custom exporter that re-publishes KSM-shaped series can be supported.
//
// The service / endpointslice queries (D29) are best-effort: an upstream that
// does not export them (older KSM, or KSM started without
// --resources=services,endpointslices) yields empty indexes, and "://"
// connection-string endpoints simply fall back to `external/<label>`.
func ReadTopology(ctx context.Context, q promql.Querier, r promql.Renderer, window time.Duration, end time.Time) (Topology, error) {
	var podVec, nodeVec, addrVec, pvcVec, labelVec model.Vector
	var svcVec, epEndpointsVec, epLabelsVec model.Vector
	var ownerVec, rsOwnerVec model.Vector

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QPodInfo), r.Render(promql.QPodInfo, window), end)
		podVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QNodeInfo), r.Render(promql.QNodeInfo, window), end)
		nodeVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QNodeAddresses), r.Render(promql.QNodeAddresses, window), end)
		addrVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QPVCBindings), r.Render(promql.QPVCBindings, window), end)
		pvcVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QNodeLabels), r.Render(promql.QNodeLabels, window), end)
		labelVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QServiceInfo), r.Render(promql.QServiceInfo, window), end)
		svcVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QEndpointSliceEndpoints), r.Render(promql.QEndpointSliceEndpoints, window), end)
		epEndpointsVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QEndpointSliceLabels), r.Render(promql.QEndpointSliceLabels, window), end)
		epLabelsVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QPodOwner), r.Render(promql.QPodOwner, window), end)
		ownerVec = v
		return err
	})
	g.Go(func() error {
		v, err := q.Instant(ctx, string(promql.QReplicaSetOwner), r.Render(promql.QReplicaSetOwner, window), end)
		rsOwnerVec = v
		return err
	})
	if err := g.Wait(); err != nil {
		return Topology{}, fmt.Errorf("topology fan-out: %w", err)
	}

	t := parseTopology(podVec, nodeVec, addrVec, pvcVec, labelVec, svcVec, epEndpointsVec, epLabelsVec, ownerVec, rsOwnerVec)
	t.RawSeriesCount = map[string]int{
		string(promql.QPodInfo):                len(podVec),
		string(promql.QNodeInfo):               len(nodeVec),
		string(promql.QNodeAddresses):          len(addrVec),
		string(promql.QPVCBindings):            len(pvcVec),
		string(promql.QNodeLabels):             len(labelVec),
		string(promql.QServiceInfo):            len(svcVec),
		string(promql.QEndpointSliceEndpoints): len(epEndpointsVec),
		string(promql.QEndpointSliceLabels):    len(epLabelsVec),
		string(promql.QPodOwner):               len(ownerVec),
		string(promql.QReplicaSetOwner):        len(rsOwnerVec),
	}
	return t, nil
}

func parseTopology(podVec, nodeVec, addrVec, pvcVec, labelVec, svcVec, epEndpointsVec, epLabelsVec, ownerVec, rsOwnerVec model.Vector) Topology {
	clusters := map[string]struct{}{}

	// Pod controller-owner resolution (D34), with the ReplicaSet skipped to its
	// owning Deployment. Built up-front so the per-pod assembly below can stamp
	// owner_kind / owner_name onto each pod's labels.
	podOwners := resolvePodOwners(ownerVec, rsOwnerVec)

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
		for k, v := range nodeLabels[[2]string{cluster, nodeName}] {
			labels[k] = v
		}
		var ips []string
		if ip, ok := externalIPs[[2]string{cluster, nodeName}]; ok && ip != "" {
			ips = []string{ip}
		}
		nodes = append(nodes, &graph.K8sNode{
			IDValue:        graph.K8sNodeID(cluster, nodeName),
			NameValue:      nodeName,
			LabelsValue:    labels,
			IPAddressValue: ips,
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
		podIP := string(s.Metric["pod_ip"])
		k := podKey{cluster, ns, name}
		podGroups[k] = append(podGroups[k], podObs{
			uid:     uid,
			nodeID:  graph.K8sNodeID(cluster, nodeName),
			ts:      s.Timestamp,
			labels:  labels,
			nodeRaw: nodeName,
			podIP:   podIP,
		})
		clusters[cluster] = struct{}{}
	}

	pods := make([]*graph.PodNode, 0, len(podVec))
	podsByUID := map[string]*graph.PodNode{}
	podsByNameNS := map[podNameKey]*graph.PodNode{}
	addPodToIndex := func(uid string, pod *graph.PodNode) {
		if uid == "" {
			return
		}
		if existing, dup := podsByUID[uid]; dup {
			slog.Warn("duplicate pod UID across clusters",
				"uid", uid,
				"existing_id", existing.ID(),
				"new_id", pod.ID(),
			)
			return
		}
		podsByUID[uid] = pod
	}
	for k, group := range podGroups {
		// Newest sample first; pods that churned UIDs within the window collapse
		// to the most recent observation since there is no reliable cross-UID
		// identity link (deleted pods do not back-fill metrics).
		sort.SliceStable(group, func(i, j int) bool { return group[i].ts > group[j].ts })
		// kube-state-metrics emits multiple series per pod-UID as labels evolve
		// during scheduling (e.g. node arrives after the first scrape). Merge
		// labels across same-UID samples — newer values win — so the emitted
		// PodNode reflects the most informative observation. The pod IP lives
		// outside labels and is selected separately below.
		merged := mergeSameUIDLabels(group)
		canonical := group[0]
		// Pod IP is sourced from kube_pod_info.pod_ip. Newest sample wins; if
		// the newest is empty (e.g. arrived before scheduling completed) we
		// fall back to the most recent non-empty observation.
		var podIP string
		for _, obs := range group {
			if obs.podIP != "" {
				podIP = obs.podIP
				break
			}
		}
		var ips []string
		if podIP != "" {
			ips = []string{podIP}
		}
		labels := merged[canonical.uid]
		// Stamp the controller owner (ReplicaSet skipped to its Deployment).
		// Absent when the pod has no controller owner — never empty strings.
		if o, ok := podOwners[podNameKey(k)]; ok {
			labels["owner_kind"] = o.kind
			labels["owner_name"] = o.name
		}
		canonicalPod := &graph.PodNode{
			IDValue:        graph.PodID(k.cluster, canonical.uid),
			NameValue:      k.pod,
			LabelsValue:    labels,
			IPAddressValue: ips,
		}
		pods = append(pods, canonicalPod)
		addPodToIndex(canonical.uid, canonicalPod)
		podsByNameNS[podNameKey(k)] = canonicalPod
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

	// Services (D29). kube_service_info carries cluster_ip; "None" means headless.
	servicesByNameNS := map[serviceKey]ServiceObs{}
	for _, s := range svcVec {
		cluster := bucketCluster(string(s.Metric["cluster"]))
		ns := string(s.Metric["namespace"])
		svc := string(s.Metric["service"])
		if svc == "" {
			continue
		}
		servicesByNameNS[serviceKey{cluster, ns, svc}] = ServiceObs{
			ClusterIP: string(s.Metric["cluster_ip"]),
		}
		clusters[cluster] = struct{}{}
	}

	// EndpointSlice -> owning Service name, via the kubernetes.io/service-name
	// label kube-state-metrics flattens to label_kubernetes_io_service_name
	// (requires the operator to allowlist it; absent -> the slice's endpoints
	// stay unmapped and the service falls back to external/<label> downstream).
	type sliceKey struct{ cluster, namespace, slice string }
	sliceToService := map[sliceKey]string{}
	for _, s := range epLabelsVec {
		cluster := bucketCluster(string(s.Metric["cluster"]))
		ns := string(s.Metric["namespace"])
		slice := string(s.Metric["endpointslice"])
		svc := string(s.Metric["label_kubernetes_io_service_name"])
		if slice == "" || svc == "" {
			continue
		}
		sliceToService[sliceKey{cluster, ns, slice}] = svc
		clusters[cluster] = struct{}{}
	}

	// EndpointsByService: resolve each endpoint's backing pod via
	// (cluster, targetref_namespace, targetref_name) against the loaded pods,
	// keyed by the owning service recovered from the slice->service map. This is
	// the source of the Service → backing-pod fan-out (service-selects-pod edges).
	endpointsByService := map[serviceKey][]EndpointObs{}
	for _, s := range epEndpointsVec {
		cluster := bucketCluster(string(s.Metric["cluster"]))
		ns := string(s.Metric["namespace"])
		slice := string(s.Metric["endpointslice"])
		svc, ok := sliceToService[sliceKey{cluster, ns, slice}]
		if !ok {
			continue
		}
		if kind := string(s.Metric["targetref_kind"]); kind != "" && kind != "Pod" {
			continue
		}
		targetNS := string(s.Metric["targetref_namespace"])
		if targetNS == "" {
			targetNS = ns
		}
		targetName := string(s.Metric["targetref_name"])
		if targetName == "" {
			continue
		}
		pod, ok := podsByNameNS[podNameKey{cluster, targetNS, targetName}]
		if !ok {
			continue
		}
		key := serviceKey{cluster, ns, svc}
		endpointsByService[key] = append(endpointsByService[key], EndpointObs{Pod: pod})
		clusters[cluster] = struct{}{}
	}

	clusterList := make([]string, 0, len(clusters))
	for c := range clusters {
		clusterList = append(clusterList, c)
	}
	sort.Strings(clusterList)

	return Topology{
		Pods:               pods,
		Nodes:              nodes,
		PVCs:               pvcs,
		PodPVCs:            bindings,
		PodsByUID:          podsByUID,
		ServicesByNameNS:   servicesByNameNS,
		EndpointsByService: endpointsByService,
		ClustersObserved:   clusterList,
	}
}

// ownerRef is a resolved controller owner (kind + name) for a pod.
type ownerRef struct{ kind, name string }

// resolvePodOwners builds the (cluster, namespace, pod) → controller-owner index
// from kube_pod_owner, skipping the intermediate ReplicaSet (D34): when a pod's
// controller owner is a ReplicaSet, it is resolved one level up via
// kube_replicaset_owner to the owning Deployment. A bare ReplicaSet with no
// Deployment owner keeps the ReplicaSet as the owner; any other owner kind is
// surfaced verbatim. Pods with no controller owner are simply absent from the
// returned map (the caller omits the labels rather than emitting empty strings).
//
// Pure function of the two input vectors — no ordering dependence: when a pod
// reports multiple controller owners, the lexically-smallest (kind, name) wins
// so the emitted entity is stable across rebuilds (D6 determinism).
func resolvePodOwners(ownerVec, rsOwnerVec model.Vector) map[podNameKey]ownerRef {
	// ReplicaSet → owning Deployment, keyed by (cluster, namespace, replicaset).
	// Only Deployment owners are retained; a ReplicaSet owned by anything else
	// (or nothing) is left unresolved so the pod keeps the ReplicaSet.
	rsToDeployment := make(map[podNameKey]string, len(rsOwnerVec))
	for _, s := range rsOwnerVec {
		if string(s.Metric["owner_kind"]) != "Deployment" {
			continue
		}
		cluster := bucketCluster(string(s.Metric["cluster"]))
		ns := string(s.Metric["namespace"])
		rs := string(s.Metric["replicaset"])
		dep := string(s.Metric["owner_name"])
		if rs == "" || dep == "" {
			continue
		}
		rsToDeployment[podNameKey{cluster, ns, rs}] = dep
	}

	owners := make(map[podNameKey]ownerRef, len(ownerVec))
	for _, s := range ownerVec {
		if string(s.Metric["owner_is_controller"]) != "true" {
			continue
		}
		cluster := bucketCluster(string(s.Metric["cluster"]))
		ns := string(s.Metric["namespace"])
		pod := string(s.Metric["pod"])
		kind := string(s.Metric["owner_kind"])
		name := string(s.Metric["owner_name"])
		if pod == "" || kind == "" || name == "" {
			continue
		}
		if kind == "ReplicaSet" {
			if dep, ok := rsToDeployment[podNameKey{cluster, ns, name}]; ok {
				kind, name = "Deployment", dep
			}
		}
		key := podNameKey{cluster, ns, pod}
		// Deterministic pick: lexically-smallest (kind, name) wins on collision.
		if cur, ok := owners[key]; ok {
			if kind > cur.kind || (kind == cur.kind && name >= cur.name) {
				continue
			}
		}
		owners[key] = ownerRef{kind, name}
	}
	return owners
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
// emitting multiple kube_pod_info series per UID as state evolves (e.g. node
// arrives on a later scrape).
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
