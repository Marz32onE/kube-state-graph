package build

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/common/model"

	"github.com/marz32one/kube-state-graph/pkg/graph"
	"github.com/marz32one/kube-state-graph/pkg/promql"
)

// ServiceGraphResult is the typed output of the pod-service-graph reader.
type ServiceGraphResult struct {
	Edges         []*graph.Edge
	ServiceNodes  []*graph.ServiceNode
	OthersNodes   []*graph.OthersNode
	ExternalNodes []*graph.ExternalNode
	SynthPods     []*graph.PodNode
}

// ReadServiceGraph fetches service-graph series for the window and joins each
// endpoint against the supplied topology. Per D29, endpoints whose client /
// server label is a "://" connection string are resolved to in-cluster
// service / pod nodes (falling back to an others node) — there is no
// configurable pattern knob. The Renderer is accepted for signature symmetry
// with ReadTopology; the metric-name prefix is NOT applied to
// traces_service_graph_request_total (different exporter family, design.md
// D26), so r is effectively a no-op here today.
func ReadServiceGraph(
	ctx context.Context,
	q promql.Querier,
	r promql.Renderer,
	window time.Duration,
	end time.Time,
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
	return parseServiceGraph(vec, topology), nil
}

// sgResolver carries the per-build dedupe maps and topology indexes used to
// resolve service-graph endpoints into graph nodes. Service nodes and
// service-selects-pod edges are materialised on demand (only for services a
// "://" connection string actually references) and deduped here.
type sgResolver struct {
	topology  Topology
	podByID   map[string]*graph.PodNode // client side: cluster known from metric
	podByUID  map[string]*graph.PodNode // server side: cluster recovered via index
	others    map[string]*graph.OthersNode
	externals map[string]*graph.ExternalNode
	synthPods map[string]*graph.PodNode
	services  map[string]*graph.ServiceNode // keyed by service id
	svcEdges  map[string]*graph.Edge        // service-selects-pod, keyed by "svcID|podID"
}

func parseServiceGraph(vec model.Vector, topology Topology) ServiceGraphResult {
	if len(vec) == 0 {
		return ServiceGraphResult{}
	}

	podByID := make(map[string]*graph.PodNode, len(topology.Pods))
	for _, p := range topology.Pods {
		podByID[p.ID()] = p
	}

	res := &sgResolver{
		topology:  topology,
		podByID:   podByID,
		podByUID:  topology.PodsByUID,
		others:    map[string]*graph.OthersNode{},
		externals: map[string]*graph.ExternalNode{},
		synthPods: map[string]*graph.PodNode{},
		services:  map[string]*graph.ServiceNode{},
		svcEdges:  map[string]*graph.Edge{},
	}

	// Dedup pod-calls-pod by (srcID, tgtID). Multiple upstream series can
	// resolve to the same edge identity — most commonly when `connection_type`
	// differs — and edge IDs are deterministic only by (type, source, target).
	type aggEdge struct {
		srcIsPod   bool
		srcCluster string
	}
	type pairKey struct{ src, tgt string }
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

		srcID, srcIsPod := res.resolveClient(clientLabel, traceCluster, clientUID, clientNS)
		tgtID := res.resolveServer(serverLabel, traceCluster, serverUID, serverNS)

		if srcID == "" || tgtID == "" {
			continue
		}

		key := pairKey{src: srcID, tgt: tgtID}
		if _, dup := pairs[key]; dup {
			continue
		}
		pairs[key] = aggEdge{srcIsPod: srcIsPod, srcCluster: traceCluster}
	}

	edges := make([]*graph.Edge, 0, len(pairs)+len(res.svcEdges))
	for k, agg := range pairs {
		// Edge `cluster` label is the trace-source / client-side cluster, but
		// only when the client side is a pod (per design D9). A client "://"
		// label that resolved to a real headless pod counts as a pod here.
		labels := map[string]string{}
		if agg.srcIsPod {
			labels["cluster"] = agg.srcCluster
		}
		edges = append(edges, graph.NewEdge(graph.EdgeTypePodCallsPod, k.src, k.tgt, labels))
	}
	for _, e := range res.svcEdges {
		edges = append(edges, e)
	}

	out := ServiceGraphResult{
		Edges:         edges,
		ServiceNodes:  make([]*graph.ServiceNode, 0, len(res.services)),
		OthersNodes:   make([]*graph.OthersNode, 0, len(res.others)),
		ExternalNodes: make([]*graph.ExternalNode, 0, len(res.externals)),
		SynthPods:     make([]*graph.PodNode, 0, len(res.synthPods)),
	}
	for _, sv := range res.services {
		out.ServiceNodes = append(out.ServiceNodes, sv)
	}
	for _, o := range res.others {
		out.OthersNodes = append(out.OthersNodes, o)
	}
	for _, ext := range res.externals {
		out.ExternalNodes = append(out.ExternalNodes, ext)
	}
	for _, sp := range res.synthPods {
		out.SynthPods = append(out.SynthPods, sp)
	}
	return out
}

// resolveEmptyUID resolves an endpoint that carries no pod UID — the shared
// prologue for both the client and server sides. Per the D29 resolution order:
//  1. a "://" label runs connection-string resolution (Stage 0: service / pod / others)
//  3. a non-URL label promotes to an external node (D27 fallback)
//  4. a wholly empty endpoint drops
//
// (Step 2, pod-UID resolution, is the caller's responsibility and only runs
// for non-empty UIDs.) Returns (id, isPod); isPod is true only when a headless
// connection string resolved to a real pod.
func (r *sgResolver) resolveEmptyUID(label, traceCluster string) (string, bool) {
	if strings.Contains(label, "://") {
		return r.resolveConnString(label, traceCluster) // Stage 0
	}
	if label != "" {
		return r.external(label), false // D27 fallback (non-URL only)
	}
	return "", false // drop
}

// resolveClient resolves the client side of a service-graph series. Returns
// (id, isPod). isPod is true when the resolved endpoint is a pod — real,
// synthesised, or a headless connection string that resolved to a real pod.
// The client side knows its cluster from the metric's `cluster` label.
func (r *sgResolver) resolveClient(label, traceCluster, podUID, namespace string) (string, bool) {
	if podUID == "" {
		return r.resolveEmptyUID(label, traceCluster)
	}
	id := graph.PodID(traceCluster, podUID)
	if _, ok := r.podByID[id]; ok {
		return id, true
	}
	r.synthPod(id, traceCluster, namespace, podUID) // cluster known from metric
	return id, true
}

// resolveServer mirrors resolveClient. The metric does not carry server-side
// cluster, so pod-UID resolution recovers it via the global UID index; the
// connection-string path uses the trace-source cluster (`.svc.cluster.local`
// is in-cluster relative to the caller).
func (r *sgResolver) resolveServer(label, traceCluster, podUID, namespace string) string {
	if podUID == "" {
		id, _ := r.resolveEmptyUID(label, traceCluster)
		return id
	}
	if pod, ok := r.podByUID[podUID]; ok {
		return pod.ID()
	}
	r.synthPod(graph.PodID("", podUID), "", namespace, podUID) // server cluster unknown
	return graph.PodID("", podUID)
}

// resolveConnString implements D29 Stage 0 for a label containing "://".
// Returns (id, isPod). A headless connection string that resolves to a real
// pod is a pod (isPod=true); a service node or the unresolvable→others
// fallback is not.
func (r *sgResolver) resolveConnString(label, traceCluster string) (string, bool) {
	if host := connStringHost(label); host != "" {
		svc, ns, podHost, kind := classifyK8sDNS(host)
		switch kind {
		case dnsService:
			if id, ok := r.resolveServiceLevel(traceCluster, ns, svc); ok {
				return id, false
			}
		case dnsHeadlessPod:
			if id, ok := r.resolveHeadlessPod(traceCluster, ns, svc, podHost); ok {
				return id, true
			}
		case dnsNone:
			// Not a recognised k8s .svc name → falls through to the others
			// fallback below.
		}
	}
	// Unresolvable: not a parseable host, not a k8s .svc name, or absent from
	// the trace cluster's topology → others node (labels={}). Keeps
	// truly-external URLs and unknown in-cluster names visible.
	return r.othersNode(label), false
}

// resolveServiceLevel resolves a `<service>.<namespace>` record to a service
// node (materialising its service-selects-pod edges), scoped to the
// trace-source cluster. A service absent from that cluster's topology is
// unresolvable (the caller falls back to an others node). The cluster is
// always known — the reader buckets a missing trace cluster to "unknown"
// (bucketCluster), so lookups are cluster-scoped.
func (r *sgResolver) resolveServiceLevel(cluster, ns, svc string) (string, bool) {
	obs, ok := r.topology.ServicesByNameNS[serviceKey{cluster, ns, svc}]
	if !ok {
		return "", false
	}
	return r.materializeService(cluster, ns, svc, obs), true
}

// resolveHeadlessPod resolves a `<pod-hostname>.<service>.<namespace>` record
// to the specific backing pod within the trace-source cluster: first by
// endpointslice hostname (handles an arbitrary spec.hostname), then by the
// StatefulSet pod-name == hostname convention (kube-state-metrics does not
// expose spec.hostname).
func (r *sgResolver) resolveHeadlessPod(cluster, ns, svc, podHost string) (string, bool) {
	for _, ep := range r.topology.EndpointsByService[serviceKey{cluster, ns, svc}] {
		if ep.Hostname == podHost {
			return ep.Pod.ID(), true
		}
	}
	if pod, ok := r.topology.PodsByNameNS[podNameKey{cluster, ns, podHost}]; ok {
		return pod.ID(), true
	}
	return "", false
}

// materializeService creates (once) a ServiceNode and its service-selects-pod
// edges to every backing pod in the topology endpoint index.
func (r *sgResolver) materializeService(cluster, ns, svc string, obs ServiceObs) string {
	id := graph.ServiceID(cluster, ns, svc)
	if _, ok := r.services[id]; ok {
		return id
	}
	var ips []string
	if obs.ClusterIP != "" && obs.ClusterIP != "None" {
		ips = []string{obs.ClusterIP}
	}
	r.services[id] = &graph.ServiceNode{
		IDValue:        id,
		NameValue:      svc,
		LabelsValue:    map[string]string{"cluster": cluster, "namespace": ns},
		IPAddressValue: ips,
	}
	for _, ep := range r.topology.EndpointsByService[serviceKey{cluster, ns, svc}] {
		r.addServiceEdge(id, ep.Pod.ID(), ns)
	}
	return id
}

func (r *sgResolver) addServiceEdge(svcID, podID, ns string) {
	key := svcID + "|" + podID
	if _, ok := r.svcEdges[key]; ok {
		return
	}
	labels := map[string]string{}
	if ns != "" {
		labels["namespace"] = ns
	}
	r.svcEdges[key] = graph.NewEdge(graph.EdgeTypeServiceSelectsPod, svcID, podID, labels)
}

func (r *sgResolver) external(label string) string {
	id := graph.ExternalID(label)
	if _, ok := r.externals[id]; !ok {
		r.externals[id] = &graph.ExternalNode{
			IDValue:     id,
			NameValue:   label,
			LabelsValue: map[string]string{},
		}
	}
	return id
}

func (r *sgResolver) othersNode(label string) string {
	id := graph.OthersID(label)
	if _, ok := r.others[id]; !ok {
		r.others[id] = &graph.OthersNode{
			IDValue:     id,
			NameValue:   label,
			LabelsValue: map[string]string{},
		}
	}
	return id
}

func (r *sgResolver) synthPod(id, cluster, namespace, podUID string) {
	if _, ok := r.synthPods[id]; ok {
		return
	}
	labels := map[string]string{"cluster": cluster}
	if namespace != "" {
		labels["namespace"] = namespace
	}
	r.synthPods[id] = &graph.PodNode{IDValue: id, NameValue: podUID, LabelsValue: labels}
}

// connStringHost extracts the host of a "://" connection string (scheme,
// userinfo, port, and path stripped). Returns "" when unparseable.
func connStringHost(label string) string {
	u, err := url.Parse(label)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

type dnsKind int

const (
	dnsNone dnsKind = iota
	dnsService
	dnsHeadlessPod
)

// classifyK8sDNS matches a host against Kubernetes Service DNS grammar. It
// strips an optional trailing ".svc.<cluster-domain>" (or ".svc") and counts
// the dotted labels of the service-relative part:
//
//	2 labels  <service>.<namespace>                 → service-level record
//	3 labels  <pod-hostname>.<service>.<namespace>  → headless pod record
func classifyK8sDNS(host string) (service, namespace, podHostname string, kind dnsKind) {
	rel := host
	if i := strings.Index(host, ".svc."); i >= 0 {
		rel = host[:i]
	} else if strings.HasSuffix(host, ".svc") {
		rel = strings.TrimSuffix(host, ".svc")
	}
	parts := strings.Split(rel, ".")
	switch len(parts) {
	case 2:
		return parts[0], parts[1], "", dnsService
	case 3:
		return parts[1], parts[2], parts[0], dnsHeadlessPod
	default:
		return "", "", "", dnsNone
	}
}
