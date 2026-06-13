package build

import (
	"context"
	"fmt"
	"net/url"
	"sort"
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
	ExternalNodes []*graph.ExternalNode
	SynthPods     []*graph.PodNode
}

// ReadServiceGraph fetches service-graph series for the window and joins each
// endpoint against the supplied topology. Per D29, endpoints whose client /
// server label is a "://" connection string are resolved to in-cluster
// service nodes (which fan out service-selects-pod edges to their backing
// pods), falling back to an external node — there is no configurable pattern
// knob. The Renderer is accepted for signature symmetry
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
	endpointsByService map[serviceKey][]EndpointObs // service-selects-pod fan-out source
	podByID            map[string]*graph.PodNode    // client side: cluster known from metric
	podByUID           map[string]*graph.PodNode    // server side: cluster recovered via index
	svcCandidates      map[famSvcKey][]svcCandidate
	svcHolderFamilies  map[nsSvcKey]holderFamily // unknown-family fallback: which LOADED family/families hold (ns, svc) — "unknown"-bucketed entries excluded
	knownFamilies      map[string]struct{}       // family keys of every loaded cluster (excl. "unknown")
	epVisibleClusters  map[string]struct{}       // clusters with ANY endpoint data — pruning evidence gate
	externals          map[string]*graph.ExternalNode
	synthPods          map[string]*graph.PodNode
	services           map[string]*graph.ServiceNode // keyed by service id
	svcEdges           map[string]*graph.Edge        // service-selects-pod, keyed by "svcID|podID"
}

// famSvcKey keys the D29 fan-out candidate index: the service identity a
// "://" host classifies to, scoped by cluster family. Folding the family into
// the key makes per-endpoint resolution a direct map hit and keeps the family
// rule encoded in exactly one place (the index build).
type famSvcKey struct{ family, namespace, service string }

// nsSvcKey keys the unknown-family fallback index: the service identity a
// "://" host classifies to, across ALL loaded clusters regardless of family.
type nsSvcKey struct{ namespace, service string }

// holderFamily records which loaded family holds a (namespace, service): the
// single family key, or multi=true when two-plus families hold it (ambiguous
// for the unknown-family fallback). The candidates themselves are NOT
// duplicated here — a unique holder family's candidate slice is exactly
// svcCandidates[famSvcKey{family, ns, svc}] (a loaded family key can never be
// "unknown", so that bucket contains no pseudo-cluster entries).
type holderFamily struct {
	family string
	multi  bool
}

// svcCandidate is one cluster's deployment of a (namespace, service).
type svcCandidate struct {
	cluster string
	obs     ServiceObs
}

func parseServiceGraph(vec model.Vector, topology Topology) ServiceGraphResult {
	if len(vec) == 0 {
		return ServiceGraphResult{}
	}

	// Per-metric tally of samples missing the `cluster` label; surfaced as one
	// aggregated warn at the end of the parse.
	mc := missingClusterCounts{}

	podByID := make(map[string]*graph.PodNode, len(topology.Pods))
	for _, p := range topology.Pods {
		podByID[p.ID()] = p
	}

	// Inverted index for the D29 family fan-out: (family, namespace, service)
	// → that family's clusters deploying the service, sorted by cluster so
	// fan-out emission is a pure function of the data, independent of map
	// iteration order (D6). Built once per parse; per-"://"-endpoint
	// resolution is then a single map hit. (Filtering the sorted
	// ClustersObserved list per endpoint would also work — the index just
	// keeps the family rule in one place.)
	// svcHolderFamilies records, per bare (namespace, service), which loaded
	// family/families hold it: the unknown-family fallback consults it when
	// the anchor names no loaded family (missing "unknown"-bucketed label, or
	// a bogus trace label). "unknown"-bucketed service entries are EXCLUDED
	// here — the bucket is an absence of identity, so it must neither count as
	// a holder family in the uniqueness check (one unlabelled duplicate of a
	// prod service would flip {prod-0} to {prod-0, unknown} = ambiguous,
	// silently disabling the fallback) nor let a bogus-label anchor resolve
	// into the pseudo-cluster. They DO stay in svcCandidates: an "unknown"
	// anchor's direct family hit is the only resolution a fully-unlabelled
	// deployment has (see resolveServiceLevel for the precedence between the
	// two). Candidates are not duplicated: a unique holder family resolves
	// back through svcCandidates.
	svcCandidates := make(map[famSvcKey][]svcCandidate, len(topology.ServicesByNameNS))
	svcHolderFamilies := make(map[nsSvcKey]holderFamily, len(topology.ServicesByNameNS))
	for k, obs := range topology.ServicesByNameNS {
		family := clusterFamilyKey(k.cluster)
		key := famSvcKey{family: family, namespace: k.namespace, service: k.service}
		svcCandidates[key] = append(svcCandidates[key], svcCandidate{cluster: k.cluster, obs: obs})
		if k.cluster != "unknown" {
			gk := nsSvcKey{namespace: k.namespace, service: k.service}
			if hf, seen := svcHolderFamilies[gk]; !seen {
				svcHolderFamilies[gk] = holderFamily{family: family}
			} else if !hf.multi && hf.family != family {
				svcHolderFamilies[gk] = holderFamily{family: hf.family, multi: true}
			}
		}
	}
	for _, cands := range svcCandidates {
		sort.Slice(cands, func(i, j int) bool { return cands[i].cluster < cands[j].cluster })
	}

	// knownFamilies gates the unknown-family fallback: an anchor whose family
	// IS loaded but lacks the service keeps the external fallback (no
	// cross-family escape), while an anchor naming NO loaded family may fall
	// back to the unique family holding the service. The "unknown" bucket is
	// an absence of identity, not a cluster, so it never counts as loaded —
	// an "unknown" anchor is always eligible for the fallback.
	knownFamilies := map[string]struct{}{}
	addKnownFamily := func(cluster string) {
		if cluster == "" || cluster == "unknown" {
			return
		}
		knownFamilies[clusterFamilyKey(cluster)] = struct{}{}
	}
	for _, c := range topology.ClustersObserved {
		addKnownFamily(c)
	}
	for k := range topology.ServicesByNameNS {
		addKnownFamily(k.cluster)
	}
	for _, p := range topology.Pods {
		addKnownFamily(p.Labels()["cluster"])
	}

	// epVisibleClusters is the pruning evidence gate: a cluster with ZERO
	// EndpointsByService entries has no endpoint visibility at all (its KSM
	// lacks the kubernetes.io/service-name endpointslice allowlist, or the
	// join produced nothing) — for such a cluster "zero backing pods" is
	// absence of evidence, not evidence of absence, and its candidates are
	// never pruned.
	epVisibleClusters := map[string]struct{}{}
	for k := range topology.EndpointsByService {
		epVisibleClusters[k.cluster] = struct{}{}
	}

	res := &sgResolver{
		endpointsByService: topology.EndpointsByService,
		podByID:            podByID,
		podByUID:           topology.PodsByUID,
		svcCandidates:      svcCandidates,
		svcHolderFamilies:  svcHolderFamilies,
		knownFamilies:      knownFamilies,
		epVisibleClusters:  epVisibleClusters,
		externals:          map[string]*graph.ExternalNode{},
		synthPods:          map[string]*graph.PodNode{},
		services:           map[string]*graph.ServiceNode{},
		svcEdges:           map[string]*graph.Edge{},
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
		// Drop zero-rate series. Written as !(v > 0) rather than v <= 0 so
		// NaN-valued samples are dropped too — every comparison with NaN is
		// false in Go, so `s.Value <= 0` would let NaN through and materialise
		// nodes/edges for traffic that never happened.
		if !(s.Value > 0) {
			continue
		}

		clientLabel := string(s.Metric["client"])
		serverLabel := string(s.Metric["server"])
		// Single `cluster` label = trace source / client-side cluster.
		traceCluster := mc.bucket(promql.QServiceGraphTotal, string(s.Metric["cluster"]))
		clientUID := string(s.Metric["client_k8s_pod_uid"])
		serverUID := string(s.Metric["server_k8s_pod_uid"])
		clientNS := string(s.Metric["client_k8s_namespace_name"])
		serverNS := string(s.Metric["server_k8s_namespace_name"])

		clientUID, serverUID = normalizeSelfLoopUIDs(clientUID, serverUID, clientLabel, serverLabel)

		// Drop the series BEFORE any resolution when either side is wholly
		// empty (no UID, no label): no edge can exist, and resolving the
		// other side anyway would leak its materialisation side effects
		// (service / external nodes plus service-selects-pod fan-out) as an
		// orphan subgraph — amplified by family size under the D29 fan-out.
		if (clientUID == "" && clientLabel == "") || (serverUID == "" && serverLabel == "") {
			continue
		}

		// Each side resolves to a SET of node IDs: a "://" endpoint may fan out
		// to one service node per family cluster holding the addressed
		// (namespace, service) (D29 cluster-family fan-out); every other path
		// yields exactly one ID, and an empty set drops the side (and with it
		// the series — the cross product below is then empty).
		srcIDs, srcIsPod := res.resolveClient(clientLabel, traceCluster, clientUID, clientNS)

		// Family scope for the server-side "://" path prefers the
		// UID-recovered client-pod cluster over the raw trace label: the label
		// is frequently missing (bucketed to "unknown") or disagrees with
		// topology (see resolveClient), and `.svc` DNS is in-cluster relative
		// to the CALLER — whose authoritative cluster is the resolved pod's,
		// not the label's. Falls back to the trace label when the client side
		// is not a topology pod. Edge labels.cluster is unaffected (still the
		// raw trace label, per D9).
		familyCluster := traceCluster
		if srcIsPod && len(srcIDs) == 1 {
			if pod, ok := res.podByID[srcIDs[0]]; ok {
				if c := pod.Labels()["cluster"]; c != "" {
					familyCluster = c
				}
			}
		}
		tgtIDs := res.resolveServer(serverLabel, familyCluster, serverUID, serverNS)

		// Cross product: any resolved source × any resolved target. The two
		// sides' ambiguities are independent — when both are "://" sets, any
		// family deployment of the source service may call any family
		// deployment of the target service.
		for _, srcID := range srcIDs {
			for _, tgtID := range tgtIDs {
				// Deterministic dedupe: multiple upstream series can resolve to the
				// same (src, tgt) pair while carrying different trace `cluster`
				// labels (e.g. one missing → "unknown", the client pod recovered via
				// the cluster-agnostic UID index). betterSrcCluster picks the most
				// informative label deterministically so the emitted edge's
				// labels.cluster is a pure function of the data, not vector arrival
				// order (D6). srcIsPod is identical for a given srcID, so only
				// srcCluster needs the tie-break.
				key := pairKey{src: srcID, tgt: tgtID}
				if prev, dup := pairs[key]; !dup || betterSrcCluster(traceCluster, prev.srcCluster) {
					pairs[key] = aggEdge{srcIsPod: srcIsPod, srcCluster: traceCluster}
				}
			}
		}
	}

	edges := make([]*graph.Edge, 0, len(pairs)+len(res.svcEdges))
	for k, agg := range pairs {
		// Edge `cluster` label is the trace-source / client-side cluster, but
		// only when the client side is a pod (per design D9). A client "://"
		// label resolves to a service or external node (never a pod), so such
		// an edge never carries cluster.
		labels := map[string]string{}
		if agg.srcIsPod {
			labels["cluster"] = agg.srcCluster
		}
		// Edge type is target-driven: a target that resolved to a service node
		// (via the D29 "://" connection-string rule) yields pod-calls-service;
		// every other target (pod, synth-pod, external) stays pod-calls-pod.
		edgeType := graph.EdgeTypePodCallsPod
		if _, isSvc := res.services[k.tgt]; isSvc {
			edgeType = graph.EdgeTypePodCallsService
		}
		edges = append(edges, graph.NewEdge(edgeType, k.src, k.tgt, labels))
	}
	for _, e := range res.svcEdges {
		edges = append(edges, e)
	}

	out := ServiceGraphResult{
		Edges:         edges,
		ServiceNodes:  make([]*graph.ServiceNode, 0, len(res.services)),
		ExternalNodes: make([]*graph.ExternalNode, 0, len(res.externals)),
		SynthPods:     make([]*graph.PodNode, 0, len(res.synthPods)),
	}
	for _, sv := range res.services {
		out.ServiceNodes = append(out.ServiceNodes, sv)
	}
	for _, ext := range res.externals {
		out.ExternalNodes = append(out.ExternalNodes, ext)
	}
	for _, sp := range res.synthPods {
		out.SynthPods = append(out.SynthPods, sp)
	}

	mc.warn()

	return out
}

// betterSrcCluster reports whether next should replace prev as a duplicate
// (src, tgt) pair's edge labels.cluster. An identified cluster always beats
// the "unknown" missing-label bucket — a sibling series that carried the real
// trace-source cluster is strictly more informative, and plain lexical order
// would let "unknown" win against any real name sorting after it ("us-east-1",
// "v…", …). Among real names (or two "unknown"s) the lexically-smaller wins.
// Pure and order-free, so the pick stays a deterministic function of the data
// (D6) regardless of vector arrival order.
func betterSrcCluster(next, prev string) bool {
	if next == prev {
		return false
	}
	if prev == "unknown" {
		return true
	}
	if next == "unknown" {
		return false
	}
	return next < prev
}

// isConnString reports whether a client/server label is a "://" connection
// string (D29) rather than a workload name or pod UID. It is the single
// definition of the connection-string discriminator, shared by resolveEmptyUID
// (Stage 0 routing) and normalizeSelfLoopUIDs (D33) so the two can never drift.
func isConnString(label string) bool { return strings.Contains(label, "://") }

// normalizeSelfLoopUIDs implements the D33 self-loop UID guard. Some
// service-graph exporters stamp BOTH client_k8s_pod_uid and server_k8s_pod_uid
// with the SAME pod UID for a peer they could only identify as a "://"
// connection string (the real remote lives in the client/server label, not in
// a pod UID). A non-empty UID normally short-circuits D29 Stage 0
// (resolveEmptyUID), so the "://" side would collapse onto the caller's own pod
// — a self-loop pod-calls-pod edge — and no service node would ever
// materialise. When the two UIDs collide (non-empty and equal), the UID on any
// "://" side is bogus and is cleared so that side falls through to
// connection-string resolution; the non-"://" side keeps the shared UID and
// resolves to its real pod.
func normalizeSelfLoopUIDs(clientUID, serverUID, clientLabel, serverLabel string) (string, string) {
	if clientUID == "" || clientUID != serverUID {
		return clientUID, serverUID
	}
	if isConnString(clientLabel) {
		clientUID = ""
	}
	if isConnString(serverLabel) {
		serverUID = ""
	}
	return clientUID, serverUID
}

// resolveEmptyUID resolves an endpoint that carries no pod UID — the shared
// prologue for both the client and server sides. Per the D29 resolution order:
//  1. a "://" label runs connection-string resolution (Stage 0: services / external)
//  3. a non-URL label promotes to an external node (D27 fallback)
//  4. a wholly empty endpoint drops
//
// (Step 2, pod-UID resolution, is the caller's responsibility and only runs
// for non-empty UIDs.) A no-UID endpoint resolves to service or external
// nodes, never a pod. Only Stage 0 can yield more than one ID (one service
// node per family cluster holding the addressed service); the D27 fallback
// yields exactly one and a wholly empty endpoint yields nil (drop).
func (r *sgResolver) resolveEmptyUID(label, familyCluster string) []string {
	if isConnString(label) {
		return r.resolveConnString(label, familyCluster) // Stage 0 — services or external, never a pod
	}
	if label != "" {
		return []string{r.external(label)} // D27 fallback (non-URL only)
	}
	return nil // drop
}

// resolveClient resolves the client side of a service-graph series. Returns
// (ids, isPod). isPod is true when the resolved endpoint is a pod — real or
// synthesised from a non-empty UID; a pod is always exactly one ID, so
// srcIsPod is uniform across the returned set. A "://" connection string
// resolves to service or external nodes (never a pod) and is the only path
// that can return more than one ID. The client side knows its cluster from
// the metric's `cluster` label.
func (r *sgResolver) resolveClient(label, traceCluster, podUID, namespace string) ([]string, bool) {
	if podUID == "" {
		return r.resolveEmptyUID(label, traceCluster), false
	}
	id := graph.PodID(traceCluster, podUID)
	if _, ok := r.podByID[id]; ok {
		return []string{id}, true
	}
	// The trace's `cluster` label is frequently missing (bucketed to "unknown")
	// or disagrees with the client pod's real topology cluster, so the
	// cluster-scoped podByID lookup misses even though the pod exists. Recover
	// the real pod via the global UID index — symmetric with resolveServer —
	// before minting a ghost, otherwise every client pod in a no-cluster-label
	// deployment would duplicate as an "unknown/<uid>" synth node. Only
	// synthesise when the UID is unknown to BOTH indexes.
	if pod, ok := r.podByUID[podUID]; ok {
		return []string{pod.ID()}, true
	}
	r.synthPod(id, traceCluster, namespace, podUID)
	return []string{id}, true
}

// resolveServer mirrors resolveClient. The metric does not carry server-side
// cluster, so pod-UID resolution recovers it via the global UID index; the
// connection-string path resolves across familyCluster's family
// (`.svc.cluster.local` names route to any family member under mesh routing).
// familyCluster is the caller's authoritative cluster: the UID-recovered
// client-pod cluster when the client side resolved to a topology pod, else
// the raw trace label (bucketed to "unknown" when missing).
func (r *sgResolver) resolveServer(label, familyCluster, podUID, namespace string) []string {
	if podUID == "" {
		return r.resolveEmptyUID(label, familyCluster)
	}
	if pod, ok := r.podByUID[podUID]; ok {
		return []string{pod.ID()}
	}
	r.synthPod(graph.PodID("", podUID), "", namespace, podUID) // server cluster unknown
	return []string{graph.PodID("", podUID)}
}

// resolveConnString implements D29 Stage 0 for a label containing "://". Every
// recognised in-cluster reference resolves to Service nodes — both the
// <service>.<namespace> form and the headless <pod-hostname>.<service>.<namespace>
// form resolve to the same (service, namespace), looked up across
// familyCluster's family (widened by the unknown-family fallback and narrowed
// by endpoint-backed pruning — see resolveServiceLevel); each surviving match
// fans out service-selects-pod edges to its own cluster's backing pods. An
// unparseable host, a non-2/3-label name, or zero surviving candidates falls
// back to an external node. The result is therefore never a pod — Stage 0
// yields service nodes or a single external node.
//
// Deliberately NOT memoised: resolution is a url.Parse plus a couple of map
// hits (µs-scale, dwarfed by the upstream fetch), materialisation is
// idempotent (services / externals / svcEdges all dedupe), and a cache keyed
// on (familyCluster, label) is exactly the kind of collision-prone state a
// pure per-parse function does not need.
func (r *sgResolver) resolveConnString(label, familyCluster string) []string {
	if host := connStringHost(label); host != "" {
		if svc, ns, ok := classifyK8sDNS(host); ok {
			if ids := r.resolveServiceLevel(familyCluster, ns, svc); len(ids) > 0 {
				return ids
			}
		}
	}
	// Unresolvable: not a parseable host, not a 2/3-label k8s .svc name, or
	// zero candidates survive resolution (family miss without an eligible
	// unique-family fallback) → external node (labels={}, verbatim label as
	// name). Keeps truly-external URLs, unknown in-cluster names, and
	// cross-family-ambiguous names visible.
	return []string{r.external(label)}
}

// resolveServiceLevel resolves a `<service>.<namespace>` record to service
// nodes (materialising their service-selects-pod edges), scoped to
// familyCluster's FAMILY: every cluster whose clusterFamilyKey equals
// familyCluster's AND which holds the (namespace, service) materialises its
// own service node — familyCluster itself is not special, just a family
// member. Candidates come from the per-parse svcCandidates index (sorted by
// cluster at build, so the result is a pure function of the data — D6
// determinism).
//
// Two refinements on the raw family lookup:
//   - Unknown-family fallback: when the anchor's family is not the family of
//     ANY loaded cluster (the "unknown" bucket, or a bogus trace label), the
//     (namespace, service) is looked up across all LOADED clusters and
//     resolves iff exactly one family holds it (two-plus families is
//     ambiguous → nil → external). When NO loaded cluster holds the name, an
//     "unknown" anchor keeps its direct family hit on "unknown"-bucketed
//     service entries — the only resolution a fully-unlabelled deployment
//     has; identified holders always shadow that pseudo-cluster hit. A LOADED
//     family that merely lacks the service does NOT escape across families.
//   - Endpoint-backed pruning: candidates with zero backing pods are skipped
//     when at least one candidate is endpoint-backed (a fleet-wide Service
//     object whose pods run in one cluster must not fan out edges to its
//     endpointless siblings). Unbackedness counts only as evidence in
//     clusters with endpoint visibility (see endpointBacked); all-unbacked
//     candidate sets resolve unpruned.
//
// An empty result means the endpoint is unresolvable and the caller falls
// back to an external node.
func (r *sgResolver) resolveServiceLevel(familyCluster, ns, svc string) []string {
	family := clusterFamilyKey(familyCluster)
	cands := r.svcCandidates[famSvcKey{family: family, namespace: ns, service: svc}]
	if _, known := r.knownFamilies[family]; !known {
		// Unanchorable anchor. A non-empty cands here can only be
		// "unknown"-bucketed entries (a loaded family's key would be known) —
		// identified holders take precedence over that pseudo-cluster hit.
		if holders, anyLoaded := r.loadedUniqueFamilyHolders(ns, svc); anyLoaded {
			cands = holders
		}
	}
	cands = r.endpointBacked(cands, ns, svc)
	ids := make([]string, 0, len(cands))
	for _, cand := range cands {
		ids = append(ids, r.materializeService(cand.cluster, ns, svc, cand.obs))
	}
	return ids
}

// loadedUniqueFamilyHolders implements the unknown-family fallback lookup:
// the LOADED clusters holding (namespace, service) — "unknown"-bucketed
// entries are excluded at svcHolderFamilies build time. anyLoaded reports
// whether any loaded cluster holds the name at all; holders is non-nil ONLY
// when they all belong to a single family (two-plus families is ambiguous —
// no anchor exists to disambiguate — and resolves to nil → external). A
// unique holder family's candidates are exactly its svcCandidates bucket
// (already sorted at build; a loaded family key is never "unknown", so the
// bucket holds no pseudo-cluster entries) — the uniqueness check is an
// order-free set property (D6).
func (r *sgResolver) loadedUniqueFamilyHolders(ns, svc string) ([]svcCandidate, bool) {
	hf, ok := r.svcHolderFamilies[nsSvcKey{namespace: ns, service: svc}]
	if !ok {
		return nil, false
	}
	if hf.multi {
		return nil, true // held by ≥2 families: ambiguous
	}
	return r.svcCandidates[famSvcKey{family: hf.family, namespace: ns, service: svc}], true
}

// endpointBacked applies endpoint-backed pruning: when at least one candidate
// has backing pods in its own cluster's EndpointsByService entry, candidates
// PROVABLY without backing pods are dropped (no service node, no edge, no
// fan-out). "Provably" requires endpoint visibility: a candidate whose whole
// cluster has zero endpoint data (epVisibleClusters miss — endpointslice
// allowlist absent there, staged rollout, join failure) is KEPT, because its
// zero is absence of evidence, not evidence of absence. When NO candidate is
// backed the full set is returned unchanged — a known service with zero
// endpoints anywhere still materialises (deliberate operator signal), and
// deployments without the kubernetes.io/service-name endpointslice allowlist
// keep full connection-string resolution. Pure order-preserving filter over a
// sorted slice (D6).
func (r *sgResolver) endpointBacked(cands []svcCandidate, ns, svc string) []svcCandidate {
	anyBacked := false
	for _, c := range cands {
		if len(r.endpointsByService[serviceKey{c.cluster, ns, svc}]) > 0 {
			anyBacked = true
			break
		}
	}
	if !anyBacked {
		return cands
	}
	kept := make([]svcCandidate, 0, len(cands))
	for _, c := range cands {
		if len(r.endpointsByService[serviceKey{c.cluster, ns, svc}]) > 0 {
			kept = append(kept, c)
			continue
		}
		if _, visible := r.epVisibleClusters[c.cluster]; !visible {
			kept = append(kept, c)
		}
	}
	return kept
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
	for _, ep := range r.endpointsByService[serviceKey{cluster, ns, svc}] {
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

func (r *sgResolver) synthPod(id, cluster, namespace, podUID string) {
	if existing, ok := r.synthPods[id]; ok {
		// Deterministic dedupe: the same synth-pod id can arrive again with a
		// different namespace label (conflicting upstream series in arbitrary
		// vector order). Keep the lexically-smaller namespace so the node's
		// content is a pure function of the data, not arrival order (D6). The
		// node is build-local and unpublished, so mutating its label map is safe.
		existingNS := existing.LabelsValue["namespace"]
		if namespace != "" && (existingNS == "" || namespace < existingNS) {
			existing.LabelsValue["namespace"] = namespace
		}
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

// classifyK8sDNS matches a host against Kubernetes Service DNS grammar and
// returns the addressed (service, namespace). It strips an optional trailing
// ".svc.<cluster-domain>" (or ".svc") and resolves BOTH the service form
// <service>.<namespace> and the headless pod form
// <pod-hostname>.<service>.<namespace> to the same (service, namespace): every
// recognised "://" in-cluster reference resolves to its Service node, which
// fans out to all backing pods (D29). ok is false when the service-relative
// part is not 2 or 3 dotted labels.
func classifyK8sDNS(host string) (service, namespace string, ok bool) {
	rel := host
	// "svc" is a legal DNS-1123 label, so a namespace or service literally
	// named "svc" must not confuse the suffix strip. The bare-suffix check
	// runs FIRST: an FQDN never ends in ".svc", but a bare form like
	// "myservice.svc.svc" (service in a namespace named "svc") contains an
	// interior ".svc." that would otherwise truncate the name too early. For
	// FQDNs the cluster-domain suffix is then the LAST ".svc." occurrence
	// (e.g. "myservice.svc.svc.cluster.local") — a first-occurrence
	// strings.Index would truncate those too early as well.
	if strings.HasSuffix(host, ".svc") {
		rel = strings.TrimSuffix(host, ".svc")
	} else if i := strings.LastIndex(host, ".svc."); i >= 0 {
		rel = host[:i]
	}
	parts := strings.Split(rel, ".")
	switch len(parts) {
	case 2: // <service>.<namespace>
		return parts[0], parts[1], true
	case 3: // <pod-hostname>.<service>.<namespace> → resolve to its service
		return parts[1], parts[2], true
	default:
		return "", "", false
	}
}
