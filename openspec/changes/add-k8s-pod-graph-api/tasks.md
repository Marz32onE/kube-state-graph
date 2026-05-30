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
- [x] 8.2 Implement `GET /v1/graph` handler: parse + validate `start`, `end`, filter params, traversal params; align window + build + project + serialise.
- [x] 8.3 Implement Cytoscape.js serialiser: `{ apiVersion, clusters, elements: { nodes, edges } }` with canonical node/edge `data` shape. The body MUST NOT contain time-varying or echo-of-input fields — body shape is fixed so that identical inputs against the same upstream state produce a byte-identical body.
- [x] 8.4 Implement `GET /v1/graph/nodegraph` handler: project → Grafana Node Graph JSON (`nodes_fields`/`nodes`/`edges_fields`/`edges`); map `name`→`title`, cluster·namespace→`subTitle`, `type`→`mainStat`, edge `type`→edge `mainStat`, `secondaryStat` omitted.
- [x] 8.5 Implement `GET /v1/clusters` handler: live discovery query against VictoriaMetrics, intersected with `--clusters-allowlist`. No in-process discovery cache.
- [x] 8.6 Implement `GET /v1/edge-types` handler: serialise the in-code registry; long `Cache-Control` on the response.
- [x] 8.7 No HTTP cache validator on `/v1/graph` / `/v1/graph/nodegraph` / `/v1/clusters` (no `ETag`, no `Last-Modified`). No `Cache-Control` either — cacheability is a future-iteration concern.
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
- [x] 11.2 Component-test the build pipeline end to end: each request runs an upstream fan-out; repeated identical requests return byte-identical response bodies.
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
- [x] 16.2 Write `docs/api.md`: response shapes, query parameters, 60 s alignment policy, status codes and `reason` values.
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
- [x] 18.6 Author absolute-timestamp fixtures and corresponding tests for: single-cluster `pod-runs-on-node`, cross-cluster `pod-calls-pod`, `KSG_EXTERNAL_NAME_PATTERN` substitution producing an external node, body determinism across repeated builds, `/v1/clusters` discovery, `/v1/edge-types` shape.
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
- [x] 21.8 Implement `/openapi.yaml` and `/openapi.json` Gin handlers serving the embedded spec via `embed.FS`, with `Cache-Control: max-age=3600`.
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

## 29. OTLP tracing and logging (capability: otlp-observability — added)

Per design D25 and `specs/otlp-observability/spec.md`. Wires OpenTelemetry tracing + slog → OTLP logs through the existing build pipeline. No new CLI flags; OTel-standard env vars only. Telemetry is no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset.

### 29.A Dependencies and module bootstrap

- [x] 29.A.1 Add Go module dependencies: `go.opentelemetry.io/otel`, `go.opentelemetry.io/otel/sdk`, `go.opentelemetry.io/otel/sdk/log`, `go.opentelemetry.io/otel/exporters/otlp/otlptrace`, `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`, `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`, `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc`, `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp`, `go.opentelemetry.io/otel/semconv/v1.27.0`, `go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin`, `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`, `go.opentelemetry.io/contrib/bridges/otelslog`. Pin versions to current stable; run `go mod tidy`.
- [x] 29.A.2 Add a design-doc note (D25 already covers it) listing the new direct deps so the "no casual deps" rule in `CLAUDE.md` is satisfied.
- [x] 29.A.3 Update `.golangci.yml` if any new lint rule fires on the OTel SDK call sites (e.g. `errcheck` on deferred `Shutdown`); document the suppression. (Verified: `golangci-lint run` reports zero new issues against `internal/telemetry/`, `internal/api/tracing*`, the updated `internal/build/build.go`, or `internal/promql/client.go`. Two pre-existing trailing-whitespace warnings in `internal/config/config.go` and `internal/integration/graph_e2e_test.go` are unrelated to this change.)

### 29.B Telemetry init module

- [x] 29.B.1 New file `internal/telemetry/telemetry.go` exposing `Init(ctx context.Context, version string) (shutdown func(context.Context) error, enabled bool, err error)`. Reads `OTEL_EXPORTER_OTLP_ENDPOINT` (and per-signal overrides) to decide enabled/disabled.
- [x] 29.B.2 Build `*resource.Resource` via `resource.New(ctx, resource.WithFromEnv(), resource.WithProcess(), resource.WithHost(), resource.WithTelemetrySDK(), resource.WithAttributes(semconv.ServiceName("kube-state-graph"), semconv.ServiceVersion(version), semconv.ServiceInstanceID(uuid.NewString())))`. Honour `OTEL_SERVICE_NAME` override.
- [x] 29.B.3 Build trace exporter switching on `OTEL_EXPORTER_OTLP_PROTOCOL` (`grpc` default, `http/protobuf` alt). Wrap in `sdktrace.NewBatchSpanProcessor`. Install `sdktrace.NewTracerProvider(...)`. When disabled install `noop.NewTracerProvider()`.
- [x] 29.B.4 Build log exporter analogously and install `sdklog.NewLoggerProvider(...)` (or no-op).
- [x] 29.B.5 Set global propagator `propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})`.
- [x] 29.B.6 Return a `shutdown` closure that calls both providers' `Shutdown(ctx)` in sequence and joins errors via `errors.Join`.
- [x] 29.B.7 Unit tests `internal/telemetry/telemetry_test.go`: (a) endpoint unset → enabled=false, returned providers are no-op; (b) endpoint set → enabled=true, exporter target matches; (c) protocol HTTP/protobuf selects the http exporter; (d) `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` reflected in the resource attribute set; (e) `OTEL_TRACES_SAMPLER=parentbased_traceidratio` + `OTEL_TRACES_SAMPLER_ARG=0.25` parsed correctly.

### 29.C slog OTLP bridge

- [x] 29.C.1 In `internal/telemetry/`, add `NewSlogHandler(local slog.Handler) slog.Handler` returning a multi-handler that fans out to the existing local stderr handler and `otelslog.NewHandler("kube-state-graph")`. When the global `LoggerProvider` is no-op the bridge is a no-op too.
- [x] 29.C.2 Configure the local stderr handler with a `ReplaceAttr` that, when called from a `slog.LogAttrs(ctx, ...)`, surfaces `trace_id` / `span_id` from `trace.SpanContextFromContext(ctx)`. (The OTLP side gets it automatically through the bridge.)
- [x] 29.C.3 Replace `slog.New(...)` construction in `cmd/kube-state-graph/main.go` to use the multi-handler. Default global logger via `slog.SetDefault(...)` so `slog.InfoContext(ctx, ...)` calls in handlers pick it up.
- [x] 29.C.4 Audit existing `slog.*` call sites in `internal/api/`, `internal/build/`, `internal/promql/` and convert log calls inside request-scoped paths to `*Context(ctx, ...)` variants so trace correlation kicks in.
- [x] 29.C.5 Unit test: log a record with a synthetic `context.Context` carrying a known `SpanContext`; assert the captured stderr line contains the expected `trace_id` and `span_id` keys.
- [x] 29.C.6 Negative test: ensure no log line emitted from the auth middleware contains the literal sentinel API key value (use `bytes.Contains` against captured output).

### 29.D HTTP request tracing (otelgin) and outbound propagation

- [x] 29.D.1 In `internal/api/server.go`, install `otelgin.Middleware("kube-state-graph", otelgin.WithFilter(...))` on the `/v1/*` and `/debug/*` route groups only. Mount `/livez`, `/readyz`, `/metrics`, `/openapi.*`, `/docs`, `/docs/assets/*` on a separate group without the middleware.
- [x] 29.D.2 Map non-2xx responses to span status `Error` with description = `build.Reason.String()` for typed errors, else the raw `error` string.
- [x] 29.D.3 Wrap the Prometheus HTTP client transport with `otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithPropagators(otel.GetTextMapPropagator()))` so PromQL HTTP calls inject `traceparent` outbound and emit a client span per upstream call.
- [x] 29.D.4 Component test in `internal/api/server_happy_test.go` (or sibling): inbound request with explicit `traceparent` header → assert the recorded span's parent matches; mock collector with an in-memory exporter (`tracetest.NewInMemoryExporter`).
- [x] 29.D.5 Component test: probes `/livez`, `/readyz`, `/metrics` produce zero spans on the in-memory exporter.
- [x] 29.D.6 Component test: failed `/v1/graph` returns 502 and the captured server span has `Error` status with description `"upstream"` (or whatever `build.Reason` maps to 502). (Description may be overwritten by otelgin's HTTP-status hook; test asserts `Error` code + presence of an exception event recording the build error.)

### 29.E Build pipeline span instrumentation

- [x] 29.E.1 Add a package-level `tracer = otel.Tracer("kube-state-graph")` accessor in `internal/build/`, `internal/promql/`, `internal/graph/`, `internal/api/`.
- [x] 29.E.2 In `internal/build/build.go::Build`, open `ctx, span := tracer.Start(ctx, "kube-state-graph.build", trace.WithAttributes(...))` with `kube_state_graph.window_seconds` and `kube_state_graph.end_unix`. On success set `kube_state_graph.cluster_count`, `graph.node.count`, `graph.edge.count`. On error `span.RecordError(err); span.SetStatus(codes.Error, reason.String())`.
- [x] 29.E.3 In `internal/build/topology.go::ReadTopology` and `internal/build/servicegraph.go::ReadServiceGraph`, wrap each errgroup leg in `tracer.Start(ctx, "prometheus.query", trace.WithAttributes(semconv.DBSystemKey.String("prometheus"), attribute.String("db.statement", query), attribute.String("kube_state_graph.query_name", name)))`. Pass the legs' contexts through to the prom client so the outbound HTTP span chains correctly. (Implemented at the `promql.Client.Instant` boundary so every errgroup leg gets the span automatically.)
- [x] 29.E.4 In `internal/api/handlers.go`, wrap the `graph.Project(g, scope)` call in `tracer.Start(ctx, "kube-state-graph.project")` and the serialiser call in `tracer.Start(ctx, "kube-state-graph.serialise", trace.WithAttributes(attribute.String("kube_state_graph.serialiser", "cytoscape" | "nodegraph")))`. Emit post-projection / post-serialise node + edge counts.
- [x] 29.E.5 Update `internal/api/errors.go::mapBuildError` so the same place that picks the HTTP status + `reason` body string also records the error on the span (helper `recordBuildError(ctx, err, reason)`).

### 29.F Integration and validation

- [ ] 29.F.1 Add a testcontainers-based integration test `internal/integration/otlp_e2e_test.go` that starts an OTel Collector container alongside VictoriaMetrics, configures the API server with `OTEL_EXPORTER_OTLP_ENDPOINT=<container endpoint>`, makes a `/v1/graph` request, and asserts the collector received: (a) one `GET /v1/graph` server span; (b) one `kube-state-graph.build` child; (c) ≥ 1 `prometheus.query` grandchild with `db.system=prometheus` and a non-empty `db.statement`; (d) the corresponding log records carry matching `trace_id` / `span_id`. **Deferred** — the existing `internal/integration/` testcontainers suite is gated by Docker bridge-network availability which is not present in the current dev sandbox; same blocker as `TestGraphSuite`. Will land in a follow-up change once CI exposes Docker.
- [x] 29.F.2 Negative integration test: with no endpoint env var set, run a `/v1/graph` request and assert no socket connection is opened to any port (or simpler: assert `telemetry.Init` returns `enabled=false` and the in-memory exporter remains empty). (Covered by `TestInit_DisabledByDefault` in `internal/telemetry/telemetry_test.go`: clears all OTel env vars, calls Init, asserts `enabled=false` and that the global TracerProvider/LoggerProvider are no-op.)
- [x] 29.F.3 Property/contract test: assert the response body for `/v1/edge-types` (and by extension `/v1/graph`) is byte-identical when tracing is enabled vs disabled — resource attributes must not leak into the response body. (Implemented as `TestTracing_BodyStableAcrossTracingState`.)
- [x] 29.F.4 Update `local/kind/manifests/30-api-server.yaml` to add commented-out `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_SERVICE_NAME` env entries showing how to point the rig at a sidecar Alloy. Do not enable by default. (Implemented as enabled-by-default in the rig: env points kube-state-graph at the in-cluster Alloy, Alloy fans traces to a new Tempo Deployment for Grafana exploration; `28-tempo.yaml` added; Alloy now also accepts logs and runs them through `otelcol.exporter.debug`.)
- [x] 29.F.5 Update `docs/operations.md` with an "OpenTelemetry" section listing the env vars, the span topology, the expected log fields, and the secret-redaction guarantee. Mirror in `docs/operations.zh-tw.md`. (No zh-tw mirror exists in this repo; English doc updated.)
- [x] 29.F.6 Update `docs/api.md` to mention that responses are unaffected by tracing and that `traceparent` is honoured / propagated.
- [x] 29.F.7 Update `CLAUDE.md` "Load-bearing design rules" with a bullet: "Tracing/logging exports are config'd by OTel env vars only (no `--otlp-*` flags), default no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset, and SHALL NOT alter response bodies."
- [x] 29.F.8 Run `openspec validate "add-k8s-pod-graph-api"`, `make test`, `make vet`, `make lint`, `make check-docs`. Confirm `govulncheck` clean against the new OTel deps. (`openspec validate` passes; `go vet ./...` clean; `go test ./... -race` excluding the Docker-dependent integration package: 122 passed; `golangci-lint run` reports only pre-existing trailing-whitespace nits unrelated to this change. `govulncheck` and `make check-docs` deferred to a follow-up shell that has those tools wired.)

### 29.G Graceful shutdown wiring

- [x] 29.G.1 In `cmd/kube-state-graph/main.go` shutdown sequence, after `http.Server.Shutdown(ctx)` returns, call `telemetry.Shutdown(ctx)` (the closure from 29.B.6) using the same context with the existing `--shutdown-grace-period` deadline.
- [x] 29.G.2 If `telemetry.Shutdown` returns an error, log it via the local stderr handler (NOT through the slog OTLP bridge — the bridge is being torn down) and exit with status 1.
- [x] 29.G.3 Component test: simulate a SIGTERM with a working in-memory collector → buffered spans flushed within grace period; with a blackhole collector → process exits with status 1 within the grace period (no extension). (Implemented in-process via `internal/telemetry/shutdown_test.go`: `TestShutdown_FlushesPendingSpans` uses a custom `recordingExporter` plus a 1 h `WithBatchTimeout` so the batcher cannot auto-flush; asserts `Shutdown` drains queued spans before returning. `TestShutdown_NoopWhenDisabled` covers the no-op path; `TestShutdown_ContextDeadlineRespected` proves an expired context does not block the call. Subprocess-level SIGTERM exit-code coverage left for a future shell-level integration test.)

## 30. Configurable upstream metric-name prefix

Per design D26 and the "Configurable upstream metric-name prefix" requirement in `specs/cluster-topology-source/spec.md`. Adds a single additive prefix knob so deployments using a fork of kube-state-metrics or a custom exporter that re-publishes KSM-shaped series under an organisational prefix (e.g. `o11y_kube_pod_info`) can be supported without forking the API server. The prefix is empty by default — bit-identical behaviour to today.

### 30.A Config surface

- [ ] 30.A.1 Add `MetricPrefix string` to `config.Config` in `internal/config/config.go`. Default `""` in `Defaults()`.
- [ ] 30.A.2 Bind env var `KSG_METRIC_PREFIX` in `applyEnv` (string getter).
- [ ] 30.A.3 Bind flag `--metric-prefix` in `Parse` with help text describing the additive semantics and example `o11y_`.
- [ ] 30.A.4 Add validation in `Config.Validate`: when non-empty, must match `^[a-zA-Z_:][a-zA-Z0-9_:]*$` (Prometheus metric-name charset). Error message includes `metric-prefix` so operators can grep.
- [ ] 30.A.5 Unit tests in `internal/config/config_test.go`: default empty, env wins over default, flag wins over env, valid prefix accepted (`o11y_`, `acme_`), invalid prefix rejected (`o11y-bad!`, `1starts_with_digit`).

### 30.B Renderer in `internal/promql`

- [ ] 30.B.1 Add `Renderer struct { Prefix string }` and method `func (r Renderer) Render(q Query, window time.Duration) string` in `internal/promql/queries.go`. Apply `r.Prefix` to: `QPodInfo`, `QNodeInfo`, `QNodeAddresses`, `QPVCBindings`, `QNodeLabels`, `QClusterDiscovery`. Do NOT apply to `QServiceGraphTotal` or `QUpProbe`.
- [ ] 30.B.2 Keep the existing package-level `Render(q, window)` as a thin shim that delegates to `Renderer{}.Render(q, window)` (zero-prefix back-compat for tests that don't care).
- [ ] 30.B.3 Update `queries_test.go`: table-driven assertions for both empty and non-empty prefix across all six prefixed queries, plus a negative assertion that `Render(QServiceGraphTotal)` and `Render(QUpProbe)` are unaffected by prefix.
- [ ] 30.B.4 The `Query` string constants stay as the bare names (used as the `query` label on self-metrics + the `kube_state_graph.query_name` span attribute). Prefix affects only the rendered PromQL string, not the metric/span dimension.

### 30.C Thread through Builder + Server

- [ ] 30.C.1 Add `r promql.Renderer` field to `build.Builder`. Construct it from `cfg.MetricPrefix` inside `build.New` so callers do not need to change their argument lists.
- [ ] 30.C.2 Replace every `promql.Render(promql.Q...)` callsite in `internal/build/topology.go`, `internal/build/servicegraph.go`, and `internal/build/build.go` (`upProbe`) with `b.r.Render(...)` — except `QServiceGraphTotal` and `QUpProbe` may continue to use the package-level `Render` since they are not prefixed anyway. For consistency, route them through `b.r.Render` too (the renderer is a no-op on those queries).
- [ ] 30.C.3 Add `r promql.Renderer` field to `api.Server`. Construct from `cfg.MetricPrefix` inside `api.New`. Replace `promql.Render(promql.QClusterDiscovery, ...)` in `handlers.go` and `promql.Render(promql.QUpProbe, ...)` in `handleReadyz` with `s.r.Render(...)`.
- [ ] 30.C.4 `ReadTopology` and `ReadServiceGraph` currently take `q promql.Querier` and `window` directly — pass the renderer in too (extra parameter) rather than re-reading from a `Builder` receiver, since these are package-level functions. Plumb through from `Builder.Build`.

### 30.D Wire main.go

- [ ] 30.D.1 No change required to `cmd/kube-state-graph/main.go` aside from logging the prefix in the startup banner (`logger.Info("starting kube-state-graph", ..., "metric_prefix", cfg.MetricPrefix)`) for operator visibility.

### 30.E Integration + golden coverage

- [ ] 30.E.1 In `internal/integration/graph_e2e_test.go`, add ONE new subtest that ingests a `kube_pod_info` series under the name `o11y_kube_pod_info` (plus the matching `o11y_kube_node_info` etc.), starts the API with `cfg.MetricPrefix = "o11y_"`, and asserts the resulting graph contains the expected pod node. Other existing subtests stay on the default empty prefix.
- [ ] 30.E.2 Verify `internal/api/server_happy_test.go` substring-matchers (`"last_over_time(kube_pod_info"`, `"last_over_time(kube_node_info"`) keep working when the default prefix is empty. No test code change needed — the substring is still present in the rendered query when prefix is `""`.
- [ ] 30.E.3 Add one focused `internal/api/handlers_test.go` (or sibling) case that constructs a Server with `cfg.MetricPrefix = "o11y_"`, calls `/v1/clusters`, and asserts the mock querier saw a query string containing `o11y_kube_node_info`. Uses `newMockQuerier` with a fixture keyed on `"o11y_kube_node_info"`.
- [ ] 30.E.4 Golden tests: golden bodies are unaffected because the prefix lives entirely upstream of the response shape — only confirm `make test` still passes with no `-update`.

### 30.F Docs + operator notes

- [ ] 30.F.1 Add a `KSG_METRIC_PREFIX` / `--metric-prefix` entry to the env-var / flag table in `README.md` (and `README.zh-tw.md` if present) under the upstream configuration section.
- [ ] 30.F.2 Add an "Exporter compatibility contract" subsection to `docs/operations.md` that lists: (a) supported metric-name suffix list (the six `kube_*` series + `cluster_discovery`); (b) the required label set per metric (mirror the spec); (c) the additive nature of the prefix; (d) the explicit non-coverage of `traces_service_graph_request_total` and `up{}`.
- [ ] 30.F.3 Add a bullet to the "Load-bearing design rules" section of `CLAUDE.md`: "Metric-name prefix is an additive `KSG_METRIC_PREFIX` knob applied to KSM-shaped series only; the metric-name suffix and label-name set are a fixed contract (see D26)."
- [ ] 30.F.4 Document in `docs/operations.md` that the prefix is NOT applied to `traces_service_graph_request_total` (Alloy/Tempo family) or `up{}` (Prometheus-native), and that a separate knob can ship in a follow-up if a deployment needs it.

### 30.G Validation

- [ ] 30.G.1 Run `openspec validate "add-k8s-pod-graph-api"`.
- [ ] 30.G.2 Run `make test`, `make vet`, `make lint`.
- [ ] 30.G.3 Verify `make verify-mocks` clean (no interface signature changes — `promql.Querier` is unchanged).

## 31. Missing pod-UID human-label fallback (pod-service-graph — modified)

### 31.A Reader behaviour

- [ ] 31.A.1 In `internal/build/servicegraph.go::resolveClientEndpoint`, after the `KSG_EXTERNAL_NAME_PATTERN` branch and before the existing `if podUID == "" { return "", false }` short-circuit, add a new branch: when `podUID == ""` AND `humanLabel != ""`, insert an `ExternalNode` (id=`graph.ExternalID(humanLabel)`, name=`humanLabel`, labels=`map[string]string{}`) into `externals` (idempotent — only insert when the id is not already present) and return `(extID, false)`. Reduce the existing `return "", false` to the case where both `podUID` and `humanLabel` are empty.
- [ ] 31.A.2 Mirror the same change in `resolveServerEndpoint`: empty `podUID` + non-empty `humanLabel` → insert external node, return `extID`. Keep the empty-both case dropping (return `""`).
- [ ] 31.A.3 Confirm the resulting rule order in both functions is exactly: (a) `KSG_EXTERNAL_NAME_PATTERN` substring match → external (existing); (b) UID-resolution / synth-pod fallback (existing, now explicitly gated on non-empty UID); (c) missing-UID human-label fallback (new branch); (d) drop. The pattern rule continues to win, so a series whose label matches the pattern AND has empty UID still produces an external node carrying `labels.pattern`.
- [ ] 31.A.4 Confirm the existing edge-cluster-label aggregation in `parseServiceGraph` (`if agg.srcIsPod { labels["cluster"] = agg.srcCluster }`) still produces the desired output: the missing-UID fallback returns `srcIsPod=false`, so the edge omits `labels.cluster` automatically. No code change needed here — verify with the new tests in 31.B.1.
- [ ] 31.A.5 Confirm the inserted `ExternalNode` from the fallback path is included in `ServiceGraphResult.ExternalNodes` (existing iteration over `externals` map at the bottom of `parseServiceGraph`). No code change needed; the existing map is the single source of truth.

### 31.B Unit tests

- [ ] 31.B.1 Add unit tests in `internal/build/servicegraph_test.go`:
  - `TestParseServiceGraph_MissingClientUID_PromotesToExternal`: empty `client_k8s_pod_uid`, non-empty `client="admin"`, server-side pod resolvable → edge `source="external/admin"`, source `ExternalNode` in `ExternalNodes`, edge `labels` map contains no `cluster` key.
  - `TestParseServiceGraph_MissingServerUID_PromotesToExternal`: empty `server_k8s_pod_uid`, non-empty `server="payments"`, client-side pod resolvable, `cluster="cluster-alpha"` → edge `target="external/payments"`, target `ExternalNode` in `ExternalNodes`, edge `labels.cluster="cluster-alpha"`.
  - `TestParseServiceGraph_BothUIDsMissing_BothLabelsPresent`: both UIDs empty, both human labels present → external→external edge, edge `labels` map contains no `cluster` key, both nodes in `ExternalNodes`.
  - `TestParseServiceGraph_UIDAndLabelBothEmpty_EdgeDropped`: client UID + client human label both empty → no edge, no node. Server-side variant in a sibling test.
  - `TestParseServiceGraph_PatternWinsOverMissingUIDFallback`: `KSG_EXTERNAL_NAME_PATTERN="://"`, `client="http://api.example.com"`, `client_k8s_pod_uid=""` → external node carries `labels.pattern: "://"` (proves the pattern branch fired first).
  - `TestParseServiceGraph_DedupeBetweenPatternAndFallback`: two series, one whose label matches the pattern with non-empty UID, another with the SAME human label but empty UID, both produce the same `external/<label>` id → single node in `ExternalNodes` (existing externals-map dedupe carries the new path).
- [ ] 31.B.2 Strengthen the property test in `internal/graph/property_test.go` (or add a sibling) that for randomised service-graph fixtures, every emitted edge satisfies: `srcID != ""` AND `tgtID != ""`. With the fallback in place, missing-UID series no longer produce empty IDs, so the invariant should hold for any series whose `(client, server)` pair has at least one non-empty side per endpoint.

### 31.C Component & golden tests

- [ ] 31.C.1 Add a component test in `internal/api/` that injects a service-graph fixture with a UID-less client via `newMockQuerier(t, fixtureSet{...})` and asserts the `/v1/graph` response contains an `external` node with the human label as `name`, and the expected `pod-calls-pod` edge with no edge `labels.cluster` key.
- [ ] 31.C.2 Add ONE new golden fixture covering the fallback shape under `internal/api/testdata/golden/` (do not mutate existing goldens to avoid mass churn). Run `go test ./internal/api/ -update -run Golden_MissingUIDFallback` (or similar single-name selector) once the fallback is implemented and the fixture stable.
- [ ] 31.C.3 Confirm existing golden fixtures are byte-identical (no diff) after the change — the fallback only fires for empty UIDs which the current fixtures never produce, so no existing golden should move.

### 31.D Spec, design, and docs

- [ ] 31.D.1 Confirm `openspec/changes/add-k8s-pod-graph-api/specs/pod-service-graph/spec.md` contains the new "Missing pod-UID human-label fallback" requirement and the updated trigger language on "Synthesised pod node fallback" (i.e., "non-empty pod-UID endpoint").
- [ ] 31.D.2 Confirm `openspec/changes/add-k8s-pod-graph-api/design.md` contains `D27. Missing pod-UID human-label fallback` and the corresponding risk bullet in "Risks / Trade-offs".
- [ ] 31.D.3 Update `CLAUDE.md` "Load-bearing design rules": amend the existing "External-endpoint substitution rule" bullet (or add a sibling bullet) to note that missing `client_k8s_pod_uid` or `server_k8s_pod_uid` now promotes to `external/<label>` rather than dropping the edge; ID has no cluster prefix; edge `labels.cluster` rules unchanged.
- [ ] 31.D.4 Update `docs/operations.md` "Exporter compatibility contract" to clarify that `client_k8s_pod_uid` and `server_k8s_pod_uid` are RECOMMENDED but no longer hard-required for an edge to appear — missing UID surfaces the dependency as an `external/<label>` node. Mirror in `docs/operations.zh-tw.md` if it exists.
- [ ] 31.D.5 Update the OpenAPI / Scalar-rendered description for `/v1/graph` if the response-shape documentation explicitly states "every node is a pod resolved via pod UID" — replace with the per-endpoint resolution order from D27.

### 31.E Validation

- [ ] 31.E.1 Run `openspec validate "add-k8s-pod-graph-api"`.
- [ ] 31.E.2 Run `make test`, `make vet`, `make lint`.
- [ ] 31.E.3 Run `make verify-mocks` (no interface signatures changed — should be clean).
- [ ] 31.E.4 Sanity-check the manual rig: with Beyla emitting at least one span whose `k8s.pod.uid` resource attr is intentionally stripped (or by ingesting a hand-crafted `traces_service_graph_request_total` series via the integration-test ingest helper), confirm `/v1/graph` contains an `external/<label>` node for that endpoint.

## 32. Typed `ipaddress` node attribute + remove HTTP cache validators (capability: graph-api — modified, cluster-topology-source — modified)

The response shape is changed so that every pod and K8s-node entry carries its observed IP address(es) on a typed top-level `ipaddress` attribute (`string[]`, `omitempty`) instead of inside the `labels` bag, and the legacy `pod_ip` / `host_ip` / `external_ip` label keys are removed. At the same time, the unused HTTP cache-validator path (`ETag` + `If-None-Match` + `304 Not Modified`) is removed from the codebase — v1 ships no in-process result cache and no validator. Section 32 captures the surgical changes; see `design.md` D28 and `specs/graph-api/spec.md` for the new contract.

### 32.A Code

- [x] 32.A.1 `internal/graph/node.go`: add `IPAddress() []string` to the sealed `GraphNode` interface. Add `IPAddressValue []string` to `PodNode` and `K8sNode`; have `PVCNode` / `ExternalNode` return `nil`.
- [x] 32.A.2 `internal/build/topology.go`: stop writing `pod_ip` / `host_ip` / `external_ip` into the labels map. Track `pod_ip` on `podObs` and surface it as `PodNode.IPAddressValue` (newest non-empty wins across same-UID merge). Surface `kube_node_status_addresses{type="ExternalIP"}` on `K8sNode.IPAddressValue`.
- [x] 32.A.3 `internal/api/serialise.go`: add `IPAddress []string` to `cytoscapeNodeData` with `json:"ipaddress,omitempty"`. Populate from `n.IPAddress()`.
- [x] 32.A.4 `internal/api/handlers.go` + `internal/api/docs.go`: remove all `ETag` / `If-None-Match` / `304` logic and the `sha256ETag` / `sha256Quoted` helpers. Drop `crypto/sha256` / `encoding/hex` imports. Strip ETag mentions from swag annotations.
- [x] 32.A.5 `internal/graph/registry.go`: delete `EdgeTypesETag` and its imports.
- [x] 32.A.6 `internal/api/tracing_middleware.go`: remove the `kube_state_graph.etag` span attribute.
- [x] 32.A.7 `internal/observability/metrics.go`: drop the "ETag computation" mention from the serialise-duration help text.

### 32.B Tests

- [x] 32.B.1 `internal/build/topology_test.go`: replace `labels.pod_ip` / `labels.host_ip` / `labels.external_ip` assertions with `IPAddress` checks; assert those label keys are absent. Drop `host_ip` from expectations.
- [x] 32.B.2 `internal/api/server_test.go`: drop `TestEdgeTypesEndpoint_IfNoneMatch304` and remove the `ETag` header assertion from `TestEdgeTypesEndpoint_StaticCatalogue`.
- [x] 32.B.3 `internal/api/server_happy_test.go`: drop `TestGraphEndpoint_HappyPath_IfNoneMatch304`. Extend `TestGraphEndpoint_HappyPath` to assert `data.ipaddress` is populated for the pod entry and that the `pod_ip` / `host_ip` / `external_ip` label keys are absent.
- [x] 32.B.4 `internal/api/docs_test.go`: drop `TestOpenAPIJSONEndpoint_IfNoneMatch304`. Replace the `ETag`-header assertion in `TestOpenAPIYAMLEndpoint` with a body / content-type / cache-control assertion. Add a sibling `TestOpenAPIJSONEndpoint` doing the same for JSON.
- [x] 32.B.5 `internal/api/tracing_test.go`: rename `TestTracing_ETagStableAcrossTracingState` → `TestTracing_BodyStableAcrossTracingState`; assert byte-identical bodies rather than ETag headers. Drop the `kube_state_graph.etag` attribute check in `TestTracing_EdgeTypesEmitsServerSpan`.
- [x] 32.B.6 `internal/integration/graph_e2e_test.go`: delete `TestETagRoundTrip304` and `TestRepeatedRequestsReturnSameETag`.

### 32.C Spec, design, and docs

- [x] 32.C.1 `openspec/changes/add-k8s-pod-graph-api/proposal.md` + `proposal.zh-tw.md`: remove ETag mention; add bullet for the new `ipaddress` attribute + label removal.
- [x] 32.C.2 `openspec/changes/add-k8s-pod-graph-api/design.md` + `design.zh-tw.md`: rewrite D6 ("Response shape and deterministic body; no in-process result cache") to drop ETag entirely. Add D28 ("Top-level `ipaddress` attribute on pod and K8s-node entries"). Sweep all remaining `ETag` / `If-None-Match` mentions from other D entries.
- [x] 32.C.3 `openspec/changes/add-k8s-pod-graph-api/specs/graph-api/spec.md`: delete the "Conditional GET via response validator (ETag)" requirement and its scenarios. Add a "Deterministic response body" requirement and a "Node `ipaddress` attribute" requirement with scenarios for pod / node / pvc-or-external / absent-source cases. Drop the OpenAPI ETag scenario.
- [x] 32.C.4 `openspec/changes/add-k8s-pod-graph-api/specs/cluster-topology-source/spec.md`: rewrite the canonical-fields requirement so `pod_ip` / `host_ip` / `external_ip` no longer appear in labels; pod IP lives on `ipaddress`, node IP lives on the K8s-node entry's `ipaddress`, `host_ip` is dropped from the pod entry entirely.
- [x] 32.C.5 `openspec/changes/add-k8s-pod-graph-api/specs/otlp-observability/spec.md`: drop the `kube_state_graph.etag` span-attribute requirement and the matching scenario assertion.
- [x] 32.C.6 `internal/api/static/openapi/openapi.yaml` + `openapi.json` + `docs/swagger.yaml` + `docs/swagger.json` + `docs/docs.go`: regenerated via `make docs`. ETag/304 mentions cleared; `ipaddress` field added to `internal_api.cytoscapeNodeData`.
- [x] 32.C.7 `docs/operations.md`: remove ETag-based amortisation framing in the capacity-planning section and drop `host_ip` from the exporter-compatibility table. Drop the `kube_state_graph.etag` span-attribute row.
- [x] 32.C.8 `CLAUDE.md`: remove the "ETag determinism is load-bearing" bullet and the ETag mention in the request-lifecycle diagram. Add a new bullet describing the `ipaddress` attribute contract. Add `IPAddress()` to the sealed-interface method list.

### 32.D Validation

- [x] 32.D.1 `go build ./...`, `go vet ./...`, `go test ./... -count=1 -race -short`: all green.
- [x] 32.D.2 `make docs` regenerates `internal/api/static/openapi/*` + `docs/swagger.*` cleanly with no ETag / 304 references remaining and the new `ipaddress` schema entry visible.
- [ ] 32.D.3 `openspec validate "add-k8s-pod-graph-api"` passes after the spec rewrites.
- [ ] 32.D.4 Manually launch the local rig + Scalar UI and confirm `/v1/graph` returns nodes carrying `ipaddress` (pod + node entries) and that no `ETag` header is set on graph routes.

## 33. Split `others` (pattern) from `external` (missing-UID fallback) (capability: graph-api — modified, pod-service-graph — modified)

Pattern-matched non-pod endpoints (operator-declared via `KSG_OTHERS_NAME_PATTERN`) and missing-UID inferred non-pod endpoints (producer regression fallback per D27) now live in disjoint namespaces with disjoint dedupe maps. The pattern rule emits `type="others"` with `id="others/<label>"`; the missing-UID fallback continues to emit `type="external"` with `id="external/<label>"`. The env var / flag / config field are renamed from `KSG_EXTERNAL_NAME_PATTERN` / `--external-name-pattern` / `ExternalNamePattern` to `KSG_OTHERS_NAME_PATTERN` / `--others-name-pattern` / `OthersNamePattern`. Rationale: the producer-regression signal (sudden growth of `type=external`) was diluted by steady-state declared endpoints; splitting keeps the alarm clean. See design.md D18 + D27.

> **Superseded in part by §34 (D29).** The `OthersNode` type, the `others/<label>` namespace, and the disjoint others-vs-external split established here are RETAINED. Only the configurable knob (33.D — `KSG_OTHERS_NAME_PATTERN` / `--others-name-pattern` / `Config.OthersNamePattern`) is later removed by 34.D: `"://"` detection becomes hardcoded and unresolved others nodes drop `labels.pattern` (→ `labels={}`). Do not invest in 33.D's knob work beyond what is already in the tree; execute §34 for the final state.

### 33.A Graph types

- [ ] 33.A.1 `internal/graph/node.go`: add `NodeTypeOthers NodeType = "others"`. Add `OthersNode` struct mirroring `ExternalNode` (fields `IDValue`, `NameValue`, `LabelsValue`). Implement `ID()`, `Name()`, `Type()` (returns `NodeTypeOthers`), `Labels()`, `IPAddress()` (returns `nil`), `isGraphNode()`. Add `OthersID(value string) string { return "others/" + value }`.
- [ ] 33.A.2 Update the `ExternalNode` doc-comment to clarify it represents only the missing-UID human-label fallback path (D27); the pattern-matched path now produces `OthersNode`.

### 33.B Builder rewiring

- [ ] 33.B.1 `internal/build/servicegraph.go`: add `OthersNodes []*graph.OthersNode` to `ServiceGraphResult`.
- [ ] 33.B.2 Rename the `externalPattern` parameter on `ReadServiceGraph`, `parseServiceGraph`, `resolveClientEndpoint`, `resolveServerEndpoint` to `othersPattern`.
- [ ] 33.B.3 In `parseServiceGraph`, allocate a second dedupe map: `others := map[string]*graph.OthersNode{}`. Pass it alongside `externals` into both resolver functions.
- [ ] 33.B.4 In both `resolveClientEndpoint` and `resolveServerEndpoint`, change the pattern branch to insert into `others` with `id=graph.OthersID(humanLabel)`, `type="others"`, `labels={"pattern": othersPattern}`. The missing-UID fallback branch keeps inserting into `externals` unchanged.
- [ ] 33.B.5 At the bottom of `parseServiceGraph`, populate `out.OthersNodes` from the `others` map in addition to `out.ExternalNodes` from `externals`.

### 33.C Builder.assemble + main pipeline

- [ ] 33.C.1 `internal/build/build.go::assemble`: include `sg.OthersNodes` in the total / iteration so others nodes flow into the assembled `[]graph.GraphNode`. Update the `total` size hint to account for them.
- [ ] 33.C.2 `internal/build/build.go::Build`: pass `b.cfg.OthersNamePattern` to `ReadServiceGraph` (renamed from `b.cfg.ExternalNamePattern`).

### 33.D Config + flag rename

- [ ] 33.D.1 `internal/config/config.go`: rename `Config.ExternalNamePattern` → `Config.OthersNamePattern`. Update `Defaults()` field name. Update `Parse` to bind `--others-name-pattern` (replacing `--external-name-pattern`); update help text to reference others nodes. Update `applyEnv` to read `KSG_OTHERS_NAME_PATTERN` (replacing `KSG_EXTERNAL_NAME_PATTERN`).
- [ ] 33.D.2 `cmd/kube-state-graph/main.go`: rename the `external_name_pattern_set` slog field to `others_name_pattern_set` and read `cfg.OthersNamePattern`.

### 33.E Tests

- [ ] 33.E.1 `internal/config/config_test.go`: replace `KSG_EXTERNAL_NAME_PATTERN` with `KSG_OTHERS_NAME_PATTERN`; rename `cfg.ExternalNamePattern` → `cfg.OthersNamePattern`; update assertion message.
- [ ] 33.E.2 `internal/build/servicegraph_test.go`: rewrite pattern-rule tests to assert `id="others/<value>"`, `type="others"`, and that nodes land in `ServiceGraphResult.OthersNodes`. Rewrite the "pattern + fallback dedupe" test (was `TestParseServiceGraph_DedupeBetweenPatternAndFallback`) to assert that the two paths now produce two distinct nodes (`others/<label>` and `external/<label>`).
- [ ] 33.E.3 `internal/integration/graph_e2e_test.go`: rename `cfg.ExternalNamePattern = "://"` → `cfg.OthersNamePattern = "://"`. Update any test that asserts on `type="external"` for a pattern-matched endpoint to expect `type="others"` and `id="others/<label>"`. Keep missing-UID-fallback tests on `type="external"`.
- [ ] 33.E.4 `internal/graph/registry.go`: update `EdgeTypePodCallsPod`'s description to reference `KSG_OTHERS_NAME_PATTERN` (and mention both `others` + `external` endpoint kinds).
- [ ] 33.E.5 Golden tests: regenerate any fixtures under `internal/api/testdata/golden/` whose payload includes pattern-matched endpoints (`type="external"` with `labels.pattern`) — those now serialise as `type="others"` with `id="others/<value>"`. Run `go test ./internal/api/ -update -run Golden` after the code change.

### 33.F Docs + manifests

- [ ] 33.F.1 Rename `docs/external-substitution.md` → `docs/others-substitution.md`; rewrite the body for the new node type and env var. Add a short section pointing to D27 for the missing-UID fallback (now a separate `type=external` node).
- [ ] 33.F.2 `README.md` + `README.zh-tw.md`: update the env / flag table entries (`KSG_EXTERNAL_NAME_PATTERN` → `KSG_OTHERS_NAME_PATTERN`, `--external-name-pattern` → `--others-name-pattern`); update prose mentioning `external` nodes produced by the pattern to mention `others` instead.
- [ ] 33.F.3 `docs/operations.md`: update the "Exporter compatibility contract" + any other section that mentions `KSG_EXTERNAL_NAME_PATTERN` or the `external`-via-pattern shape.
- [ ] 33.F.4 `docs/api.md`: add `others` to the node `type` enum description; clarify the disjoint dedupe.
- [ ] 33.F.5 `local/kind/manifests/30-api-server.yaml`: rename the env var.
- [ ] 33.F.6 `charts/kube-state-graph/templates/deployment.yaml` + `charts/kube-state-graph/values.yaml`: rename the env var + the values key (`externalNamePattern` → `othersNamePattern` if such a key exists in `values.yaml`).
- [ ] 33.F.7 `CLAUDE.md`: rewrite the "External-endpoint substitution rule" bullet to describe the split (pattern → others, fallback → external); update the resolution-order numbered list.

### 33.G Swag / OpenAPI

- [ ] 33.G.1 Update any swag `@Description` or `@Param` on `/v1/graph` / `/v1/graph/nodegraph` mentioning `KSG_EXTERNAL_NAME_PATTERN` or the `external` enum value for pattern-matched nodes. Run `make docs` and commit regenerated `docs/swagger.{json,yaml,go}` + `internal/api/static/openapi/*`.

### 33.H Validation

- [ ] 33.H.1 `openspec validate "add-k8s-pod-graph-api"`.
- [ ] 33.H.2 `make build`, `make vet`, `make test`, `make lint`.
- [ ] 33.H.3 `make verify-mocks` — no interface signatures changed; should stay clean.

## 34. Connection-string endpoint resolution + remove KSG_OTHERS_NAME_PATTERN (capability: pod-service-graph, cluster-topology-source, graph-api)

Per design D29. The configurable substring-match knob (`KSG_OTHERS_NAME_PATTERN` / `--others-name-pattern` / `Config.OthersNamePattern`) is removed entirely (pre-GA, no backward-compat burden). In its place the service-graph reader hardcodes `"://"` detection against the `client` / `server` label values and runs a new **connection-string resolution** stage (Stage 0) when an endpoint's pod UID is empty and its label contains `"://"`. The label is parsed as a URL; the host is matched against k8s `.svc` DNS grammar and resolved against new topology indexes (`ServicesByNameNS`, `EndpointsByService`, `PodsByNameNS`) built from `kube_service_info`, `kube_endpointslice_endpoints`, and `kube_endpointslice_labels`. A 2-label service-relative host resolves to a new `type="service"` node (materialising deduped `service-selects-pod` edges to backing pods on demand); a 3-label headless host resolves to the specific backing pod (a real pod, `srcIsPod=true`); a miss falls back to an `others/<label>` node with `labels={}` (the `pattern` key is dropped along with the knob). The per-endpoint resolution order becomes: (1) connection-string resolution; (2) pod-UID resolution / synth-pod fallback (non-empty UID only); (3) missing-UID human-label fallback → `external/<label>` (non-URL labels only); (4) drop. `"://"` labels never reach the external fallback; `external` is now reserved for non-URL missing-UID producer-regression signals. See design.md D29 + D27 + D18.

### 34.A Graph types

- [x] 34.A.1 `internal/graph/node.go`: add `NodeTypeService NodeType = "service"`. Add `ServiceNode` struct (fields `IDValue`, `NameValue`, `LabelsValue map[string]string`, `IPAddressValue []string`). Implement `ID()`, `Name()`, `Type()` (returns `NodeTypeService`), `Labels()`, `IPAddress()` (returns `IPAddressValue` — `[cluster_ip]` when known, `nil` when the service is headless `cluster_ip="None"` or absent), `isGraphNode()`.
- [x] 34.A.2 `internal/graph/node.go`: add `ServiceID(cluster, namespace, service string) string { return cluster + "/" + namespace + "/" + service }` (cluster-scoped, mirrors `PVCNode` keying).
- [x] 34.A.3 `internal/graph/registry.go`: add `EdgeTypeServiceSelectsPod` (`type="service-selects-pod"`, directed `service → pod`, `may_cross_cluster=false` / intra-cluster, `source_type=["service"]`, `target_type=["pod"]`, no required labels — optional `namespace`). Register it in `graph.EdgeTypes` so `/v1/edge-types` lists it.
- [x] 34.A.4 `internal/graph/registry.go`: extend `EdgeTypePodCallsPod`'s `source_type` and `target_type` to ALSO include `"service"` (a pod can call a service node). The lists already include `"pod"`, `"others"`, `"external"` — append `"service"` to both.

### 34.B Topology reader (capability: cluster-topology-source)

- [x] 34.B.1 `internal/promql/queries.go`: add `Query` constants `QServiceInfo` (`kube_service_info`), `QEndpointSliceEndpoints` (`kube_endpointslice_endpoints`), `QEndpointSliceLabels` (`kube_endpointslice_labels`). These are KSM-shaped — they MUST be prefix-aware via `promql.Renderer{Prefix}` (D26 scope extension, see 34.D / 34.G). Keep the bare `Query` constants unprefixed so the `query` / `query_name` self-metric + span dimensions stay stable.
- [x] 34.B.2 `internal/build/topology.go`: extend the `ReadTopology` errgroup fan-out with the three new prefixed queries (rendered via the threaded `promql.Renderer`). Tolerate empty / absent series — `nil` or empty Vector MUST yield empty indexes, never an error (older KSM without endpointslices, or KSM started without `--resources=services,endpointslices`).
- [x] 34.B.3 `internal/build/topology.go`: parse `kube_service_info{cluster, namespace, service, cluster_ip, ...}` and build index `ServicesByNameNS map[serviceKey]serviceObs` keyed by `(cluster, namespace, service)`, carrying `cluster_ip` (retain the literal `"None"` so the resolver can distinguish headless from ClusterIP).
- [x] 34.B.4 `internal/build/topology.go`: parse `kube_endpointslice_labels{cluster, namespace, endpointslice, label_kubernetes_io_service_name, ...}` to map each endpointslice to its owning service name, joined by `(cluster, namespace, endpointslice)`.
- [x] 34.B.5 `internal/build/topology.go`: parse `kube_endpointslice_endpoints{cluster, namespace, endpointslice, address, hostname, targetref_kind, targetref_name, targetref_namespace, ...}`; resolve each endpoint's backing pod by joining `(cluster, targetref_namespace, targetref_name)` against the loaded topology pods (the global pod index built from `kube_pod_info`); build index `EndpointsByService map[serviceKey][]endpointObs` keyed by `(cluster, namespace, service)` (service name recovered via the slice→service map from 34.B.4), each entry carrying the resolved `*PodNode` plus the endpoint `hostname` label (for headless pod-hostname matching).
- [x] 34.B.6 `internal/build/topology.go`: build index `PodsByNameNS map[podNameKey]*PodNode` keyed by `(cluster, namespace, pod-name)` from the loaded `kube_pod_info` pods — used as the StatefulSet-convention headless fallback when no endpointslice `hostname` matches (KSM does NOT expose `spec.hostname`).
- [x] 34.B.7 `internal/build/topology.go`: surface the three indexes on the `Topology` struct (e.g. `Topology.ServicesByNameNS`, `Topology.EndpointsByService`, `Topology.PodsByNameNS`) alongside the existing `Topology.PodsByUID`. Build INDEXES ONLY — do NOT materialise `ServiceNode`s or `service-selects-pod` edges here; those are emitted on demand by the service-graph reader (34.C) for referenced services only, to avoid graph bloat.
- [x] 34.B.8 IMPLEMENTATION-TIME VERIFY: against a live KSM (local rig), confirm the exact label names — `label_kubernetes_io_service_name` on `kube_endpointslice_labels` (slice→service join), and that `kube_endpointslice_endpoints` carries `hostname` + `targetref_kind` / `targetref_name` / `targetref_namespace`. Adjust the parser to the verified label names before finalising.

### 34.C Service-graph resolver (capability: pod-service-graph)

- [x] 34.C.1 `internal/build/servicegraph.go`: thread the new topology indexes (`ServicesByNameNS`, `EndpointsByService`, `PodsByNameNS`) into `parseServiceGraph` / `resolveClientEndpoint` / `resolveServerEndpoint` (alongside the existing `PodsByUID`). Add an on-demand, deduped service-node + `service-selects-pod`-edge collector (a map keyed by service id) shared across the parse so a service referenced by multiple edges materialises once.
- [x] 34.C.2 `internal/build/servicegraph.go`: in both `resolveClientEndpoint` and `resolveServerEndpoint`, add **Stage 0 connection-string resolution** that fires ONLY when the endpoint pod UID is EMPTY AND the label contains the substring `"://"`. Stage 0 runs BEFORE the missing-UID human-label fallback. When the UID is non-empty, skip Stage 0 entirely (normal pod-UID resolution applies — connection strings only appear with empty UIDs).
- [x] 34.C.3 Stage 0 — host parse: parse the label as a URL (`net/url`); take the host (strip scheme, userinfo, port, path/query). No parseable host → unresolvable (→ 34.C.7 others fallback).
- [x] 34.C.4 Stage 0 — `.svc` grammar: strip an optional trailing `.svc.<cluster-domain>` (e.g. `.svc.cluster.local`); also accept the shorter `<...>.svc` and bare `<a>.<b>` forms. Count the dotted labels of the service-relative part: 2 labels `<service>.<namespace>` → SERVICE-LEVEL record; 3 labels `<pod-hostname>.<service>.<namespace>` → HEADLESS POD record. Anything else → unresolvable.
- [x] 34.C.5 Stage 0 — cluster determination: use the trace-source `cluster` label (client side) for the lookup, since `.svc.cluster.local` is in-cluster DNS (target shares the caller's k8s cluster). When the trace cluster is empty (e.g. external client), attempt a UNIQUE `(namespace, service)` (or `(namespace, pod-hostname)`) match across all clusters; if not unique or absent → unresolvable.
- [x] 34.C.6 Stage 0 — SERVICE-LEVEL resolution: look up `(cluster, namespace, service)` in `ServicesByNameNS`. HIT → endpoint resolves to a `ServiceNode` (`id=graph.ServiceID(cluster,namespace,service)`, `type="service"`, `labels={cluster, namespace}`, `ipaddress=[cluster_ip]` when `cluster_ip != "None"`, omitted for headless). Materialise (on demand, deduped) one `service-selects-pod` edge from this service node to EACH backing pod in `EndpointsByService[(cluster,namespace,service)]`. MISS (service not in topology) → unresolvable (→ 34.C.7). A service endpoint is NOT a pod (`srcIsPod=false`).
- [x] 34.C.7 Stage 0 — HEADLESS POD resolution: resolve the specific backing pod. PRIMARY: scan `EndpointsByService[(cluster,namespace,service)]` for the endpoint whose `hostname` label == `<pod-hostname>` → its resolved `*PodNode` (handles arbitrary `spec.hostname`). FALLBACK when no endpointslice hostname matches: `PodsByNameNS[(cluster,namespace,pod-hostname)]` (StatefulSet convention pod-name==hostname). HIT → endpoint resolves to the REAL pod node (`id="<cluster>/<uid>"`, `srcIsPod=true` on the client side). MISS → unresolvable (→ 34.C.8).
- [x] 34.C.8 Stage 0 — UNRESOLVABLE fallback: when Stage 0 cannot resolve (no parseable host, host not a parseable k8s `.svc` name, service/pod not in topology, or ambiguous cross-cluster), fall back to an OTHERS node: `id=graph.OthersID(label)`, `name=<label>` (verbatim), `type="others"`, `labels={}` (EMPTY — no `pattern` key). This keeps truly-external URLs (`https://payments.partner.example/api`) and unknown k8s names visible. A service endpoint resolving to others is NOT a pod (`srcIsPod=false`).
- [x] 34.C.9 `internal/build/servicegraph.go`: ensure `"://"`-containing labels with empty UID ALWAYS go through Stage 0 and NEVER reach the missing-UID human-label fallback (now reserved for NON-URL labels). The fallback branch SHALL gate on `!strings.Contains(label, "://")` in addition to its existing empty-UID + non-empty-label conditions.
- [x] 34.C.10 `internal/build/servicegraph.go`: confirm the edge `labels.cluster` rule (D9) is preserved — present only when the CLIENT side resolves to a POD. A client `"://"` label that resolves (via Stage 0 headless) to a real pod now makes the edge carry `cluster` (improvement, `srcIsPod=true`); a client resolving to a service / others / external omits `cluster` (`srcIsPod=false`). Service nodes are NOT pods.
- [x] 34.C.11 `internal/build/servicegraph.go`: add `ServiceNodes []*graph.ServiceNode` and `ServiceSelectsPodEdges []graph.Edge` (or fold the edges into the existing `Edges` slice) to `ServiceGraphResult`; populate from the on-demand collector at the bottom of `parseServiceGraph`. Dedupe service nodes by id and service-selects-pod edges by the canonical edge id.
- [x] 34.C.12 `internal/build/build.go::assemble`: include `sg.ServiceNodes` in the assembled `[]graph.GraphNode` and `sg.ServiceSelectsPodEdges` in the edge set; update any `total` size hint. Service nodes + edges go through the existing `graph.SortNodes` / `SortEdges` determinism path (34.F validates byte-stability).

### 34.D Remove the knob (capability: graph-api / pod-service-graph)

- [x] 34.D.1 `internal/config/config.go`: delete `Config.OthersNamePattern` field, its `Defaults()` entry, the `--others-name-pattern` flag binding in `Parse`, and the `KSG_OTHERS_NAME_PATTERN` env binding in `applyEnv`.
- [x] 34.D.2 `internal/build/servicegraph.go`: remove the `othersPattern` parameter from `ReadServiceGraph`, `parseServiceGraph`, `resolveClientEndpoint`, `resolveServerEndpoint` and the substring-match pattern branch entirely (Stage 0 + the missing-UID fallback now cover all non-pod endpoints).
- [x] 34.D.3 `internal/build/build.go::Build`: drop the `b.cfg.OthersNamePattern` argument to `ReadServiceGraph`.
- [x] 34.D.4 `cmd/kube-state-graph/main.go`: remove the `others_name_pattern_set` startup slog field.
- [x] 34.D.5 `internal/build/servicegraph.go`: confirm `OthersNode` instances now always carry `labels={}` (the `pattern` key is gone everywhere). The disjointness between `others/<label>` (recognised `"://"` connection strings that did not resolve in-cluster) and `external/<label>` (non-URL missing-UID producer-regression signal) is preserved via the separate dedupe maps + node types.

### 34.E Rig + RBAC + chart (capability: verification-harness)

- [x] 34.E.1 `local/kind/manifests/` KSM config: add `services` and `endpointslices` to KSM `--resources`; add `kube_service_info`, `kube_endpointslice_endpoints`, `kube_endpointslice_labels` to `--metric-allowlist`.
- [x] 34.E.2 `local/kind/manifests/` KSM RBAC: extend the ClusterRole with `list` / `watch` on `services` (core) and `endpointslices` (`discovery.k8s.io`).
- [x] 34.E.3 `local/kind/manifests/30-api-server.yaml`: REMOVE the `KSG_OTHERS_NAME_PATTERN` env entry.
- [x] 34.E.4 `charts/kube-state-graph/templates/deployment.yaml`: remove the `KSG_OTHERS_NAME_PATTERN` env mapping. `charts/kube-state-graph/values.yaml`: remove the `othersNamePattern` values key.

### 34.F Tests + golden

- [x] 34.F.1 `internal/build/servicegraph_test.go`: remove the `othersPattern` parameter from all call sites. Add Stage 0 cases — service-level hit (resolves to `ServiceNode` + materialises `service-selects-pod` edges to backing pods), headless-pod hit via endpointslice `hostname` match, headless-pod hit via `PodsByNameNS` StatefulSet fallback, unresolvable `"://"` label → `others/<label>` with `labels={}`, and a truly-external URL (`https://...partner...`) → `others/<label>`. Assert client `"://"` headless-pod resolution makes the edge carry `labels.cluster`; service / others resolution omits it.
- [x] 34.F.2 `internal/build/topology_test.go`: add cases parsing `kube_service_info` / `kube_endpointslice_endpoints` / `kube_endpointslice_labels` into the three indexes; assert headless (`cluster_ip="None"`) services are retained and distinguishable; assert absence of these series yields empty indexes with no error.
- [x] 34.F.3 `internal/config/config_test.go`: remove the `KSG_OTHERS_NAME_PATTERN` env case and the `cfg.OthersNamePattern` assertions / round-trip flag entry.
- [x] 34.F.4 `internal/integration/graph_e2e_test.go`: remove the `cfg.OthersNamePattern = "://"` override; add a connection-string e2e — ingest `kube_service_info` + `kube_endpointslice_*` plus a `traces_service_graph_request_total` series whose `server` is a `mongodb://...svc.cluster.local:27017` connection string with empty `server_k8s_pod_uid`, and assert `/v1/graph` contains the resolved `service` node + `service-selects-pod` edges (or the resolved headless pod for the 3-label case), and that a `https://...partner...` URL surfaces as `type="others"` with `labels={}`.
- [x] 34.F.5 Golden: regenerate `with-others-*` fixtures under `internal/api/testdata/golden/` — pattern-matched-with-`labels.pattern` payloads now serialise as `others/<label>` with `labels={}`. Regenerate `edge-types.json` to include `service-selects-pod` and the extended `pod-calls-pod` source/target type lists. Add ONE new service-node golden (a connection-string client resolving to a `service` node + its `service-selects-pod` edges). Run `go test ./internal/api/ -update -run Golden` after the code change.
- [x] 34.F.6 `internal/graph/property_test.go`: extend the generator / invariants so `service` nodes and `service-selects-pod` edges participate — every `service-selects-pod` edge source resolves to a `service` node and target to a `pod` node; service nodes + edges are sorted deterministically (byte-stable across re-runs).

### 34.G Docs

- [x] 34.G.1 Rewrite `docs/others-substitution.md` for D29: connection-string resolution (hardcoded `"://"`, no knob), the `.svc` grammar (service-level vs headless), `service` nodes + `service-selects-pod` edges, and the `others` (unresolvable `"://"`) vs `external` (non-URL missing-UID) split. Remove all `KSG_OTHERS_NAME_PATTERN` / `--others-name-pattern` references.
- [x] 34.G.2 `README.md` + `README.zh-tw.md`: remove the `KSG_OTHERS_NAME_PATTERN` / `--others-name-pattern` rows from the env / flag tables; update prose mentioning the configurable pattern to describe the hardcoded connection-string resolution.
- [x] 34.G.3 `docs/operations.md` "Exporter compatibility contract": add `kube_service_info`, `kube_endpointslice_endpoints`, `kube_endpointslice_labels` to the supported metric list with their required label sets (`service`, `cluster_ip`, `endpointslice`, `address`, `hostname`, `targetref_kind`, `targetref_name`, `targetref_namespace`, `label_kubernetes_io_service_name`); note `KSG_METRIC_PREFIX` applies to these three and NOT to `traces_service_graph_*` or `up{}`.
- [x] 34.G.4 `CLAUDE.md`: rewrite the others-endpoint rule bullet for D29 (hardcoded `"://"`, connection-string resolution, no knob); rewrite the per-endpoint resolution-order list (Stage 0 connection-string → pod-UID/synth → missing-UID external → drop); add the `service` node type + `service-selects-pod` edge to the sealed-types / registry mentions; note the `KSG_OTHERS_NAME_PATTERN` knob removal; extend the D26 metric-prefix bullet's metric list with the three new series.

### 34.H Swag / OpenAPI

- [x] 34.H.1 Update any swag `@Description` / `@Param` on `/v1/graph` / `/v1/graph/nodegraph` mentioning `KSG_OTHERS_NAME_PATTERN` to describe the hardcoded connection-string resolution instead.
- [x] 34.H.2 Add the `service` node `type` enum value and the `service-selects-pod` edge type to the documented schemas; ensure `/v1/edge-types` annotations / examples include it. Run `make docs` and commit regenerated `docs/swagger.{json,yaml,go}` + `internal/api/static/openapi/*`.

### 34.I Validation

- [x] 34.I.1 `openspec validate "add-k8s-pod-graph-api"`.
- [x] 34.I.2 `make build`, `make vet`, `make test`, `make lint`.
- [x] 34.I.3 `make verify-mocks` — `promql.Querier` / `auth.Validator` / `clock.Clock` signatures are unchanged; if the new `Topology` indexes touch any mocked interface, regenerate and commit. Otherwise should stay clean.

## 35. Exclude virtual sentinel endpoints (`user` / `unknown`) at the query layer (capability: pod-service-graph)

Per design D30. The `servicegraph` connector emits **virtual peers** for endpoints it cannot pair to an instrumented span — an uninstrumented caller as `client="user"`, an unresolved peer as `"unknown"`. These carry no pod UID and resolve to no actionable node. Drop any `traces_service_graph_request_total` series whose `client` OR `server` label is exactly `"user"` or `"unknown"`, **at the PromQL query layer** via anchored negative matchers (`client!~"user|unknown",server!~"user|unknown"`) rather than fetch-then-drop in Go. Matching is exact (fully anchored), case-sensitive, both-sides-independent, and the sentinel set is compiled in (no knob, consistent with D29). The matcher is a fixed metric-selection refinement — NOT a caller-supplied filter — so it does not violate the "no filters pushed to PromQL" rule (it never varies per request). It targets ONLY the `client` / `server` endpoint labels and does not affect the `cluster="unknown"` bucketing. See design.md D30 + D29 + D2/D7.

### 35.A Service-graph query selector

- [x] 35.A.1 `internal/promql/queries.go::Renderer.Render`: change the `QServiceGraphTotal` arm from `rate(traces_service_graph_request_total[%s])` to `rate(traces_service_graph_request_total{client!~"user|unknown",server!~"user|unknown"}[%s])`. Keep the bare `QServiceGraphTotal` constant value (`"traces_service_graph_request_total"`) UNCHANGED so the `query` / `query_name` self-metric and `kube_state_graph.query_name` span dimensions stay stable (D25 / D26). The metric-name prefix (D26) still does NOT apply to this metric — only the label matchers are added.
- [x] 35.A.2 `internal/promql/queries.go`: extract the sentinel matcher fragment as a documented package constant (e.g. `const serviceGraphSentinelSelector = `client!~"user|unknown",server!~"user|unknown"``) and reference it from the `Render` arm, so the deferred numeric-metric selectors (`traces_service_graph_request_failed_total`, `traces_service_graph_request_server_seconds_bucket`) reuse the identical fragment when they land (D30 forward note). Add a comment explaining the anchored / exact / case-sensitive semantics and the empty-UID-co-occurrence rationale.

### 35.B Tests

- [x] 35.B.1 `internal/promql/queries_test.go`: assert `Renderer{}.Render(QServiceGraphTotal, window)` emits the two anchored matchers verbatim; assert a non-empty-prefix `Renderer{Prefix: "o11y_"}` leaves the service-graph metric name bare (`traces_service_graph_request_total`, no prefix) while still carrying the matchers; assert the `QServiceGraphTotal` constant value itself is unchanged (bare metric name).
- [x] 35.B.2 `internal/build/servicegraph_test.go`: add a comment / guard documenting that `parseServiceGraph` itself does NOT filter sentinel labels — exclusion is upstream at the query layer, so a `client="user"` series handed directly to `parseServiceGraph` is (correctly) still parsed. The behavioural contract for exclusion lives at the promql layer (35.B.1) and is proven end-to-end at the integration layer (35.B.3). Do NOT add a parse-level sentinel filter.
- [x] 35.B.3 `internal/integration/graph_e2e_test.go`: ingest into the testcontainers VictoriaMetrics a normal `traces_service_graph_request_total` series (both UIDs resolvable) plus (a) a series with `client="user"`, (b) a series with `server="unknown"`, and (c) a series with `server="http://user/api"` (empty `server_k8s_pod_uid`). Query `/v1/graph` and assert: the normal series produces its edge; (a) and (b) produce NO edge and NO `user` / `unknown` node (real VictoriaMetrics evaluates the anchored matcher); (c) is NOT excluded and surfaces as an `others` (or resolved) endpoint — proving the anchored match does not catch substrings.
- [x] 35.B.4 Golden: scan existing fixtures under `internal/api/testdata/golden/` for any scenario whose mock upstream injects a `client` / `server` value of exactly `user` or `unknown` — none are expected (component / golden tests drive a `MockQuerier`, which returns vectors directly and is unaffected by the query-string matcher), so golden output should be unchanged. If any fixture does inject such a value, decide per-fixture whether to keep it (component tests bypass the query layer) or update it; re-run `go test ./internal/api/ -update -run Golden` only if a deliberate shape change results. Record the outcome in the PR.

### 35.C Docs

- [x] 35.C.1 `CLAUDE.md`: add a load-bearing-design-rule bullet for the sentinel exclusion (query-layer, fixed `{user, unknown}` set, anchored exact / case-sensitive match on `client` / `server` only, distinct from `cluster="unknown"` bucketing). Update the "No filters pushed to PromQL" bullet to carve out this fixed, request-invariant selector refinement. Mention it in the `ReadServiceGraph` line of the request-lifecycle diagram / prose.
- [x] 35.C.2 `docs/operations.md` "Exporter compatibility contract": note that `client` / `server ∈ {user, unknown}` series are excluded at the query layer (the connector's virtual nodes for uninstrumented / unresolved peers), so they never appear in the graph; document `user` / `unknown` as reserved sentinel values on the `client` / `server` dimension and that ingress visibility for uninstrumented callers is intentionally not surfaced in v1.
- [x] 35.C.3 `README.md` + `README.zh-tw.md` + `docs/api.md`: where the service-graph behaviour / node types are described, note that uninstrumented `user` ingress and `unknown` peers are dropped (no node / edge), and that this is fixed (no knob) in v1.

### 35.D Swag / OpenAPI

- [x] 35.D.1 No response-schema change (no new node `type`, edge type, or field — node / edge counts merely shrink). Confirm no swag `@Description` / `@Param` requires editing; if a `/v1/graph` description enumerates which endpoints are surfaced, optionally add the sentinel-exclusion note and run `make docs` to regenerate `docs/swagger.{json,yaml,go}` + `internal/api/static/openapi/*`. Otherwise this is a no-op.

### 35.E Validation

- [x] 35.E.1 `openspec validate "add-k8s-pod-graph-api"`.
- [x] 35.E.2 `make build`, `make vet`, `make test`, `make lint`.
- [x] 35.E.3 `make verify-mocks` — no interface signatures change; should stay clean.
