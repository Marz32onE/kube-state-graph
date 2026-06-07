## 1. PromQL query layer (`pkg/promql`)

- [x] 1.1 Add `QPVCInfo Query = "kube_persistentvolumeclaim_info"` constant in `queries.go` (bare metric name; grouped with the KSM-shaped queries)
- [x] 1.2 Add a `Render` case for `QPVCInfo` with the leading `%s` prefix placeholder: `last_over_time(%skube_persistentvolumeclaim_info[%s])` (so it is prefix-aware via `Renderer.Prefix`)
- [x] 1.3 Unit test: `Render(QPVCInfo, w)` yields the bare form with zero prefix and `o11y_kube_persistentvolumeclaim_info` with `Prefix="o11y_"` (covers the modified `cluster-topology-source` prefix scenario)

## 2. Topology StorageClass resolution (`pkg/build`)

- [x] 2.1 Add `QPVCInfo` to the `ReadTopology` errgroup fan-out (now 11 parallel queries: 6 KSM topology + 3 service/endpointslice + 2 owner)
- [x] 2.2 Implement `resolvePVCStorageClass` in `topology.go` (mirror `resolvePodOwners`): parse the result into `map` keyed by `(cluster, namespace, persistentvolumeclaim)` → storageclass; on duplicate `(cluster, namespace, claim)` keep the lexically-smallest storageclass (D2 determinism)
- [x] 2.3 Join into PVC node construction: when building a PVC from `kube_pod_spec_volumes_persistentvolumeclaims_info`, look up `(cluster, namespace, claim_name)` against the map (binding `claim_name` == info `persistentvolumeclaim`) and set the resolved StorageClass; **enrich existing PVC nodes only — never materialise a PVC from the info metric** (Non-Goal)
- [x] 2.4 Unit test: StorageClass resolved for a matching PVC (no `storageclass` key in `labels`, no `data.storageclass`); PVC with no matching info series → empty StorageClass, build succeeds; info metric entirely absent → all PVCs empty, build succeeds; duplicate series → lexically-smallest wins (covers the three `PVC StorageClass resolution` scenarios + the `PVC info metric absent` scenario)

## 3. Graph node types (`pkg/graph`)

- [x] 3.1 Add `StorageClassValue string` field to `PVCNode` and thread it through the PVC node constructor
- [x] 3.2 Add `StorageClass() string` to the sealed `GraphNode` interface
- [x] 3.3 Implement `StorageClass()` on all five concrete types: `PVCNode` returns its value; `PodNode`, `K8sNode`, `ServiceNode`, `ExternalNode` return `""` (D3)
- [x] 3.4 Unit test: `StorageClass()` returns the resolved value for a `PVCNode` and `""` for every other node kind and for a class-less PVC

## 4. Cytoscape serialiser (`pkg/cytoscape`)

- [x] 4.1 While scanning `view.Nodes`, collect a sorted set of `(cluster, storageclass)` from `type="pvc"` nodes whose `StorageClass() != ""`
- [x] 4.2 Add a `storageClassParentID(cluster, sc)` helper (alongside `clusterParentID`) producing `"<cluster>/storageclass/<sc>"`
- [x] 4.3 Synthesise `type="storageclass"` group nodes — `id="<cluster>/storageclass/<sc>"`, `name=<sc>`, `labels={}`, `parent="cluster/<cluster>"`, no `ipaddress` — emitted **after** cluster groups and **before** real nodes, ordered by `(cluster, storageclass)` (D4/D5/D6, byte-determinism)
- [x] 4.4 Extend `compoundParent` for `type="pvc"`: return `storageClassParentID(cluster, sc)` when `StorageClass() != ""`; else fall back to `clusterParentID(cluster)`. Confirm no `data.storageclass` field is ever emitted on the PVC entry
- [x] 4.5 Unit tests: `cluster > storageclass > pvc` nesting; `cluster > pvc` fallback when class empty; group node has `labels={}` and `parent="cluster/<cluster>"`; group ordering deterministic; PVC entry has neither `data.storageclass` nor a `labels.storageclass` (covers the new `graph-api` compound + response-shape scenarios)

## 5. Golden & property tests (`internal/api`, `pkg/graph`)

- [x] 5.1 Extend a golden fixture so it contains a PVC with a resolved StorageClass and a PVC without one; regenerate with `go test ./internal/api/ -update -run Golden` and review the diff (new `storageclass` group node + re-parented PVC)
- [x] 5.2 Property test invariant (`pkg/graph/property_test.go` or serialiser test): every emitted node's `data.parent`, when present, references an `id` present in the same response — including the new storageclass groups (no dangling parent)

## 6. Integration tests (`internal/integration`)

- [x] 6.1 Add a `kube_persistentvolumeclaim_info` fixture to the VictoriaMetrics testcontainer ingest; assert the `/v1/graph` body contains the `<cluster>/storageclass/<sc>` group node and the PVC nests under it end-to-end
- [x] 6.2 Integration case: a cluster whose upstream omits `kube_persistentvolumeclaim_info` renders PVCs under `cluster > pvc` (graceful fallback)

## 7. Docs & contracts

- [x] 7.1 README: add the `kube_persistentvolumeclaim_info` row (labels `cluster, namespace, persistentvolumeclaim, storageclass`; Optional) to the upstream-metric table
- [x] 7.2 CLAUDE.md: add `kube_persistentvolumeclaim_info` to the `KSG_METRIC_PREFIX` allowlist sentence and extend the D31 compound-node note with the `cluster > storageclass > pvc` nesting + `cluster > pvc` fallback
- [x] 7.3 Update the handwritten swag `@`-annotations that enumerate node `type` values to include `storageclass`, then run `make docs` and commit `docs/swagger.{json,yaml}` (the `check-docs` drift job does NOT catch stale hand-annotations — update them manually)
- [x] 7.4 Run `make check-docs` and confirm no drift (regeneration is idempotent; the only diff is the intended node-types annotation change, to be committed)

## 8. Verification gate

- [x] 8.1 `make test` (race + shuffle), `make vet`, `make lint`, `make vuln` all green
- [x] 8.2 `make verify-mocks` clean (no mock regen expected — `GraphNode` is not in `.mockery.yaml`; confirmed "mocks up-to-date")
- [x] 8.3 `openspec validate add-storageclass-compound-node` passes (this CLI exposes `validate`, not `verify`); ready for `/opsx:archive`
