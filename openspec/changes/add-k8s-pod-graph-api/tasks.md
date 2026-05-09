## 1. Project bootstrap

- [x] 1.1 Initialise Go module (`go mod init github.com/<org>/kube-state-graph`, Go 1.22+).
- [x] 1.2 Add direct dependencies: `github.com/gin-gonic/gin`, `github.com/prometheus/client_golang`, `github.com/google/uuid`, `golang.org/x/sync` (errgroup + semaphore).
- [x] 1.3 Lay out package skeleton: `cmd/kube-state-graph/`, `internal/api/`, `internal/build/`, `internal/promql/`, `internal/graph/`, `internal/config/`, `internal/observability/`.
- [x] 1.4 Add baseline `Makefile` targets: `build`, `test`, `vet`, `lint`, `cover`, `kind-up`, `kind-down`, `smoke`.
- [x] 1.5 Wire up `golangci-lint` config and a pre-commit/CI step that runs `go vet`, `golangci-lint`, and `go test ./...`.
- [x] 1.6 Add `.editorconfig`, `LICENSE`, top-level `README.md` placeholder.

## 2. Configuration and structured logging

- [x] 2.1 Define `internal/config.Config` struct covering: `--prom-url`, `--listen-addr`, `--max-window`, `--max-skew`, `--max-pods`, `--build-timeout`, `--build-concurrency`, `--cluster-discovery-lookback`, `--clusters-allowlist`, `--external-name-pattern`, `--enable-debug`, `--log-level`.
- [x] 2.2 Bind flags + env vars (`KSG_*`) using stdlib `flag` and `os.LookupEnv`; implement `Validate()` invariants (positive window, valid URL, etc.).
- [x] 2.3 Implement `internal/observability/log.New(level)` returning a `*slog.Logger` backed by a JSON handler; install as default.
- [x] 2.4 Add request-ID middleware that attaches an ID to context and logs one line per HTTP request with method/path/status/duration/request_id/cluster filters.

## 3. Graph types and registries

- [x] 3.1 Define sealed node interface `graph.GraphNode` with concrete `PodNode`, `K8sNode`, `PVCNode`, `ExternalNode` implementing `ID()`, `Name()`, `Type()`, `Labels()`.
- [x] 3.2 Define `graph.Edge` struct: `ID`, `Type`, `Source`, `Target`, `Labels` (map[string]string).
- [x] 3.3 Implement deterministic edge ID via `uuid.NewSHA1(namespace, []byte(type+"|"+source+"|"+target))` with a fixed compiled-in namespace UUID.
- [x] 3.4 Implement `graph.Graph` struct holding `Nodes`, `Edges`, plus pre-built forward + reverse adjacency maps (`map[NodeID][]*Edge`).
- [x] 3.5 Implement `graph.Scope` (filter spec: clusters, namespaces, nodes, edge types, traversal root/depth/direction).
- [x] 3.6 Implement pure `graph.Project(g *Graph, scope Scope) GraphView` returning slices of pointers (no allocations).
- [x] 3.7 Implement edge-type registry as a single `var` consumed by both the builder and the `/v1/edge-types` handler; cover `pod-runs-on-node`, `pod-mounts-pvc`, `pod-calls-pod`.

## 4. Upstream Prometheus query layer

- [x] 4.1 Wrap `prometheus/client_golang/api/v1` in `internal/promql.Client` with `Instant(ctx, query, ts) (model.Vector, error)` plus duration / failure metrics.
- [x] 4.2 Centralise PromQL templates in `internal/promql/queries.go` as named constants for `kube_pod_info`, `kube_node_info`, `kube_node_status_addresses`, `kube_pod_spec_volumes_persistentvolumeclaims_info`, `kube_node_labels`, `traces_service_graph_request_total`, plus the discovery and probe queries.
- [x] 4.3 Implement `--clusters-allowlist` injection: build a `{cluster=~"a|b|c"}` fragment and splice it into every query template (service-graph queries use the same single-`cluster` selector — server-side cluster is recovered at build time via the topology pod-UID index, not via PromQL label filtering).
- [x] 4.4 Implement `count(kube_pod_info)` cluster-size probe used to enforce `--max-pods` before a full build.
- [x] 4.5 Implement parallel fan-out via `errgroup.WithContext`; per-call context timeout from `--build-timeout`; abort whole build on any sub-query failure.

## 5. Cluster topology source (capability: cluster-topology-source)

- [x] 5.1 Implement `internal/build/topology.Read(ctx, q, window, end, allowlist) (Topology, error)`.
- [x] 5.2 Parse `kube_pod_info` series into `PodNode` entities: `id="<cluster>/<uid>"`, `name=<pod>`, `type="pod"`, `labels` includes `cluster`, `namespace`, `node` (cluster-scoped node ID).
- [x] 5.3 Parse `kube_node_info` + `kube_node_status_addresses` into `K8sNode` entities; surface `external_ip` under `labels`.
- [x] 5.4 Parse `kube_pod_spec_volumes_persistentvolumeclaims_info` into `PVCNode` entities; key `<cluster>/<namespace>/<claim_name>`.
- [x] 5.5 Parse `kube_node_labels` `label_*` entries; flatten into the K8s node `labels` under their original keys (e.g., `label_topology_kubernetes_io_zone` → `topology.kubernetes.io/zone`).
- [x] 5.6 Implement pod-restart handling: when multiple UIDs exist for the same `(cluster, namespace, pod)`, keep ONLY the latest UID as the canonical pod and discard prior UIDs (no synthetic linking edge — there is no reliable identity link once kubelet stops reporting the deleted UID).
- [x] 5.7 Implement `cluster="unknown"` bucketing for series missing the `cluster` label; surface in `kube_state_graph_clusters_observed`.
- [x] 5.8 Build `pod-runs-on-node` edges from `kube_pod_info{node=...}`.
- [x] 5.9 Build `pod-mounts-pvc` edges by joining `kube_pod_spec_volumes_persistentvolumeclaims_info` with the pod's host node within the same cluster.

## 6. Pod service-graph reader (capability: pod-service-graph)

- [x] 6.1 Implement `internal/build/servicegraph.Read(ctx, q, window, end, allowlist, externalPattern) ([]Edge, []ExternalNode, error)`.
- [x] 6.2 Compute `rate(traces_service_graph_request_total[<window>]) @ <end>`; drop series whose rate is exactly zero.
- [x] 6.3 For each surviving series, perform per-endpoint substitution: if `KSG_EXTERNAL_NAME_PATTERN` is non-empty AND the `client` (or `server`) label value contains the pattern → external node (`id="external/<value>"`, `name=<value>`, `type="external"`, `labels={"pattern":"<pattern>"}`); else pod-UID resolution via topology map.
- [x] 6.4 When pod-UID resolution finds no topology entry, emit a synthesised pod node (`name=<pod-uid>`, `labels.cluster=<cluster>`, no `ghost` flag).
- [x] 6.5 Set edge `labels.cluster` to the trace-source / client-side pod's cluster; omit the `cluster` key when the client side is an external endpoint. Server-side cluster is recovered via the topology global pod-UID index (not stamped on the metric) and is observable through the resolved target node's `labels.cluster`.
- [x] 6.6 Confirm numeric metrics (`rate`, `p99_ms`, `error_rate`) are NOT written into `labels` in v1; add a regression test that asserts no such keys appear.
- [x] 6.7 Tolerate empty / sparse upstream — `nil` or empty Vector results MUST yield zero edges, never an error.

## 7. Build pipeline + orchestrator

- [x] 7.1 Implement `internal/build.Build(ctx, q, window, end, allowlist, externalPattern) (*graph.Graph, error)`: runs topology + service-graph readers in parallel, joins, returns the global multi-cluster graph plus pre-computed adjacency.
- [x] 7.2 Validate caller-supplied `start` / `end` against `--max-window` and `--max-skew`; pass through to upstream PromQL verbatim. No server-side bucketing or alignment.
- [x] 7.3 Implement build-concurrency cap via `semaphore.Weighted`; on `TryAcquire` failure return a typed error mapped to `503 capacity`.
- [x] 7.4 Implement per-build timeout via `context.WithTimeout`; map timeout to `503 timeout`.
- [x] 7.5 Implement `--max-pods` enforcement: on probe overflow return typed error mapped to `503 cluster_too_large`.
- [x] 7.6 Implement outside-retention detection: zero topology rows + healthy `up{}` ⇒ `400 outside_retention`.
- [x] 7.7 (Removed — was: in-process Ristretto cache. v1 ships no result cache; future cache mechanism tracked separately.)
- [x] 7.8 (Removed — was: singleflight wrapper. v1 ships no request coalescing.)

## 8. HTTP API (capability: graph-api)

- [x] 8.1 Stand up Gin engine with `/v1/` route group, request-ID + slog middleware.
- [x] 8.2 Implement `GET /v1/graph` handler: parse + validate `start`, `end`, filter params, traversal params; align window + build + project + serialise + ETag.
- [x] 8.3 Implement Cytoscape.js serialiser: `{ apiVersion, clusters, elements: { nodes, edges } }` with canonical node/edge `data` shape. The body MUST NOT contain time-varying or echo-of-input fields — body shape is fixed so that identical inputs against the same upstream state produce a byte-identical body and `ETag`.
- [x] 8.4 Implement `GET /v1/graph/nodegraph` handler: project → Grafana Node Graph JSON (`nodes_fields`/`nodes`/`edges_fields`/`edges`); map `name`→`title`, cluster·namespace→`subTitle`, `type`→`mainStat`, edge `type`→edge `mainStat`, `secondaryStat` omitted.
- [x] 8.5 Implement `GET /v1/clusters` handler: live discovery query against VictoriaMetrics, intersected with `--clusters-allowlist`. No in-process discovery cache; ETag-based revalidation only.
- [x] 8.6 Implement `GET /v1/edge-types` handler: serialise the in-code registry; long `Cache-Control` and registry-hash `ETag`; honour `If-None-Match`.
- [x] 8.7 Implement `ETag` (sha256 of body) header on graph endpoints. No `Cache-Control` and no `X-Cache` on `/v1/graph`/`/v1/graph/nodegraph` — there is no server-side build cache to advertise.
- [x] 8.8 Implement `If-None-Match` 304 short-circuit on graph endpoints.
- [x] 8.9 Implement traversal pruning: BFS over the freshly built graph's adjacency map bounded by `depth`; reject `depth > 6` with `400 depth_too_large`.
- [x] 8.10 Implement filter validation: reject obviously malformed values; treat unknown values as empty result, not error.
- [x] 8.11 Implement `GET /livez` (always 200) and `GET /readyz` (1 s `up{}` probe → 200 / 503).
- [x] 8.12 Implement `GET /debug/last-queries` (behind `--enable-debug`). The previous `/admin/cache` flush route is removed — there is no result cache to flush.
- [x] 8.13 Implement uniform JSON error body `{ apiVersion, error: { reason, message } }` for 4xx/5xx and apply consistently.

## 9. Observability

- [x] 9.1 Register `kube_state_graph_*` Prometheus metrics: `build_duration_seconds`, `project_duration_seconds`, `serialise_duration_seconds{format}`, `build_concurrency`, `build_rejected_total{reason}`, `graph_node_count{cluster,kind}`, `graph_edge_count{type,cross_cluster}`, `clusters_observed`, `upstream_query_duration_seconds{query}`, `upstream_query_failures_total{query}`, `http_requests_total{path,status}`. Cache / singleflight metrics removed alongside their underlying mechanisms.
- [x] 9.2 Wire metrics emission into the build pipeline, orchestrator (semaphore + timeout), HTTP middleware, and upstream client.
- [x] 9.3 Expose `/metrics` (Prometheus exposition); confirm `cluster` and `cross_cluster` labels appear on observational gauges.
- [x] 9.4 Emit one structured `slog.Info("graph built", ...)` log line per build with the documented fields.

## 10. Unit tests

- [x] 10.1 Unit-test `graph.Project` against hand-crafted graphs covering: cluster filter, namespace filter, edge-type filter, traversal at depth 0/1/2/6, unknown root.
- [x] 10.2 Unit-test topology parser against canned `model.Vector` inputs: pod-restart handling, missing `cluster` bucketing, K8s node label flattening.
- [x] 10.3 Unit-test service-graph parser: zero-rate drop, ghost-pod fallback, cross-cluster edge labels, allowlist filtering on both endpoints.
- [x] 10.4 Unit-test external-name-pattern substitution: empty pattern (disabled), match on client only, match on server only, match on both, no match.
- [x] 10.5 Unit-test edge-ID generator: stability across rebuilds, RFC 4122 / UUIDv5 format, distinct IDs for distinct `(type, source, target)`.
- [x] 10.6 Unit-test request validation: missing/invalid `start`/`end`, `end <= start`, `window_too_large`, `end_in_future`.

## 11. Component tests (httptest mock upstream)

- [x] 11.1 Build a reusable `httptest.Server` that serves canned PromQL JSON for a fixture set keyed by query string.
- [x] 11.2 Component-test the build pipeline end to end: each request runs an upstream fan-out and returns a stable `ETag`; repeated identical requests return identical ETags.
- [x] 11.3 (Removed — was: singleflight coalescing test.)
- [x] 11.4 Component-test concurrency cap: requests beyond `--build-concurrency` return `503 capacity` with `Retry-After: 1`.
- [x] 11.5 Component-test per-build timeout: stalled upstream returns `503 timeout`.
- [x] 11.6 Component-test allowlist injection: PromQL strings sent to the mock contain the expected `cluster=~"..."` selector when `--clusters-allowlist` is set.
- [x] 11.7 Component-test cluster-too-large: probe-query stub returning a count above `--max-pods` returns `503 cluster_too_large`.

## 12. Golden tests

- [x] 12.1 Add a `testdata/golden/` tree of canned scenarios (single-cluster, two-cluster + cross-cluster edge, three-cluster + traversal, external-name-pattern matched).
- [x] 12.2 Implement golden-file harness with `-update` flag for `/v1/graph`, `/v1/graph/nodegraph`, `/v1/clusters`, `/v1/edge-types`.
- [x] 12.3 Snapshot the four endpoint responses for each scenario; commit `.golden.json` files.

## 13. Property-based tests

- [x] 13.1 Add a generator for random multi-cluster topology + service-graph edges + filter specs.
- [x] 13.2 Property: every edge endpoint resolves to a node in the response.
- [x] 13.3 Property: filtered set ⊆ unfiltered set; traversal depth never exceeded.
- [x] 13.4 Property: for cross-cluster edges, the resolved source-node `labels.cluster` differs from the resolved target-node `labels.cluster` (cross-cluster status is derived from node labels, not from edge labels).
- [x] 13.5 Property: edge IDs are unique per `(type, source, target)` and stable across re-runs.

## 14. Verification harness (capability: verification-harness)

- [x] 14.1 Author `deploy/kind/kind-config.yaml` (single cluster, 2 worker nodes).
- [x] 14.2 Author `deploy/kind/bootstrap.sh` that creates the Kind cluster and applies all manifests.
- [x] 14.3 Author manifests for in-cluster VictoriaMetrics single-node (`vmsingle`); confirm no `vmstorage`/`vmselect`/`vminsert`.
- [x] 14.4 ~~Implement `tests/harness/vm-fixtures/` Go program~~ — superseded: integration tests in `internal/integration/` ingest fixture series directly into a `testcontainers-go` VictoriaMetrics container via `POST /api/v1/import/prometheus` (Prometheus exposition format). No standalone binary, YAML config, or `/metrics` endpoint.
- [x] 14.5 ~~Author `tests/harness/vm-fixtures/fixtures.yaml`~~ — superseded: each test in `internal/integration/graph_e2e_test.go` constructs its own multi-cluster `kube_*` series, at least one cross-cluster service-graph series (where `server_k8s_pod_uid` resolves to a pod in a different cluster via the topology pod-UID index), and at least one `client="http://..."` series for external substitution.
- [x] 14.6 Author manifests for the `kube-state-graph` API server Deployment with `KSG_EXTERNAL_NAME_PATTERN="://"` set in the env section.
- [x] 14.7 Author `tests/smoke/run.sh` covering all assertions in the `verification-harness` spec (livez, readyz, clusters, edge-types, graph, multi-cluster filter, cross-cluster edge present, external node present, canonical schema enforced, metrics exposition).
- [x] 14.8 Author `deploy/kind/teardown.sh` for reproducible cluster deletion.
- [x] 14.9 Optional Grafana dashboard at `deploy/grafana/kube-state-graph-nodegraph.json` using the JSON / Infinity datasource against `/v1/graph/nodegraph`.

## 15. CI integration

- [x] 15.1 Add CI workflow stage running `go vet`, `golangci-lint`, `go test ./...` (unit + component + golden + property) on every PR.
- [x] 15.2 Add CI stage that runs `go test ./internal/integration/` on PRs touching `cmd/`, `internal/build/`, `internal/integration/`, or `local/kind/`, and on a nightly schedule. Integration tests use `testcontainers-go` with VictoriaMetrics; no separate fixtures harness.
- [x] 15.3 Publish a container image for `kube-state-graph` from CI for tagged commits; basic Dockerfile checked in.

## 16. Documentation

- [x] 16.1 Write `README.md`: what the server is, the data flow diagram, single-binary usage, env / flag reference.
- [x] 16.2 Write `docs/api.md`: response shapes, query parameters, 60 s alignment policy, status codes and `reason` values, ETag semantics.
- [x] 16.3 Write `docs/multi-cluster.md`: producer-side scrape `external_labels: { cluster: ... }` requirement (single source-cluster label on every series — Tempo / `servicegraph` connector configured with pod-UID dimensions only; remote/server-side cluster is recovered at build time from the topology pod-UID index).
- [x] 16.4 Write `docs/external-substitution.md`: `KSG_EXTERNAL_NAME_PATTERN` semantics, recommended values (`://`, `@`), examples of resulting graphs.
- [x] 16.5 Write `docs/operations.md`: self-metrics, alert recipes, `/livez` / `/readyz` semantics, capacity planning notes.

## 17. Pre-archive verification

- [x] 17.1 Run `openspec verify "add-k8s-pod-graph-api"` and confirm every requirement maps to an implementation file or test.
- [ ] 17.2 Confirm `go test ./... -cover` reports ≥ 80 % coverage on `internal/build`, `internal/graph`, `internal/api`. _(Current: deferred — needs additional handler / orchestrator tests.)_
- [ ] 17.3 Run the manual Grafana rig locally; record the resulting Grafana panel screenshot in `docs/`. _(Requires Docker + Kind on the host; not exercised in this session.)_
- [ ] 17.4 Tag a `v0.1.0` release once all preceding tasks are checked.

## 18. Container integration tests (capability: container-integration)

- [x] 18.1 Add `github.com/testcontainers/testcontainers-go` (and its `wait` subpkg) as a direct test dependency.
- [x] 18.2 Create `internal/integration/` package with `VMSuite` (`testify/suite.Suite`) whose `SetupSuite` starts a single VictoriaMetrics container (image pinned `victoriametrics/victoria-metrics:v1.107.0`) and `TearDownSuite` tears it down.
- [x] 18.3 Implement `IngestExpFmt(exposition string)` on `VMSuite` that POSTs to `<vm.URL>/api/v1/import/prometheus`, plus `WaitForSeries(query, budget)` polling helper.
- [x] 18.4 Implement readiness wait that polls VM `/-/ready` until 200 within a configurable budget (default 10 s); fail with `vm_not_ready` on timeout.
- [x] 18.5 Implement an in-process API-server-under-test factory (`StartAPIServer(configure func(*config.Config)) *httptest.Server`) on `VMSuite` that wires `api.New(...).Handler()`.
- [x] 18.6 Author absolute-timestamp fixtures and corresponding tests for: single-cluster `pod-runs-on-node`, cross-cluster `pod-calls-pod`, `KSG_EXTERNAL_NAME_PATTERN` substitution producing an external node, ETag round-trip 304, ETag determinism across repeated builds (`TestRepeatedRequestsReturnSameETag`), `/v1/clusters` discovery, `/v1/edge-types` shape.
- [x] 18.7 Per-test discriminator: each `SetupTest` writes fixtures labelled with `test="<TestName>"` so concurrent runs don't collide.
- [x] 18.8 CI workflow runs `go test ./...` on `ubuntu-latest`; the suite uses `SkipIfDockerUnavailable(t)` to skip cleanly on developer machines / runners without Docker.
- [x] 18.9 `httptest.Server` mock layer retained for sub-second inner-loop dev; container layer adds value at PR-feedback level. Decision documented in design D20.

## 19. Static-analysis suite (capability: static-analysis-suite)

- [x] 19.1 Update `.golangci.yml` to enable the curated linter set: `errcheck`, `gosimple`, `govet`, `ineffassign`, `staticcheck`, `unused`, `gocritic`, `exhaustive`, `copyloopvar`, `intrange`, `revive`, `errorlint`, `nilerr`, `gosec`, `gocyclo`, `gocognit`, `funlen`, `prealloc`, `bodyclose`, `unconvert`, `misspell`, `gofmt`, `goimports`, `dupl`, `unparam`, `mnd`.
- [x] 19.2 Configure complexity caps: `gocyclo` ≤ 15, `gocognit` ≤ 20, `funlen` ≤ 100 lines / 50 statements; relax for `_test.go` files.
- [x] 19.3 Add an `excludes` block for known-safe patterns (e.g., flag-binding magic numbers in `internal/config/config.go`) and document the rationale alongside each entry.
- [x] 19.4 Add a `vuln` CI job that installs `govulncheck` and runs `govulncheck ./...`; gate merges on its success.
- [x] 19.5 Update the CI workflow so `lint`, `vuln`, and `test` are independent jobs (no `needs` edges) running in parallel.
- [x] 19.6 Add `make lint`, `make vuln`, `make test` Makefile targets that mirror the CI configuration.
- [ ] 19.7 Run the full lint + vuln suite against the existing source tree; fix or `//nolint:<name>` (with rationale comments + tracked issues) the resulting findings. _(Deferred — `golangci-lint` not installed in this session; lint output cannot be triaged here.)_
- [ ] 19.8 Document the suite in `docs/operations.md` (link to `static-analysis-suite/spec.md` for the authoritative requirements).

## 20. Manual rig polish (capability: verification-harness — modified)

- [x] 20.1 Move integration-only assets out of CI-implying paths: `deploy/kind/` → `local/kind/`; smoke script → `local/kind/smoke.sh`. The `tests/harness/vm-fixtures/` standalone fixtures binary was dropped — integration testing moved to `internal/integration/` with `testcontainers-go`, and the local Kind rig uses real `kube-state-metrics` scraping the Kind cluster (no synthetic fixtures program needed).
- [x] 20.2 Add a Grafana Pod Deployment + Service to the rig manifests. Pin the Grafana image tag (`grafana/grafana:11.4.0`).
- [x] 20.3 Add a Grafana datasource provisioning ConfigMap pointing at the in-cluster `kube-state-graph` Service via the JSON / Infinity datasource.
- [x] 20.4 Add a Grafana dashboards provisioning ConfigMap that auto-imports `kube-state-graph-nodegraph.json` on Grafana boot.
- [x] 20.5 Document Grafana bootstrap credentials, NodePort, and expected URLs (banner in `bootstrap.sh`).
- [x] 20.6 Update `Makefile` targets: `local-up`/`local-down`/`local-smoke` aliases alongside the legacy `kind-*` / `smoke` names.
- [x] 20.7 Remove any CI workflow steps that invoke the manual rig (the user-modified `.github/workflows/ci.yml` already does this; verified).
- [ ] 20.8 Capture a Grafana panel screenshot post-bootstrap and commit it to `docs/grafana-screenshot.png`; reference it from `README.md`. _(Requires running the rig on a Docker host; deferred.)_

## 21. OpenAPI generation + offline Scalar UI (capability: api-docs / graph-api)

- [x] 21.1 Install `swag` v2 toolchain (`go install github.com/swaggo/swag/v2/cmd/swag@latest`); document the install step in `Makefile` (auto-installs on first `make docs`).
- [x] 21.2 Add document-level annotations on `cmd/kube-state-graph/main.go`: `@title`, `@version v1`, `@license.name Apache 2.0`, `@BasePath`, `@host` placeholder.
- [x] 21.3 Annotate every Gin handler in `internal/api/handlers.go` and `internal/api/docs.go` with `@Summary`, `@Description`, `@Tags`, `@Param` (per query parameter), `@Success` / `@Failure`, `@Router`. Covers all `/v1/*`, `/livez`, `/readyz`, `/metrics`, `/debug/last-queries`, `/openapi.yaml`, `/openapi.json`, `/docs`, `/docs/assets/*`.
- [x] 21.4 Error-envelope component (`errorBody`) referenced from every 4xx / 5xx response in annotations.
- [x] 21.5 Add `make docs` target that runs `swag init -g cmd/kube-state-graph/main.go --output docs --parseDependency --parseInternal`; placeholder `docs/swagger.{json,yaml}` checked in.
- [x] 21.6 Add `make check-docs` target: `make docs` followed by `git diff --exit-code docs/`.
- [x] 21.7 Add CI job (`docs-drift`) that runs the same; fails PRs with annotation drift.
- [x] 21.8 Implement `/openapi.yaml` and `/openapi.json` Gin handlers serving the embedded spec via `embed.FS`, with `Cache-Control: max-age=3600`, ETag, and `If-None-Match` 304.
- [x] 21.9 Vendor the Scalar API Reference standalone bundle into `internal/api/static/scalar/`. Pin via `VERSION`; placeholder bundle ships so the binary builds offline; `SHA256.expected` populated by the refresh script on first run.
- [x] 21.10 Add `make refresh-docs-ui` target invoking `scripts/refresh-docs-ui.sh`: downloads pinned Scalar version, validates SHA-256, writes bundle into `internal/api/static/scalar/`.
- [x] 21.11 Implement the `/docs` Gin handler: returns embedded HTML referencing `/docs/assets/scalar.js` (relative path) and `/openapi.yaml`. Test `TestDocs_OfflineInvariant` asserts no `https://` references in the served HTML.
- [x] 21.12 Implement the `/docs/assets/*path` Gin handler serving embedded files with `Cache-Control: public, max-age=86400, immutable`. Includes a path-traversal guard.
- [ ] 21.13 Implement the route ↔ spec drift contract test in `internal/api/`: parse `docs/swagger.json` via `kin-openapi`, walk `engine.Routes()`, assert bidirectional set-equality modulo allowlist. _(Deferred — adds `kin-openapi` dependency; placeholder spec covers all routes manually for now.)_
- [ ] 21.14 Add `docs/api.md` cross-link to the live `/docs` viewer; add a screenshot of the rendered Scalar UI. _(Screenshot needs running server + real Scalar bundle; deferred with 17.3.)_
- [x] 21.15 Add an offline-rendering integration test: `TestDocs_OfflineInvariant` (no `https://` script / link references) plus `TestDocs_AssetsServed` (200, non-empty body) plus `TestDocs_AssetsRejectsTraversal`.

## 22. testify migration (capability: static-analysis-suite + container-integration)

- [x] 22.1 Add `github.com/stretchr/testify` and `github.com/stretchr/testify/suite` as direct test dependencies; run `go mod tidy`.
- [x] 22.2 Add `testifylint` to `.golangci.yml` curated linters (D21) with `enable-all: true`.
- [x] 22.3 Refactor every existing `_test.go` file under `internal/` to use `assert` / `require` from testify. All 57 prior tests preserved; new docs / integration tests follow the same convention.
- [x] 22.4 Integration tests in `internal/integration/` are `suite.Suite`-based via `VMSuite`: `SetupSuite` starts VM container, `TearDownSuite` stops it, `SetupTest` writes discriminator-labelled fixtures.
- [x] 22.5 Run `make test` and (when available) `make lint` after migration; all 62 tests pass after migration.
- [ ] 22.6 Update CONTRIBUTING / docs to state the testify-only convention: no `t.Errorf` / bare `t.Fatal` in new tests. _(Deferred — minor doc task.)_

## 24. API-key authentication (capability: graph-api — modified)

- [x] 24.1 Add `internal/auth.KeySet` with `LoadFile`, `LoadCSV`, `Validate`, `Empty`, `Snapshot`. Constant-time compare per stored key (subtle.ConstantTimeCompare); always iterate the full set so match latency does not leak position. Atomic pointer swap on reload.
- [x] 24.2 Unit-test `internal/auth/keyset_test.go`: empty default, CSV parsing (dedup + blanks), file parsing (`#` comments, blank lines, dedup), reload (added key accepted, removed key rejected), missing-file error, empty-presented rejection.
- [x] 24.3 Add `internal/api/auth_middleware.go` enforcing `X-API-Key` on protected routes; open paths (`/livez`, `/readyz`, `/metrics`, `/openapi.*`, `/docs`, `/docs/assets/*`) bypass; empty keyset = no-op.
- [x] 24.4 Component-test `internal/api/auth_middleware_test.go`: missing header → 401, wrong key → 401, valid key → 200, open paths bypass without key, `/v1/graph` and `/debug/last-queries` require key when enabled, auth-disabled mode lets all routes through.
- [x] 24.5 Wire `--api-keys-file` / `--api-keys` / `--api-keys-reload-interval` flags + `KSG_API_KEYS_FILE` / `KSG_API_KEYS` / `KSG_API_KEYS_RELOAD_INTERVAL` env into `internal/config`. Validate file exists when path set.
- [x] 24.6 Load the keyset in `cmd/kube-state-graph/main.go`; start a periodic reload goroutine when file + interval are set; pass the keyset into `api.New`. Emit a startup `slog.Warn` when no keys are configured.
- [x] 24.7 Register `kube_state_graph_auth_rejected_total{reason}` counter in `internal/observability`. Increment from middleware on `missing` and `invalid` outcomes.
- [x] 24.8 Update swag annotations: document-level `@securityDefinitions.apikey ApiKeyAuth` (`@in header`, `@name X-API-Key`); per-handler `@Param X-API-Key`, `@Failure 401`, `@Security ApiKeyAuth` on `/v1/graph`, `/v1/graph/nodegraph`, `/v1/clusters`, `/v1/edge-types`, `/debug/last-queries`. Run `make docs` and commit `docs/swagger.{json,yaml,go}` + `internal/api/static/openapi/*`.
- [x] 24.9 Integration test: `internal/integration/graph_e2e_test.go::TestAPIKey_FileBacked_Enforced` exercises 401 (no header), 401 (wrong key), 200 (valid key), and confirms `/livez` stays open with auth on.
- [x] 24.10 Local rig: add `local/kind/manifests/05-api-key-secret.yaml` with two dev keys; update `30-api-server.yaml` to mount the Secret at `/etc/kube-state-graph/api-keys` and pass `KSG_API_KEYS_FILE` + `KSG_API_KEYS_RELOAD_INTERVAL` envs; update `40-grafana.yaml` datasource provisioning with `httpHeaderName1=X-API-Key` + `secureJsonData.httpHeaderValue1`.
- [x] 24.11 Update `local/kind/smoke.sh`: assert `/livez` open, assert `/v1/edge-types` without header → 401, assert `/v1/edge-types` with `X-API-Key: $KSG_SMOKE_API_KEY` → 200; thread `AUTH_HEADER` through every protected curl.
- [x] 24.12 Document Authentication in `docs/api.md` (header, exempt routes, 401 contract), `docs/operations.md` (rotation procedure, metric, K8s Secret mount), and `README.md` (env / flag table).

## 23. Pod-name filter (capability: graph-api — modified)

- [x] 23.1 Extend `graph.Scope` with `Pods map[string]struct{}` (matches `PodNode.Name`). Update `graph.NewScope` signature to accept the new repeatable string slice and convert via `stringSet`. The `pod_uid` filter (matching the canonical pod UID) was considered and dropped — pod UIDs are opaque internal identifiers callers cannot obtain without first making a `/v1/graph` call.
- [x] 23.2 In `graph.nodePassesFilters`, add a branch:
  - For `PodNode`: must match `Pods` (by `n.Name()`) when the set is non-empty.
  - For `K8sNode`, `PVCNode`, `ExternalNode`: when the pod filter is set, drop directly in `nodePassesFilters`. Endpoints survive only via the existing edge-endpoint re-add pass in `filterEdges` (for in-scope pods' incident edges).
- [x] 23.3 In `graph.preserveCrossClusterEdge`, return `false` when `Pods` is set so the cluster-scoped partner-rehydration rule does NOT fire (caller named the exact pod set).
- [x] 23.4 In `internal/api/handlers.go` `parseGraphRequest` (or equivalent), parse `q["pod"]` and pass it to `graph.NewScope`. Apply identical wiring to both `/v1/graph` and `/v1/graph/nodegraph` handlers.
- [x] 23.5 Add `@Param pod` swag annotation (repeatable, `query`, `[]string`, `collectionFormat(multi)`) on both handlers; mention it in the `@Description`. Run `make docs` and commit the regenerated `docs/swagger.{json,yaml}` so `make check-docs` stays green.
- [x] 23.6 Unit tests in `internal/graph/project_test.go`: pod-name filter narrows correctly; pod name shared across clusters returns both; pod filter AND cluster filter; pod filter does NOT trigger cross-cluster preservation; unknown pod name returns empty.
- [x] 23.7 Component tests in `internal/api/server_test.go`: HTTP `?pod=` maps through to scope correctly; combines with existing filters; unknown values return 200 + empty. _(Coverage placed in `internal/integration/graph_e2e_test.go` next to the existing edge-type filter integration tests, which exercise the full HTTP→Project→serialise pipeline against a real VM container — a closer fit than the mock-PromQL `server_test.go` which only validates request parsing.)_
- [x] 23.8 Property test in `internal/graph/property_test.go`: when `Pods` is set, every returned pod node satisfies the filter, and no cross-cluster partner pod outside the filter is returned.
- [x] 23.9 Add or reuse a golden scenario in `internal/api/testdata/golden/` exercising `?pod=...` for one well-known pod name; refresh with `go test ./internal/api/ -update -run Golden`.
- [x] 23.10 Update `docs/api.md` filter-parameter table with the new param and its semantics (exact match, repeatable, AND/OR rules, no cross-cluster partner preservation).

## 25. Type-agnostic `name` filter (capability: graph-api — modified)

Operators want to anchor a graph view on **any** node — pod, K8s node, PVC, or external endpoint (e.g. `?name=worker-3` to centre the view on a K8s node, `?name=checkout-data` to centre it on a PVC). Section 25 replaces the prior pod-only filter with a single type-agnostic `?name=` filter that matches `n.Name()` across `PodNode`, `K8sNode`, `PVCNode`, and `ExternalNode`. Edge retention rule: an edge survives when at least one resolved endpoint is in scope; the missing endpoint is re-added from `g.NodesByID` provided it passes the non-cluster filters (namespace).

- [x] 25.1 Rename `graph.Scope.Pods` → `graph.Scope.Names map[string]struct{}` (matches `n.Name()` for any node type). Rename `Scope.PodFilterActive` → `Scope.NameFilterActive`. Update `graph.NewScope` signature: replace the `pods []string` parameter with `names []string` (keep the same position so callers update mechanically).
- [x] 25.2 Rewrite `graph.nodePassesFilters` so the name branch applies uniformly to every node type: when `len(scope.Names) > 0`, drop any node whose `n.Name()` is not in the set. Remove the type-switch that special-cased pods. K8sNode / PVCNode / ExternalNode now match by name directly when the filter is set, and survive into the primary node set without waiting for the edge-endpoint re-add pass.
- [x] 25.3 Update `graph.filterEdges` so the partner re-add rule is unified:
  - When an edge has exactly one endpoint in `nodes`, re-add the missing endpoint from `g.NodesByID` if it passes `nodePassesNonClusterFilters` (i.e. namespace check).
  - Drop the `preservePodFilterPartner` helper — its job (re-adding non-pod endpoints when one side is an in-scope pod) is now subsumed by the unified rule.
  - Drop the `preserveCrossClusterEdge` helper — the cross-cluster `pod-calls-pod` partner-rehydration case is also subsumed by the unified rule (out-of-scope-cluster partner is re-added because it passes the namespace check).
  - No name-specific suppression: anchoring on a named node intentionally surfaces incident edges with their partner endpoints; otherwise the rendered graph would have dangling edges.
- [x] 25.4 In `internal/api/handlers.go` `parseGraphRequest`, replace `q["pod"]` parsing with `q["name"]` parsing on both `/v1/graph` and `/v1/graph/nodegraph`. The `pod` query parameter is no longer recognised; unknown query parameters continue to be ignored silently (existing convention).
- [x] 25.5 Update swag annotations on both handlers: remove `@Param pod` and add `@Param name` (`query`, `[]string`, `collectionFormat(multi)`, repeatable). Update each handler's `@Description` to describe the new cross-type semantics. Run `make docs` and commit regenerated `docs/swagger.{json,yaml,go}` plus `internal/api/static/openapi/*` so `make check-docs` stays green.
- [x] 25.6 Replace pod-filter unit tests in `internal/graph/project_test.go`:
  - Pod match: `?name=checkout` returns the `checkout` PodNode and re-adds K8s-node / PVC / external endpoints of its incident edges.
  - K8s-node match: `?name=node-a` returns the `K8sNode` named `node-a` and re-adds pods that run on it.
  - PVC match: `?name=data` returns the matching `PVCNode` and re-adds pods that mount it.
  - Cross-type match: `?name=worker-1` returns BOTH a pod and a K8s node when both happen to share the name.
  - Combined filters: `?name=api&cluster=cluster-alpha` only returns the cluster-alpha node(s).
  - Cross-cluster anchor: `?name=frontend` retains the cross-cluster `pod-calls-pod` edge AND re-hydrates the partner pod in the other cluster (unified edge-endpoint rule).
  - Unknown name: 200 with empty `nodes` and `edges`.
- [x] 25.7 Update property test in `internal/graph/property_test.go`: when `Names` is set, every node in the result either has `n.Name() ∈ Names` or is a missing edge endpoint re-added by the unified partner-rehydration rule (and that endpoint is incident on at least one retained edge whose other end matches the name set).
- [x] 25.8 Update integration coverage in `internal/integration/graph_e2e_test.go`: replace the `?pod=` test cases with `?name=` cases that exercise pod, K8s-node, and PVC anchors. Drop fixtures that only exist to test pod-only narrowing.
- [x] 25.9 Refresh golden scenarios under `internal/api/testdata/golden/`: rename `pod=...` cases to `name=...`; add at least one new case where the anchor is a K8s node. Refresh via `go test ./internal/api/ -update -run Golden`.
- [x] 25.10 Update `docs/api.md` filter-parameter table: remove the `pod` row, add a `name` row describing the cross-type match (exact equality on `n.Name()`, repeatable, OR within param / AND across params, no cross-cluster partner preservation when set, edge endpoints of in-scope nodes re-added subject to namespace).
- [x] 25.11 Update `docs/api.zh-tw.md` (if present) and the OpenSpec zh-tw mirrors (`design.zh-tw.md`, `proposal.zh-tw.md`) to reflect the rename.
- [x] 25.12 Run `openspec validate "add-k8s-pod-graph-api"` and confirm the modified spec parses; run `make test` + `make check-docs` after the code change.

## 26. ETag wording refactor (capability: graph-api — modified)

Operators reviewing the docs flagged that v1 framing of ETag as "HTTP-layer caching" is misleading: ETag is a **response validator** (RFC 9110 §8.8.3) used for **conditional GET / revalidation** (RFC 9110 §13.1), not a cache. v1 ships no server-side cache; the ETag's purpose is to let intermediaries and clients revalidate cheaply with `If-None-Match` and receive `304 Not Modified` when the response body would be byte-identical. Whether any party caches the response body is a client / intermediary policy, not a server feature. Section 26 retitles and rewords the affected sections so the contract reads correctly.

- [x] 26.1 In `openspec/changes/add-k8s-pod-graph-api/design.md` D6, retitle "HTTP-layer caching only (ETag); no in-process result cache" → "Conditional GET via response validator (ETag); no in-process result cache". Reword the body to:
  - State that the server emits an `ETag` strong validator computed as `sha256(body)`.
  - Frame `If-None-Match` → `304 Not Modified` as **revalidation**, not cache hit.
  - Note that fixed-TTL `Cache-Control` headers on `/v1/edge-types`, `/openapi.*`, `/docs`, and `/docs/assets/*` are independent of the validator — they apply because those resources have stable, long-lived content, not because v1 ships a build cache.
  - Keep the existing determinism prerequisites and "no singleflight" / "future cache mechanism" subsections unchanged.
- [x] 26.2 In `openspec/changes/add-k8s-pod-graph-api/specs/graph-api/spec.md`, audit any requirement / scenario that calls ETag a "cache". Where applicable, restate the contract as a **conditional GET / response validator** (e.g. "the server SHALL emit an `ETag` strong validator…", "clients MAY revalidate via `If-None-Match`…"). The byte-identity determinism contract stays as-is; only the surrounding wording changes.
- [x] 26.3 Update `docs/api.md` Headers section for `/v1/graph`:
  - Replace "save bandwidth via revalidation" with explicit framing: ETag is a strong validator on the response body; clients use it for HTTP conditional GET; `304 Not Modified` is returned when the validator matches. Whether anyone caches the body is a client / intermediary concern, not a server feature.
  - Restate why no `Cache-Control` is emitted on `/v1/graph` and `/v1/graph/nodegraph`: the server has no view of how long a freshly built graph is "fresh" without re-querying upstream, so it leaves cacheability decisions to the client.
- [x] 26.4 Mirror the same wording adjustments into `design.zh-tw.md` D6 and the zh-tw mirror (if present) of `docs/api.md`.
- [x] 26.5 Update `CLAUDE.md`'s "Load-bearing design rules" bullet on ETag determinism only if the wording references "caching" — keep the byte-identity invariant verbatim.

## 27. Configuration surface simplification (capability: graph-api — modified, cluster-topology-source — modified)

The configuration surface accumulated knobs whose value did not justify the cost of carrying them: `--max-window` / `--max-skew` duplicate guards already provided by upstream VictoriaMetrics search limits and trivially-handled empty results; `--max-pods` requires an extra probe round-trip whose only signal is bounded again by the same VM-side limits; `--cluster-discovery-lookback` has no observed reason to deviate from `1h`; `--enable-debug` toggles a debug endpoint that was only ever a 501 stub; `--build-concurrency` is a per-instance semaphore whose tuning duplicates HPA's signal at finer granularity but worse observability. Section 27 removes all of them, adds `--api-timeout` as a peer to `--build-timeout` for non-graph upstream calls, and realigns timeout / upstream-failure responses to the RFC 9110 conventions (`504 Gateway Timeout`, `502 Bad Gateway`).

- [ ] 27.1 Remove `--max-window` flag, `KSG_MAX_WINDOW` env var, `Config.MaxWindow` field, default, and `Validate()` rule. Drop the `end - start > MaxWindow` guard in `parseGraphRequest` (`internal/api/handlers.go`). Drop `window_too_large` from `internal/api/errors.go` and any error-mapping table. Drop the corresponding scenario / requirement in spec / design / docs. Bounded query cost is delegated to upstream VictoriaMetrics search limits.
- [ ] 27.2 Remove `--max-skew` flag, `KSG_MAX_SKEW` env var, `Config.MaxSkew` field, default, and `Validate()` rule. Drop the `end > now + MaxSkew` guard in `parseGraphRequest`. Drop `end_in_future` from `internal/api/errors.go`. Future-time queries return empty PromQL results which the caller surfaces as an empty graph; no KSG-side guard is necessary.
- [ ] 27.3 Remove `--max-pods` flag, `KSG_MAX_PODS` env var, `Config.MaxPods` field, default, and `Validate()` rule. Remove the `count(kube_pod_info)` probe call site (`probeClusterSize` in `internal/build/builder.go` or equivalent), the `QClusterSizeProbe` query template in `internal/promql/queries.go`, and the `ReasonClusterTooLarge` build error / `cluster_too_large` HTTP reason in `internal/build/errors.go` + `internal/api/errors.go`. Remove the corresponding spec scenario in `cluster-topology-source/spec.md` and `graph-api/spec.md`.
- [ ] 27.4 Remove `--cluster-discovery-lookback` flag, `KSG_CLUSTER_DISCOVERY_LOOKBACK` env var, `Config.ClusterDiscoveryLookback` field, default, and `Validate()` rule. In `internal/api/handlers.go::discoverClusters`, replace `s.cfg.ClusterDiscoveryLookback` with a package-level constant `clusterDiscoveryLookback = time.Hour`. Update the design / spec text to state the lookback is fixed at `1h`.
- [ ] 27.5 Remove `--enable-debug` flag, `KSG_ENABLE_DEBUG` env var, `Config.EnableDebug` field, default, and `Validate()` rule. Remove the `/debug/last-queries` Gin route registration (`internal/api/server.go` / `routes.go`) and its handler (`internal/api/debug.go` if present). Update `internal/api/auth_middleware.go` to remove `/debug/*` from any exempt-path list (no-op once routes are gone).
- [ ] 27.6 Update `cmd/kube-state-graph/main.go` HTTP server `WriteTimeout` derivation (`cfg.BuildTimeout + 5*time.Second`) — keep as-is; it does not depend on any removed field.
- [ ] 27.7 Update unit tests in `internal/config/config_test.go`: remove cases asserting parsing / validation of removed fields. Update the round-trip "all flags" test to drop the removed flags.
- [ ] 27.8 Update component tests in `internal/api/server_test.go`: drop `TestParseRequest_WindowTooLarge`, `TestParseRequest_EndInFuture`, `TestClusterTooLarge`, and any `/debug/last-queries` test. Adjust any test that wires `Config{ MaxWindow, MaxSkew, MaxPods, ClusterDiscoveryLookback, EnableDebug }` to drop those fields.
- [ ] 27.9 Update integration tests in `internal/integration/graph_e2e_test.go`: drop the `cfg.ClusterDiscoveryLookback = 365 * 24 * time.Hour` override on the test rig (the constant now applies). Drop any `MaxWindow` / `MaxSkew` / `MaxPods` tweaks. Keep tests that exercise `?cluster=`, `?name=`, etc.
- [ ] 27.10 Update Swag annotations on `/v1/graph` and `/v1/graph/nodegraph`: remove `--max-window` / `--max-skew` references from `@Description` and `@Param end "..."`. Remove the `/debug/last-queries` route annotations entirely. Remove `400 window_too_large`, `400 end_in_future`, `503 cluster_too_large` from `@Failure` blocks. Run `make docs` and commit regenerated `docs/swagger.{json,yaml,go}` plus `internal/api/static/openapi/*`.
- [ ] 27.11 Update `docs/api.md`: drop `--max-window` / `--max-skew` from the `end` parameter description, drop the `window_too_large` / `end_in_future` / `cluster_too_large` rows from the status-code table, drop the `--cluster-discovery-lookback` mention from `/v1/clusters` (replace with "fixed `1h` lookback"), drop the entire `/debug/last-queries` section.
- [ ] 27.12 Update `CLAUDE.md` "Request lifecycle" diagram and "Load-bearing design rules" bullets to drop `--max-window` / `--max-skew` validation lines and any `/debug/*` references. Mention upstream VictoriaMetrics search limits as the bounded-cost mechanism.
- [ ] 27.13 Update `local/kind/manifests/30-api-server.yaml` and any other manifests / `Makefile` snippets that set the removed env vars.
- [ ] 27.14 Run `openspec validate "add-k8s-pod-graph-api"` (must stay green); run `make test` + `make check-docs`; run `go vet ./...` + `golangci-lint run` (when available locally) to catch dead code (`probeClusterSize`, `QClusterSizeProbe`, `ReasonClusterTooLarge`, `validateWindow`, `validateSkew`, debug handler).

### 27.A Add `--api-timeout` (graph-api — modified)

- [ ] 27.A.1 Add `Config.APITimeout time.Duration` (default `5 * time.Second`). Wire `--api-timeout` flag and `KSG_API_TIMEOUT` env in `internal/config/config.go`. `Validate()` SHALL require `APITimeout > 0`.
- [ ] 27.A.2 In `internal/api/handlers.go::discoverClusters`, replace the implicit timeout (currently inherited from the request context) with an explicit `ctx, cancel := context.WithTimeout(c.Request.Context(), s.cfg.APITimeout); defer cancel()` wrapping the upstream PromQL `Instant` call. On `errors.Is(err, context.DeadlineExceeded)` return `504 Gateway Timeout` with `reason: "timeout"`.
- [ ] 27.A.3 In `/readyz` handler, replace the hardcoded `time.Second` probe timeout with `s.cfg.APITimeout`. Behaviour on probe failure stays `503 Service Unavailable` (k8s convention; not a gateway-timeout case).
- [ ] 27.A.4 Update Swag annotations on `/v1/clusters` to include `@Failure 504 {object} errorBody "Upstream timeout"`. Run `make docs` and commit regenerated artefacts.
- [ ] 27.A.5 Unit test `internal/config/config_test.go`: parses `--api-timeout` flag and `KSG_API_TIMEOUT` env; rejects zero / negative.
- [ ] 27.A.6 Component test `internal/api/server_test.go`: stalled discovery upstream → 504 with `reason: "timeout"`.
- [ ] 27.A.7 Update `docs/api.md` to describe `--api-timeout` semantics (which endpoints honour it; default `5s`); update README env table.

### 27.B Remove `--build-concurrency` and the `503 capacity` reason (graph-api — modified)

- [ ] 27.B.1 Remove `Config.BuildConcurrency` field, `--build-concurrency` flag, `KSG_BUILD_CONCURRENCY` env, default, and `Validate()` rule from `internal/config/config.go`.
- [ ] 27.B.2 Delete `internal/build/orchestrator.go` (the `Orchestrator` type with its semaphore + per-build context-timeout wrapper). Move the per-build `context.WithTimeout(ctx, cfg.BuildTimeout)` wrapping directly into `internal/api/handlers.go::handleGraph` and `handleNodeGraph`, immediately around `s.builder.Build(...)`.
- [ ] 27.B.3 Update `internal/api/server.go::New` (and the test factory in `internal/integration/...`) to drop the `NewOrchestrator(...)` call and pass the `*Builder` directly into the `Server` struct (or whatever the handler uses).
- [ ] 27.B.4 Remove `kube_state_graph_build_concurrency` gauge and `kube_state_graph_build_rejected_total{reason="capacity"}` counter labels from `internal/observability/metrics.go`. Keep `build_rejected_total{reason="timeout"}` (still load-bearing for graph endpoints).
- [ ] 27.B.5 Remove `ReasonCapacity` constant (and the `503 capacity` mapping in `internal/api/errors.go`); collapse `ReasonTimeout` mapping target from `503` to `504`. Update `mapBuildError` accordingly.
- [ ] 27.B.6 Update `internal/build/errors.go` `Reason` enum: drop `ReasonCapacity`; keep `ReasonTimeout` and `ReasonUpstream`. Update all call sites and switch / mapping tables.
- [ ] 27.B.7 Drop the spec scenario `Scenario: Build over capacity` (already removed in this section's spec edits) and the prior `Scenario: Upstream stalls beyond timeout` `Retry-After: 1` assertion text (no `Retry-After` header on 504 — clients should not retry-spin a gateway-timeout fault without backoff).
- [ ] 27.B.8 Update integration tests in `internal/integration/graph_e2e_test.go`: drop any `cfg.BuildConcurrency = N` overrides; drop `TestBuild_Capacity` and `TestRetryAfterHeader_Capacity` if present.
- [ ] 27.B.9 Update component tests `internal/api/server_test.go`: rename `TestBuild_Timeout` assertions from `503 timeout` to `504 timeout`; drop `TestBuild_Capacity`. Add a regression test that two concurrent build requests both succeed when the upstream is responsive (no semaphore = no serialisation).
- [ ] 27.B.10 Update Swag annotations on `/v1/graph` and `/v1/graph/nodegraph`: replace `@Failure 503 {object} errorBody "Build concurrency exhausted"` with `@Failure 504 {object} errorBody "Build timeout"`. Drop the `Retry-After` mention from the timeout description. Run `make docs` and commit regenerated artefacts.
- [ ] 27.B.11 Update `docs/operations.md` capacity-planning section to recommend HPA tuning (CPU + p95 latency targets) instead of `--build-concurrency`.
- [ ] 27.B.12 Update `local/kind/manifests/30-api-server.yaml` to drop the `KSG_BUILD_CONCURRENCY` env. Add a `resources:` block with sensible defaults (request 100 m / 128 Mi, limit 500 m / 512 Mi) to make the HPA-driven model concrete in the local rig.

### 27.C RFC-9110 status realignment for upstream errors (graph-api — modified)

- [ ] 27.C.1 In `internal/api/errors.go::mapBuildError`, map `ReasonTimeout → 504 Gateway Timeout` and `ReasonUpstream → 502 Bad Gateway`. Document the choice in a comment referencing RFC 9110 §15.6.3 (502) and §15.6.5 (504).
- [ ] 27.C.2 Audit every `writeError(c, http.StatusServiceUnavailable, ...)` site in `internal/api/`. Keep `503` only for `/readyz` (probe failed) and any genuine "service is going away / not yet ready" cases. Build / upstream paths SHALL NOT use 503.
- [ ] 27.C.3 Audit every `errors.Is(err, context.DeadlineExceeded)` (and the equivalent for the prom client's wrapped errors) and ensure the matching error reason maps to `504 timeout` not `503`.
- [ ] 27.C.4 Update `docs/api.md` status-code table to reflect the new mappings (already edited in this section's docs pass).
- [ ] 27.C.5 Update component tests asserting `503 timeout` / `503 capacity` → adjust to `504 timeout` / removal respectively. Update integration tests `internal/integration/graph_e2e_test.go::TestBuild_*` similarly.
- [ ] 27.C.6 `openspec validate "add-k8s-pod-graph-api"` and `make check-docs` after 27.A / 27.B / 27.C.

## 28. Drop the cluster allowlist (capability: graph-api — modified, cluster-topology-source — modified, pod-service-graph — modified)

Cluster scoping is a caller-side concern via the `?cluster=` filter on `/v1/graph`. Bounded upstream cost is delegated entirely to upstream VictoriaMetrics search limits.

- [ ] 28.1 Remove `Config.ClustersAllowlist`, the `--clusters-allowlist` flag, and the `KSG_CLUSTERS_ALLOWLIST` env from `internal/config/config.go`.
- [ ] 28.2 Remove `promql.AllowlistRegex` (and its tests) from `internal/promql/`. Drop the `allowlistRegex` parameter from `promql.Render`; collapse the `clusterSel` injection so PromQL templates emit `kube_pod_info[<window>]` etc. unconditionally.
- [ ] 28.3 Drop the `allowlistRegex string` parameter from `ReadTopology`, `ReadServiceGraph`, and any helper they delegate to in `internal/build/`.
- [ ] 28.4 Update `internal/build/build.go::Build` to call `ReadTopology` / `ReadServiceGraph` without the allowlist argument.
- [ ] 28.5 Update `internal/api/handlers.go::discoverClusters` to render the discovery query without an allowlist arg and drop the `allowSet` intersection. Remove `stringSliceToSet` if it has no other callers.
- [ ] 28.6 Update `cmd/kube-state-graph/main.go` startup log to drop the `clusters_allowlist` field.
- [ ] 28.7 Update unit / component tests: drop `TestAllowlistRegex_*`, drop any `cfg.ClustersAllowlist = …` overrides in `internal/api/server_happy_test.go` and `internal/integration/graph_e2e_test.go`. Adjust assertions that expected `cluster=~"..."` selectors in mocked PromQL strings.
- [ ] 28.8 Update Swag annotations on `/v1/clusters` to drop the `--clusters-allowlist` mention; run `make docs` and commit regenerated artefacts.
- [ ] 28.9 Update `docs/api.md` and `README.md` (+ zh-tw mirror) to drop the allowlist row from env / flag tables.
- [ ] 28.10 Update `CLAUDE.md`'s "Load-bearing design rules" bullet on allowlist injection — replace with the simpler statement that the server loads every cluster present in upstream VM and that caller-side scoping uses `?cluster=`.
- [ ] 28.11 Update `local/kind/manifests/30-api-server.yaml` (and any other manifest) to drop `KSG_CLUSTERS_ALLOWLIST` env if present.
- [ ] 28.12 Run `openspec validate "add-k8s-pod-graph-api"` and `make test` + `make check-docs`; `go vet ./...` for dead imports.
