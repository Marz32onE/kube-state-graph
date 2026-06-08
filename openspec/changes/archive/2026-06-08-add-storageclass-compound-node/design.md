## Context

PVC nodes today are built solely from `kube_pod_spec_volumes_persistentvolumeclaims_info`
(the pod→PVC binding metric), which carries `cluster, namespace, pod, volume,
claim_name` but **no StorageClass**. The StorageClass lives on a separate
kube-state-metrics default series, `kube_persistentvolumeclaim_info`
(`cluster, namespace, persistentvolumeclaim, storageclass, ...`), which ksg does
not yet read. A `storage_class` edge label was specified in the original design
but never populated and was removed as dead (F24).

The graph is rendered as Cytoscape compound nodes. The serialiser
(`pkg/cytoscape/cytoscape.go`) already synthesises one `type="cluster"` group node
per emitted cluster and nests `cluster > node > pod` (via each pod's `labels.node`,
with a fallback to the cluster group) and `cluster > {service, pvc}` (D31). These
group nodes are **serialiser-synthesised DTOs**, not `graph.GraphNode`s, computed
*after* projection from the surviving node set — so they can never dangle.

This change adds StorageClass resolution and a second compound dimension for PVCs:
`cluster > storageclass > pvc`, falling back to `cluster > pvc` when a PVC has no
resolved StorageClass.

Constraints carried from the codebase contracts (CLAUDE.md):
- `labels` is a strict `map[string]string` of typological metadata — scalars like
  StorageClass do **not** belong there (precedent: `ipaddress` D28, `owner` D34).
- The serialiser MUST NOT type-switch on concrete node types — access goes through
  the sealed `GraphNode` interface methods.
- The response body MUST be byte-deterministic (sorted, no time/echo fields).
- `KSG_METRIC_PREFIX` is prepended only to KSM-shaped series.
- `pkg/*` MUST NOT import `internal/*`; the engine stays externally importable.

## Goals / Non-Goals

**Goals:**
- Resolve each existing PVC node's StorageClass from `kube_persistentvolumeclaim_info`,
  joined on `(cluster, namespace, claim)`, degrading gracefully when the metric is
  absent.
- Introduce a presentation-only `type="storageclass"` compound group node and
  re-parent PVCs to `cluster > storageclass > pvc`, with `cluster > pvc` fallback.
- Keep StorageClass off the wire as a PVC attribute: no `data.storageclass`, nothing
  in `labels`. The class name surfaces only via the group node and `data.parent`.
- Preserve determinism and the no-dangling-parent invariant.

**Non-Goals:**
- Materialising PVC nodes that no pod mounts. `kube_persistentvolumeclaim_info` is
  used **only to enrich PVC nodes that already exist** (from the binding metric);
  it never creates new PVC nodes.
- Exposing StorageClass as a filterable query parameter (no `?storageclass=`),
  numeric/extra PVC metrics, or any new edge type.
- Reviving the removed `storage_class` edge label (F24 stays reverted).
- Changing projection/traversal semantics — the new grouping is presentation-only.

## Decisions

### D1: Resolve StorageClass via a new prefix-aware query, join on `(cluster, namespace, claim)`

Add `QPVCInfo Query = "kube_persistentvolumeclaim_info"` to `pkg/promql/queries.go`
and a `Render` case **with the leading `%s` prefix placeholder**
(`last_over_time(%skube_persistentvolumeclaim_info[%s])`) so it participates in the
`KSG_METRIC_PREFIX` allowlist like the other KSM series. The bare constant stays the
unprefixed metric name so `query=` / `query_name=` self-metric and span dimensions
remain stable across prefixed deployments.

Wire it into the `ReadTopology` errgroup fan-out (now 11 parallel queries: 6 KSM
topology + 3 D29 service/endpointslice + 2 D34 owner) and add a `resolvePVCStorageClass`
helper in `pkg/build/topology.go` mirroring `resolvePodOwners`: parse the result into
`map[pvcKey]string` keyed by `(cluster, namespace, persistentvolumeclaim)`. When
constructing each PVC node from the binding metric, look up
`(cluster, namespace, claim_name)` (the binding metric's `claim_name` equals the info
metric's `persistentvolumeclaim`) and set the resolved StorageClass.

*Alternative considered — join in PromQL via `* on(...) group_left`.* Rejected: ksg's
convention is to fetch flat series and join in Go (the readers already do this for
owners and endpointslices); a PromQL join would couple the two metrics into one query
and complicate the per-query timeout/error accounting and the prefix rendering.

### D2: Determinism on duplicate StorageClass — lexically-smallest wins

If the upstream reports more than one `storageclass` for a single
`(cluster, namespace, claim)` (shouldn't happen for a real PVC, but the series set is
untrusted), pick the lexically smallest. Same determinism rule and rationale as the
D34 owner pick. Pure function of the inputs → golden-stable.

### D3: `StorageClass()` on the sealed `GraphNode` interface (not a type-switch)

The serialiser needs each PVC's StorageClass to compute its parent. Per the
no-type-switch contract, add `StorageClass() string` to the sealed `graph.GraphNode`
interface, implemented by all five concrete types: `PVCNode` returns its resolved
value; **`PodNode`, `K8sNode`, `ServiceNode`, `ExternalNode` all return `""`**. `""`
means "no StorageClass". `PVCNode` gains a `StorageClassValue string` field set at
construction.

This mirrors `IPAddress()` / `Owner()` exactly. It is "public" in the Go sense and
available to embedders (e.g. `graph-api-gateway`), but it is **not** serialised —
the cytoscape DTO simply does not emit a `data.storageclass` field. Adding a method
to a sealed interface (unexported `isGraphNode()`) is safe: external packages consume
the interface but cannot implement it, so no embedder breaks.

*Alternative considered — concrete-type assertion in the serialiser
(`if pvc, ok := n.(graph.PVCNode); ok`).* Rejected: violates the explicit
"never through type switches in the serialiser" rule and the precedent set by
`Owner()`.

*Alternative considered — return `*string` for symmetry with `Owner() *Owner`.*
Rejected: StorageClass is a single scalar; `string` with `""`-means-absent is simpler
and the serialiser's non-empty check is the same either way.

### D4: StorageClass group nodes are serialiser-synthesised, derived from emitted PVCs — no projection change

The `type="storageclass"` group nodes are synthesised in the serialiser from the set
of emitted PVC nodes that have a non-empty `StorageClass()`, exactly as the
`type="cluster"` groups are derived from emitted nodes' `labels.cluster`. Because they
are computed **after** projection from the surviving node set, a `?cluster=` /
`?namespace=` / `name` / traversal filter that drops PVCs simply drops the
corresponding groups — **no `pkg/graph/project.go` change is required**, and no
dangling `data.parent` can be produced (the PVC's parent group is synthesised in the
same pass from the same surviving set).

This supersedes the proposal's tentative "retain StorageClass groups in projection"
note: there is nothing to retain because groups are not `GraphNode`s.

Concretely in `pkg/cytoscape/cytoscape.go`:
- While scanning `view.Nodes` to build `present`/`clusterSeen`, also collect a sorted
  set of `(cluster, storageclass)` from PVC nodes whose `StorageClass() != ""`.
- Emit StorageClass group nodes **after** the cluster groups and **before** the real
  nodes, ordered by `(cluster, storageclass)`. Each carries
  `id="<cluster>/storageclass/<sc>"`, `name=<sc>`, `type="storageclass"`,
  `labels={}`, `parent="cluster/<cluster>"`, no `ipaddress`.
- Extend `compoundParent` for `type="pvc"`: if `node.StorageClass() != ""` return its
  group id `storageClassParentID(cluster, sc)`; else fall back to the existing
  `clusterParentID(cluster)`. Add a `storageClassParentID` helper alongside
  `clusterParentID`.

### D5: Group node carries `labels: {}` (no cluster label) — per the user decision

The StorageClass group node carries an empty `labels` object, matching the
`type="cluster"` group node. Its cluster identity is expressed solely by its `id`
(`<cluster>/storageclass/<name>`) and `parent` (`cluster/<cluster>`). No `cluster`
key is added.

### D6: Group node id format `<cluster>/storageclass/<name>`

Cluster-scoped, consistent with PVC ids (`<cluster>/<namespace>/<claim>`) and the
`cluster/<name>` group id. StorageClass names are DNS-1123 subdomains (no `/`), so the
id is unambiguous. The `storageclass` segment is a fixed literal distinguishing it
from namespace-scoped PVC ids.

## Risks / Trade-offs

- **[Golden + OpenAPI drift]** New group nodes and the new `type="storageclass"`
  string change every Cytoscape body that contains a PVC, and add a node-`type` enum
  value. → Refresh `internal/api/testdata/golden/*.json` with `-update`; **manually**
  update the handwritten swag `@`-annotations enumerating node types (the `check-docs`
  drift job does not catch stale hand-annotations — known gap) and regenerate
  `docs/` via `make docs`.

- **[Metric label-name assumption]** The join assumes `kube_persistentvolumeclaim_info`
  exposes `persistentvolumeclaim` and `storageclass` and that `claim_name` (binding
  metric) == `persistentvolumeclaim` (info metric). → These are kube-state-metrics
  defaults; documented in the modified `cluster-topology-source` "Topology series
  consumed" requirement and the README metric table. Absence degrades to empty
  StorageClass (graceful), so a label-name mismatch fails safe (no grouping) rather
  than failing the build.

- **[Extra upstream query cost]** One more parallel PromQL query per build. → Bounded
  by the existing per-call timeout and upstream VictoriaMetrics search limits; runs in
  the same errgroup, so wall-clock is unchanged (it's parallel). Adds a
  `query="kube_persistentvolumeclaim_info"` dimension to the existing upstream-query
  self-metrics — additive, no metric contract change.

- **[Presentation re-parenting is observable]** Existing consumers that pinned a PVC's
  `data.parent` to `cluster/<name>` will now see `<cluster>/storageclass/<name>` for
  PVCs with a StorageClass. → Acceptable: compound nesting is explicitly
  presentation-only (D31) and the body shape `{apiVersion, clusters, elements}` plus
  all existing node/edge **types** are unchanged. Treated as additive, not a v2 break.

- **[Embedder surface]** `graph-api-gateway` (and other `pkg/` embedders) inherit the
  new `GraphNode.StorageClass()` method. → Additive on a sealed interface; consumers
  are unaffected unless they choose to read it.

## Migration Plan

Single additive deployment, no data migration:
1. Ship the reader + serialiser + interface method behind no flag (always on; the
   metric is optional, so clusters without `kube_persistentvolumeclaim_info` simply
   render the existing `cluster > pvc` nesting).
2. Refresh golden files and OpenAPI docs in the same change.
3. Rollback = revert the change; no persisted state, no schema versioning, no upstream
   dependency beyond an optional KSM default series.

To populate StorageClass, operators only need kube-state-metrics emitting its default
`kube_persistentvolumeclaim_info` (no `--metric-labels-allowlist` required, unlike the
D29 endpointslice join).

## Open Questions

None blocking. Both prior decision points are resolved: `StorageClass()` lives on the
sealed `GraphNode` interface (D3), group nodes carry `labels: {}` (D5), and group-node
ordering is fixed (D4). Deferred (out of scope, future change if wanted): a
`?storageclass=` filter, and surfacing StorageClass-level metrics (capacity, bound
status) on a future typed PVC attribute.
