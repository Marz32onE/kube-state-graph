## Why

Operators looking at the pod / node / PVC graph today cannot tell **which Argo CD
Application owns a workload**. Argo CD already records this in-cluster as a
tracking marker on every managed resource, so the fact is available through the
existing kube-state-metrics → VictoriaMetrics pipeline — no Kubernetes API
dependency, no new exporter. Surfacing it on each pod node lets a viewer pivot
straight from a pod in the topology to its GitOps source of truth (ownership,
blast radius, "who deploys this"). This mirrors the recently added pod
controller-owner attribute (D34): the same shape, resolved from the same KSM
owner chain.

## What Changes

- Add a **typed, nullable, pod-only** Argo CD application attribute to graph pod
  nodes, serialised as `data.argoapp` with `omitempty` (exact json key finalised
  in design). It follows the D34 `owner` / D31 IP-address precedent: a typed
  accessor on the sealed `graph.GraphNode` interface, emitted only in `data.*`,
  and **NEVER inside `labels`** (which stay strict `map[string]string`).
- **Resolve the owning Argo CD Application by joining KSM controller/pod series**,
  with a deterministic precedence (full ordering specified in design.md):
  - **Primary — controller annotation:** reuse the D34 owner chain
    (`kube_pod_owner` + `kube_replicaset_owner`, ReplicaSet skipped to its
    Deployment) to find the pod's top-level controller, then read that
    controller's `argocd.argoproj.io/tracking-id` annotation via
    `kube_<kind>_annotations` and parse the app name (first `:`-delimited
    segment of `<app>:<group>/<kind>:<namespace>/<name>`). This is the Argo CD
    3.0+ default tracking method and the only one Argo stamps reliably (it skips
    resources with `ownerReferences`, so Pods/ReplicaSets are never directly
    tracked).
  - **Fallback — pod label:** the pod's own instance label via
    `kube_pod_labels` (default key `app.kubernetes.io/instance`, treated as a
    **configurable** key since Argo's `instanceLabelKey` is operator-tunable).
    Covers label-tracking deployments where the label propagated into the pod
    template, and any case the controller annotation is absent.
  - Pick is a pure, order-independent function with a lexically-smallest
    tiebreak on collision (D6 determinism). The source metrics are **OPTIONAL** —
    absent / empty series degrade gracefully to no attribute, no build failure
    (same as owner / StorageClass).
- Add the new OPTIONAL upstream metric(s) to the consumed-series contract, extend
  the `KSG_METRIC_PREFIX` KSM-shaped allowlist to cover them, and add the
  corresponding PromQL query leg(s) to `ReadTopology` (bare `Query` constant +
  prefix-aware `Renderer.Render` case, so `query_name` self-metric / span
  dimensions stay stable — D26).
- Documentation: README "Upstream metrics consumed" table (noting the required
  **non-default** `--metric-annotations-allowlist` / `--metric-labels-allowlist`,
  same dependency model as the D29 endpointslice allowlist), the handwritten swag
  `@`-annotations in `internal/api/handlers.go`, and a `make docs` regeneration of
  `docs/swagger.{json,yaml}`.
- **No new node type, no new edge type.** Additive wire change (a new optional
  `data.*` field) — **not BREAKING**. Argo Rollouts CRD pods are read via their
  native Deployment/StatefulSet/Pod series; the Rollout CRD itself has no native
  KSM annotation metric (would need CustomResourceState) and is out of scope for
  v1.

## Capabilities

### New Capabilities

<!-- none — this is additive to existing capabilities, following the D34 owner / D31 StorageClass precedent which both landed in cluster-topology-source -->

### Modified Capabilities

- `cluster-topology-source`: add the OPTIONAL Argo CD source metric(s)
  (`kube_<kind>_annotations` / `kube_<kind>_labels` and/or `kube_pod_labels`) to
  the "Topology series consumed" contract and the "Configurable upstream
  metric-name prefix" series list; add a new **"Pod Argo CD application
  attribute"** requirement modelled on the existing "Pod controller-owner
  attribute with ReplicaSet skip" (D34) requirement — typed nullable attribute,
  serialised `data.*` with `omitempty`, never in `labels`, OPTIONAL with graceful
  degradation, deterministic pick on collision, no new node/edge type.
- `graph-api`: add a serialisation scenario for the new typed `data.argoapp`
  attribute (modelled on the "Node ipaddress attribute" requirement) and reaffirm
  that the value lives on a typed attribute, never in the strict `labels` map.

## Impact

- **Code:** `pkg/graph/node.go` (sealed interface accessor + PodNode field +
  nil accessor on the other four node types), `pkg/build/topology.go` (resolver +
  `topologyVectors` field + `ReadTopology` errgroup leg + `RawSeriesCount` entry +
  set on `canonicalPod`), `pkg/promql/queries.go` (Query const + prefix-aware
  Render case), `pkg/cytoscape/cytoscape.go` (`NodeData` field with `omitempty` +
  assignment in the Serialise loop).
- **Tests:** `pkg/graph/node_test.go`, `pkg/cytoscape/owner_test.go` (serialiser),
  `pkg/build/topology_test.go` (resolution + determinism + absence + not-in-labels),
  `pkg/promql/queries_test.go` (prefix), `internal/api/golden_test.go` + a golden
  fixture, `internal/integration/graph_e2e_test.go` (Docker-gated end-to-end).
- **Docs:** `README.md` metrics table, `internal/api/handlers.go` `@`-annotations
  (handwritten — not caught by `make check-docs`), `docs/swagger.{json,yaml}` via
  `make docs`.
- **Operational dependency (upstream):** KSM must be started with the matching
  allowlist flag, e.g.
  `--metric-annotations-allowlist=deployments=[argocd.argoproj.io/tracking-id]`
  (and/or `--metric-labels-allowlist=...=[app.kubernetes.io/instance]`).
  Annotations/labels are not KSM defaults; this is the same opt-in model already
  documented for the D29 endpointslice service-name label.
- **No new dependency, no `k8s.io/client-go` import, no new `build.Reason`** — the
  metric is OPTIONAL and absence is non-fatal.
- **CLAUDE.md** load-bearing design-rule sections extended: a D34-sibling bullet
  for the new attribute, the `labels`-stay-strict carve-out, the
  `KSG_METRIC_PREFIX` allowlist list, the "Sealed graph types" accessor set, and
  the D32 `pkg/` engine export list.
