## Why

PVC nodes today carry no StorageClass information, and the Cytoscape graph offers
no way to see or group persistent volumes by their storage tier. StorageClass is
a primary operational dimension (provisioner, performance tier, reclaim policy);
surfacing it on the PVC node and grouping PVCs by class lets operators reason
about storage at a glance. The enabling metric (`kube_persistentvolumeclaim_info`)
is a kube-state-metrics default that ksg does not yet read — the original design
intended a `storage_class` label but it was never populated and was removed as a
dead label (F24).

## What Changes

- Read a new upstream series, `kube_persistentvolumeclaim_info`, and resolve each
  PVC's StorageClass by joining on `(cluster, namespace, persistentvolumeclaim)`.
  StorageClass is OPTIONAL — absence degrades gracefully (no build failure), same
  precedent as `kube_pod_owner` (D34).
- StorageClass is used **only** to drive the PVC's compound parent — it is **not**
  exposed as a standalone PVC node attribute. There is **no** `data.storageclass`
  on the `type="pvc"` entry (and nothing added to `labels`); the class name
  surfaces in the wire output solely through the synthetic group node id and each
  PVC's `data.parent`. The resolved value is carried internally on `PVCNode` for
  the serialiser to compute nesting, not serialised as its own field.
- Introduce a new **presentation-only compound node** `type="storageclass"`: one
  synthetic group node per distinct `(cluster, storageClass)` observed on an
  emitted PVC, with a cluster-scoped id (e.g. `<cluster>/storageclass/<name>`).
  It is a serialiser-synthesised DTO (like the `cluster` group, D31), not a
  `GraphNode`, and carries no edges.
- Change Cytoscape nesting for PVCs to **`cluster > storageclass > pvc`**: the
  StorageClass group is parented to its cluster group, and each PVC that has a
  resolved StorageClass is parented to its StorageClass group. PVCs with **no**
  resolved StorageClass fall back to **`cluster > pvc`** (parented directly to the
  cluster group — no synthetic "(none)" group is created). This re-parents PVC
  `data.parent` values in the serialised body (golden snapshots update); the body
  shape `{apiVersion, clusters, elements}` and all existing node/edge types are
  unchanged. Additive at the type level (new node type, new typed attribute); not
  a v2 schema break.
- Add `kube_persistentvolumeclaim_info` to the `KSG_METRIC_PREFIX` allowlist so
  the new series is prefixed consistently with the other KSM-shaped queries.
- The StorageClass group must survive projection so the nesting holds: a
  StorageClass group is retained iff some PVC that survived the active filters is
  backed by it (analogous to the K8s-node "host-of-in-scope-pod" retention rule,
  D31). This stays presentation-only (synthesised at serialise time from the
  emitted PVC set), so no dangling `data.parent` can be produced.

## Capabilities

### New Capabilities
<!-- None — this extends existing capabilities rather than introducing a new one. -->

### Modified Capabilities
- `cluster-topology-source`: add a requirement to read `kube_persistentvolumeclaim_info`
  and resolve each PVC's StorageClass (optional, graceful absence); extend the
  `KSG_METRIC_PREFIX` allowlisted metric set to include it.
- `graph-api`: a new presentation-only `type="storageclass"` compound node is
  introduced and Cytoscape nesting for PVCs becomes `cluster > storageclass > pvc`
  (fallback `cluster > pvc` when StorageClass is unknown). The `type="pvc"` node
  gains **no** serialised attribute — StorageClass is reflected only via the group
  node and the PVC's `data.parent`.

## Impact

- **PromQL / build**: `pkg/promql/queries.go` (new `Q…` constant + render),
  `pkg/build/topology.go` (parse + `(cluster, namespace, claim)` join, owner-style
  optional resolution), added to the `ReadTopology` parallel fan-out.
- **Graph types**: `pkg/graph/node.go` — `PVCNode` carries the resolved
  StorageClass internally (consumed by the serialiser to compute the parent); it
  is **not** added to the serialised node body. Whether to expose a public
  `StorageClass()` accessor for embedders is a design.md decision.
- **Serialiser**: `pkg/cytoscape/cytoscape.go` — synthesise `type="storageclass"`
  group nodes and re-parent PVCs (`cluster > storageclass > pvc`, fallback
  `cluster > pvc`). No new field is emitted on the PVC entry.
- **Projection**: `pkg/graph/project.go` — ensure StorageClass groups are
  retained for in-scope PVCs (presentation-level retention).
- **Contracts / docs**: OpenAPI annotations + `make docs` regen, golden
  `testdata/golden/*.json` updates, README upstream-metric table, the
  `KSG_METRIC_PREFIX` allowlist note in `CLAUDE.md` / docs.
- **Tests**: unit (StorageClass join, serialiser nesting + no-SC fallback),
  golden refresh, and an integration fixture ingesting
  `kube_persistentvolumeclaim_info` via the VictoriaMetrics testcontainer.
- **Dependencies**: none new.
