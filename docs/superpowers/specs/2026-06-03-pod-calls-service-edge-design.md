# Design: `pod-calls-service` edge type + remove `others` node

**Date:** 2026-06-03
**Status:** Approved (brainstorming)
**Scope:** `kube-state-graph` (downstream `graph-api-gateway` reference check)

## Problem

When a pod calls a Kubernetes Service via a `"://"` connection string (e.g.
`http://payments.shop.svc.cluster.local`), the service-graph resolver already
resolves the **server side** to a `service` node (`resolveConnString` →
`materializeService`, `pkg/build/servicegraph.go`) and fans out
`service-selects-pod` edges to the backing pods. But the call edge itself is
**always** typed `pod-calls-pod` (`servicegraph.go:138`), regardless of whether
the target resolved to a pod or a service. So a "pod calls service" edge already
exists in the graph — it is simply **mislabeled** as `pod-calls-pod` while
pointing at a `service` node.

Separately, the unresolvable-`"://"` fallback currently produces an `others`
node. In practice this path is (near-)dead: every `"://"` either resolves to a
known in-cluster service or is a genuinely external endpoint. The `others` node
type carries no operational value distinct from `external`.

## Decisions

1. **`pod-calls-service` is determined purely by the target node type.** When the
   call edge's target resolves to a `service` node, the edge type is
   `pod-calls-service`; otherwise it stays `pod-calls-pod`. The source side is not
   inspected (it is almost always a pod; rarely a service/external when the client
   side itself is a connection string — `pod-calls-pod` is already an approximate
   name for those cases).

2. **Unresolvable `"://"` connection strings fall back to `external`, not
   `others`.** External URLs and unknown in-cluster service names both become
   `external/<verbatim-label>`. This collapses the previously-disjoint
   `others`/`external` ID spaces (D27/D18) — both fallbacks now produce `external`
   nodes. Distinct labels still yield distinct `external/<label>` IDs, so they
   coexist in one dedupe map.

3. **The `OthersNode` type is removed entirely.** Since nothing produces `others`
   anymore, the sealed type, its `NodeType` constant, ID helper, result field, and
   all golden/doc references are deleted rather than left as latent dead code.

## Architecture / changes

### A. `pod-calls-service` edge (target-driven) — `pkg/graph`, `pkg/build`
- `pkg/graph/edge.go`: add `EdgeTypePodCallsService EdgeType = "pod-calls-service"`.
- `pkg/build/servicegraph.go` edge-construction loop (currently ~L129–139):
  decide edge type by membership in the per-build `res.services` map —
  `res.services[k.tgt]` exists ⟺ the target resolved to a service node.
  ```go
  edgeType := graph.EdgeTypePodCallsPod
  if _, isSvc := res.services[k.tgt]; isSvc {
      edgeType = graph.EdgeTypePodCallsService
  }
  edges = append(edges, graph.NewEdge(edgeType, k.src, k.tgt, labels))
  ```
  The `labels` rule is unchanged: `cluster` is present iff the source side is a
  pod (D9). Because the service is always resolved in the trace-source (client)
  cluster, a `pod-calls-service` edge is **always intra-cluster**.

### B. Unresolvable `"://"` → `external` — `pkg/build/servicegraph.go`
- `resolveConnString` fallback (currently `return r.othersNode(label)`, ~L236)
  becomes `return r.external(label)`. The verbatim connection-string label is
  used as both the `external/<label>` ID basis and the node name (same as the
  prior `others` behaviour).

### C. Remove `OthersNode` — `pkg/graph`, `pkg/build`
- `pkg/graph/node.go`: delete the `OthersNode` type, `NodeTypeOthers` constant,
  and `OthersID()`.
- `pkg/build/servicegraph.go`: delete the `others` dedupe map field, the
  `othersNode()` method, and the `ServiceGraphResult.OthersNodes` field plus its
  assembly loop. Assembly that consumes `OthersNodes` (build.go / graph.go) is
  updated to drop it.
- Serialisation is unaffected — `pkg/cytoscape` serialises via `GraphNode`
  methods with no type switch, so dropping a concrete type needs no serialiser
  change.

### D. Registry — `pkg/graph/registry.go`
- `pod-calls-pod`: `SourceType` → `{pod, service, external}`,
  `TargetType` → `{pod, external}` (drop `others` and `service`); description
  updated to state service targets use `pod-calls-service`.
- New `pod-calls-service` entry: `SourceType {pod, service, external}`,
  `TargetType {service}`, `Directed: true`, `MayCrossCluster: false`,
  `Labels: [{cluster, string}]`.

### E. Contract docs
- `CLAUDE.md` + `openspec/changes/add-k8s-pod-graph-api/design.md` (+ `.zh-tw.md`):
  update D27/D29 — unresolvable `"://"` → `external`; target=service →
  `pod-calls-service`; remove all `others` prose, the "others/external disjoint"
  contract, and the "edge `type` stays `pod-calls-pod`" statement.
- `README.md` / `README.zh-tw.md`: add `pod-calls-service` to the edge-type list;
  drop `others` from the node-type list.

### F. Tests & generated artifacts
- `pkg/build/servicegraph_test.go`: flip every connstring→`others` assertion to
  `external`; rewrite (or remove) the "others/external disjoint" case — both
  fallbacks now yield distinct `external` nodes.
- `internal/api/golden_test.go`: delete `buildWithOthers` and the
  `testdata/golden/with-others-cytoscape.json` golden.
- Regenerate the remaining service-target goldens with `-update`
  (`with-service-cytoscape.json` → `pod-calls-service` + changed UUIDv5 edge IDs,
  since the edge ID derives from `<type>|<source>|<target>`) and
  `edge-types.json`.
- `make docs`: regenerate swagger — `NodeType` enum drops `others`, `EdgeType`
  enum adds `pod-calls-service`.
- `internal/integration/graph_e2e_test.go`: sync any `others` / `pod-calls`
  assertions.

## Risks / cross-repo impact

- **Downstream `graph-api-gateway`** embeds `pkg/cytoscape` / `pkg/graph`
  in-process. Removing `NodeTypeOthers` and the `"others"` `NodeType` enum value
  is a breaking change for any gateway code that references the constant or
  branches on the `"others"` string. During implementation, check the gateway
  (still wired via a local-path `replace` directive) and update it if needed.
- Strictly, removing a `NodeType` enum value is a non-additive schema change
  (CLAUDE.md: non-additive = v2). Accepted here because `others` was effectively
  never produced; impact is minimal.
- All edges whose target is a service get **new UUIDv5 IDs** (the type changes
  from `pod-calls-pod` to `pod-calls-service`). Golden snapshots must be refreshed.

## Testing strategy

- Unit (`pkg/build/servicegraph_test.go`): target=service ⟹ `pod-calls-service`;
  target=pod/external ⟹ `pod-calls-pod`; unresolvable `"://"` ⟹ `external`;
  no `others` produced anywhere.
- Registry (`pkg/graph`): `/v1/edge-types` lists `pod-calls-service`; type/target
  metadata correct.
- Golden (`internal/api`): wire-format snapshots refreshed and reviewed for
  the service-target relabel.
- `make test` (race + integration), `make lint`, `make vet`, `make vuln`,
  `make check-docs`, `make verify-mocks` all green before commit/push.
