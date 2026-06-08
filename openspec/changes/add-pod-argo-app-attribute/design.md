## Context

`kube-state-graph` already resolves a pod's controller owner (D34): `parseTopology`
calls `resolvePodOwners(v.PodOwner, v.ReplicaSetOwner)` to build a
`(cluster, namespace, pod) → ownerRef{kind,name}` index from `kube_pod_owner`,
skipping the intermediate ReplicaSet to its owning Deployment via
`kube_replicaset_owner`. The result is set on `PodNode.OwnerValue` and surfaced
through the sealed `graph.GraphNode.Owner() *Owner` accessor, serialised as the
`omitempty` `data.owner` object — never inside `labels`.

This change adds a second, independent enrichment to the **same pod entity**:
the **Argo CD Application** that owns the workload. The fact already exists
in-cluster as Argo CD's resource-tracking marker and reaches us through the
existing kube-state-metrics → VictoriaMetrics pipeline, so no Kubernetes API
client is introduced (the D1/D16 prohibition holds).

Research findings that constrain the design (all fact-checked against Argo CD
and kube-state-metrics primary sources — see proposal.md):

- **Argo CD 3.0+ default tracking method is `annotation`**: it stamps
  `argocd.argoproj.io/tracking-id` (a fixed, non-configurable key) on the
  top-level managed resource's outer metadata, value
  `<app>:<group>/<kind>:<namespace>/<name>` (e.g. `my-app:apps/Deployment:prod/checkout`).
  The 5-field shape is always present; for cluster-scoped resources the empty
  namespace segment is back-filled with the Application's destination namespace.
- **Argo CD never stamps resources that have `ownerReferences`** — ReplicaSets
  and Pods are skipped. So the marker lives on the **controller** (Deployment /
  StatefulSet / DaemonSet / Rollout), essentially never on the pod's own
  metadata. The label method's `app.kubernetes.io/instance` (default key,
  operator-configurable via `application.instanceLabelKey`, commonly
  `argocd.argoproj.io/instance`) reaches a pod only incidentally — when a chart
  templated it into `spec.template.metadata.labels` and Kubernetes copied it down.
- **kube-state-metrics exposes annotations/labels only on opt-in**:
  `kube_<kind>_annotations` (EXPERIMENTAL) and `kube_<kind>_labels` (STABLE)
  exist for Deployment, StatefulSet, DaemonSet, ReplicaSet, Pod, gated by
  `--metric-annotations-allowlist` / `--metric-labels-allowlist`. With nothing
  allowlisted the series are not emitted at all. KSM sanitises the key with
  `[^a-zA-Z0-9_] → _` and prefixes `annotation_` / `label_`, so
  `argocd.argoproj.io/tracking-id → annotation_argocd_argoproj_io_tracking_id`
  and `app.kubernetes.io/instance → label_app_kubernetes_io_instance` (both
  deterministic, no `_conflictN` for these unique keys).
- **Argo Rollouts (`rollouts.argoproj.io`) is a CRD with no native KSM metric**;
  reading its annotations needs KSM CustomResourceState config (operator-defined
  metric name). It is out of scope for v1 — but a Rollout-managed pod is still
  resolvable because Argo also tracks it transitively only on the Rollout; the
  pod's owner chain stops at the ReplicaSet/Rollout and falls through to the pod
  label or unresolved.

## Goals / Non-Goals

**Goals:**
- Surface, per pod, the owning Argo CD Application **name** as a typed, nullable
  attribute (`data.argoapp`), exactly mirroring the D34 `owner` precedent
  (typed accessor, `omitempty`, never in `labels`, OPTIONAL/graceful-degrading,
  deterministic on collision, no new node/edge type).
- Resolve it from the **controller's tracking-id annotation** as the primary
  source (reusing the D34 owner chain to find the controller), with the **pod's
  instance label** as a fallback.
- Keep the resolver a pure, order-independent function of its input vectors
  (D6 determinism); keep the new metrics OPTIONAL so absence never fails a build.

**Non-Goals:**
- No Argo Rollouts CRD annotation reading (needs CustomResourceState) — Rollout
  pods rely on the label fallback or stay unresolved in v1.
- No Argo CD API/CRD access; no `k8s.io/client-go`. Everything via VictoriaMetrics.
- No new node type, no new edge type, no compound-node grouping by Argo app
  (this is a serialised `data.*` attribute like `owner`, **not** a grouping key
  like StorageClass/D31). Grouping could be a separate future change.
- No Argo `installation-id`, app project, or app namespace (app-in-any-namespace)
  fields in v1 — name only (the struct is extensible later).
- No filters/traversal semantics change — the attribute is descriptive metadata
  on the pod node, not a new edge or scope dimension.

## Decisions

### D35.1 — Two-source resolution with fixed precedence

A pure resolver `resolveArgoApps(...)` (in `pkg/build/topology.go`, alongside
`resolvePodOwners` / `resolvePVCStorageClass`) produces a
`(cluster, namespace, pod) → string` app-name index, combining two sources in a
**fixed precedence**:

1. **Controller annotation (primary).** Reuse the already-computed `podOwners`
   index to get each pod's owning controller `(kind, name)`. Join that against a
   `(cluster, namespace, kind, name) → trackingId` index built from the
   controller annotation metrics, and parse the app name as the substring before
   the first `:` of the tracking-id value.
2. **Pod instance label (fallback).** When no controller-annotation match exists,
   read the pod's own instance label from `kube_pod_labels` and take its value
   verbatim as the app name.

Controller annotation wins because it is the Argo 3.0+ default, is
controller-authoritative, and is the only marker Argo reliably stamps; the pod
label is incidental and chart-dependent. Within a single source, the
lexically-smallest value wins on collision (D6), matching `resolvePodOwners`.

*Alternative considered — pod-only resolution* (read `kube_pod_labels` /
`kube_pod_annotations` directly): rejected as primary because Argo essentially
never puts the marker on the pod (the default annotation method writes only the
controller's outer metadata, and the label propagates to pods only by chance).
It survives as the fallback.

### D35.2 — Controller-kind scope: Deployment + StatefulSet + DaemonSet

KSM annotation metrics are per-resource-kind (distinct metric names *and*
identifying-label names), so the controller-annotation source needs one query
leg per supported kind. v1 supports the three native KSM workload kinds Argo
commonly manages and that the owner index already surfaces:
`kube_deployment_annotations`, `kube_statefulset_annotations`,
`kube_daemonset_annotations`. The owner index already yields the right
`(kind, name)` to join (Deployment after the RS skip; StatefulSet/DaemonSet
directly from `kube_pod_owner`).

*Trade-off:* this adds 3 controller-annotation legs + 1 pod-labels leg to
`ReadTopology` (11 → 15 parallel queries). They are cheap parallel `last_over_time`
reads against low-cardinality series (one per controller / per pod). Argo
Rollouts and other CRDs are excluded (no native metric — D35 Non-Goals).

*Alternative considered — Deployment-only* (2 new legs total): leaner, but
silently under-serves StatefulSet/DaemonSet workloads, which are common Argo
targets. **This is the main open scoping question — see Open Questions.**

### D35.3 — Typed nullable pod attribute, serialised `data.argoapp`

Add to `pkg/graph/node.go`, mirroring `Owner` exactly:

- a new accessor on the sealed `GraphNode` interface (working name
  `ArgoApp() *ArgoApp`);
- a small value struct `type ArgoApp struct { Name string \`json:"name"\` }`
  (a struct, not a bare string, to stay extensible — app namespace / project /
  resolution-source can be added later without a wire break);
- an `ArgoAppValue *ArgoApp` field on `PodNode` + `func (p *PodNode) ArgoApp() *ArgoApp { return p.ArgoAppValue }`;
- `ArgoApp() *ArgoApp { return nil }` on the other four node kinds (the compiler
  enforces completeness via the sealed interface).

Serialisation: `pkg/cytoscape/cytoscape.go` `NodeData` gains
`ArgoApp *graph.ArgoApp \`json:"argoapp,omitempty"\`` set from `n.ArgoApp()` in
the per-node loop. `omitempty` keeps every existing golden byte-identical
(ownerless/app-less pods and all non-pods emit nothing). The value MUST NOT enter
`labels` (strict `map[string]string` — D8) — there will be explicit tests.

*Naming:* `argoapp` is provisional; final json key / accessor name confirmed
with the user (see Open Questions). `data.application` reads more generic but
risks colliding with a future non-Argo notion; `data.argoapp` is unambiguous.

### D35.4 — Instance-label key: hardcoded precedence, no new knob

The pod-label fallback reads the instance label by a **fixed precedence list of
column names** — `label_app_kubernetes_io_instance` then
`label_argocd_argoproj_io_instance` (the default and the most common override) —
rather than introducing an operator-tunable key. This follows the D29/D30
"hardcoded contract, no knob" precedent and avoids threading a new config value
into the resolver. The tracking-id annotation column
(`annotation_argocd_argoproj_io_tracking_id`) is likewise fixed (the Argo
annotation key is not configurable).

*Alternative considered — a `--argo-instance-label-key` flag / env:* rejected
for v1 to keep the knob surface minimal; the two-key precedence covers the
overwhelming majority and is revisitable if a deployment needs a bespoke key.

### D35.5 — Query plumbing, prefix, and request-invariant selectors

New `promql.Query` constants (bare KSM metric names, so `query_name` self-metric
and span dimensions stay stable — D26): `QDeploymentAnnotations`,
`QStatefulSetAnnotations`, `QDaemonSetAnnotations`, `QPodLabels`. Each gets a
**prefix-aware** `Renderer.Render` case (`r.Prefix` prepended, like every
KSM-shaped series), and each is added to the documented `KSG_METRIC_PREFIX`
allowlist.

The three controller-annotation queries render with a **request-invariant
non-empty selector** on the fixed tracking-id column, e.g.
`last_over_time(%skube_deployment_annotations{annotation_argocd_argoproj_io_tracking_id!=""}[%s])`.
Like the D30 sentinel matcher, this is a fixed metric-selection contract (never a
caller filter), so it does not break the "no filters pushed to PromQL" rule —
it just bounds the series to Argo-managed controllers. `QPodLabels` renders bare
(one series per pod, same cardinality class as `kube_pod_info`, already fetched);
the instance-label column is selected at parse time per D35.4.

`ReadTopology` gains four `g.Go(fetch(...))` legs writing four new
`topologyVectors` fields, plus matching `RawSeriesCount` entries. Each goroutine
writes a distinct field (race-free; `g.Wait()` is the happens-before edge), as
today. `parseTopology` calls `resolveArgoApps(...)` up-front (next to
`resolvePodOwners` / `resolvePVCStorageClass`) and sets `ArgoAppValue` on each
`canonicalPod` via `podNameKey` lookup, `nil` when absent — exactly the
`var owner *graph.Owner; if o, ok := podOwners[...]; ok { ... }` pattern.

This is topology enrichment, so it lives in `ReadTopology` (not
`ReadServiceGraph`). Synth pods (`pkg/build/servicegraph.go`) have no
`kube_pod_info` row and correctly carry `nil` — left unset.

### D35.6 — Parsing the tracking-id value

App name = the substring before the first `:` of
`<app>:<group>/<kind>:<namespace>/<name>`. Argo app names are RFC1123 (no `:`),
so a split on the first `:` is unambiguous and robust for the always-5-field
value (including the cluster-scoped, namespace-back-filled form). A value that
does not contain `:` (malformed / non-Argo annotation that happens to be
allowlisted under the same key — not expected) is taken verbatim. An empty parse
yields no attribute (pod falls through to the label fallback, then unresolved).

## Risks / Trade-offs

- **Operators must opt in at KSM** (annotations/labels are not defaults). →
  Documented prominently in README + design as a hard prerequisite, mirroring the
  existing D29 endpointslice-allowlist note; absence degrades to no attribute, no
  error, so a misconfigured cluster simply shows no Argo apps.
- **Fan-out grows 11 → 15 parallel queries.** → Cheap `last_over_time` reads on
  low-cardinality controller series; controller-annotation legs are bounded by
  the `!=""` selector; `kube_pod_labels` is the same cardinality class as the
  already-fetched `kube_pod_info`. Acceptable; revisit only if profiling shows
  upstream pressure.
- **Argo Rollouts pods may resolve to no app** (no native KSM metric). → Out of
  scope (Non-Goal); the label fallback still catches Rollout pods whose chart
  set the instance label. Documented as a known limitation.
- **`kube_<kind>_annotations` is EXPERIMENTAL in KSM** (the `_labels` family is
  STABLE). → A contract-stability caveat noted in design/README; the labels-based
  fallback is on the STABLE family, and the attribute degrading to absent on a
  metric rename is non-fatal.
- **Wrong-app risk if owner resolution is ambiguous** (multiple controller
  annotations collide). → Deterministic lexically-smallest pick (D6), same as
  `resolvePodOwners`; covered by a determinism test feeding forward/reverse
  vector order.
- **Instance-label truncation** (Argo may truncate `app.kubernetes.io/instance`
  to 63 chars / mangle for app-in-any-namespace). → Only affects the fallback;
  value taken verbatim, accepted as a known fallback imprecision (the annotation
  primary is exact).

## Migration Plan

- **Additive, no rollback concerns.** New optional `data.argoapp` field; existing
  responses and goldens are byte-identical until a pod actually resolves an app
  (`omitempty`). No schema break, no new `build.Reason`, no API route change.
- **Deploy order:** ship the binary first (harmless — emits nothing until the
  metrics exist), then enable the KSM allowlist flags
  (`--metric-annotations-allowlist=deployments=[argocd.argoproj.io/tracking-id],statefulsets=[...],daemonsets=[...]`
  and optionally `--metric-labels-allowlist=pods=[app.kubernetes.io/instance]`).
  Disable by removing the flags — the attribute silently disappears.
- **Mocks:** no interface in `.mockery.yaml` changes (`promql.Querier` etc. are
  untouched), so `make mocks` is a no-op. Regenerate docs (`make docs`) and the
  golden fixture (`-update -run Golden`) and commit.

## Open Questions

1. **Controller-kind scope (D35.2):** ship **Deployment + StatefulSet + DaemonSet**
   (recommended — covers the common Argo workloads, +4 fan-out legs), or start
   **Deployment-only** (leaner, +2 legs) and add the others in a follow-up?
2. **Serialised key / accessor name (D35.3):** `data.argoapp` + `ArgoApp()`
   (recommended, unambiguous) vs `data.application` + `Application()` (generic,
   collision-risk)?
3. **Pod-label fallback (D35.1/D35.4):** keep the `kube_pod_labels` fallback in
   v1 (matches the user's stated second source, +1 leg, STABLE family), or defer
   it and ship controller-annotation-only first? Recommendation: keep it.
