## 1. Project bootstrap

- [x] 1.1 Initialise Go module (`go mod init github.com/<org>/kube-state-graph`, Go 1.22+).
- [x] 1.2 Add direct dependencies: `github.com/gin-gonic/gin`, `github.com/prometheus/client_golang`, `github.com/dgraph-io/ristretto/v2`, `github.com/google/uuid`, `github.com/cespare/xxhash/v2`, `golang.org/x/sync` (singleflight + errgroup + semaphore).
- [x] 1.3 Lay out package skeleton: `cmd/kube-state-graph/`, `internal/api/`, `internal/build/`, `internal/cache/`, `internal/promql/`, `internal/graph/`, `internal/config/`, `internal/observability/`.
- [x] 1.4 Add baseline `Makefile` targets: `build`, `test`, `vet`, `lint`, `cover`, `kind-up`, `kind-down`, `smoke`.
- [x] 1.5 Wire up `golangci-lint` config and a pre-commit/CI step that runs `go vet`, `golangci-lint`, and `go test ./...`.
- [x] 1.6 Add `.editorconfig`, `LICENSE`, top-level `README.md` placeholder.

## 2. Configuration and structured logging

- [x] 2.1 Define `internal/config.Config` struct covering: `--prom-url`, `--listen-addr`, `--max-window`, `--max-skew`, `--max-pods`, `--build-timeout`, `--build-concurrency`, `--cluster-discovery-lookback`, `--clusters-allowlist`, `--external-name-pattern`, `--cache-max-cost-bytes`, `--enable-debug`, `--log-level`.
- [x] 2.2 Bind flags + env vars (`KSG_*`) using stdlib `flag` and `os.LookupEnv`; implement `Validate()` invariants (positive window, valid URL, etc.).
- [x] 2.3 Implement `internal/observability/log.New(level)` returning a `*slog.Logger` backed by a JSON handler; install as default.
- [x] 2.4 Add request-ID middleware that attaches an ID to context and logs one line per HTTP request with method/path/status/duration/request_id/cluster filters/`cache_status`.

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
- [x] 4.3 Implement `--clusters-allowlist` injection: build a `{cluster=~"a|b|c"}` fragment and splice it into every query template (and into both `client_cluster=~` and `server_cluster=~` for service-graph queries).
- [x] 4.4 Implement `count(kube_pod_info)` cluster-size probe used to enforce `--max-pods` before a full build.
- [x] 4.5 Implement parallel fan-out via `errgroup.WithContext`; per-call context timeout from `--build-timeout`; abort whole build on any sub-query failure.

## 5. Cluster topology source (capability: cluster-topology-source)

- [x] 5.1 Implement `internal/build/topology.Read(ctx, q, window, end, allowlist) (Topology, error)`.
- [x] 5.2 Parse `kube_pod_info` series into `PodNode` entities: `id="<cluster>/<uid>"`, `name=<pod>`, `type="pod"`, `labels` includes `cluster`, `namespace`, `node` (cluster-scoped node ID).
- [x] 5.3 Parse `kube_node_info` + `kube_node_status_addresses` into `K8sNode` entities; surface `external_ip` under `labels`.
- [x] 5.4 Parse `kube_pod_spec_volumes_persistentvolumeclaims_info` into `PVCNode` entities; key `<cluster>/<namespace>/<claim_name>`.
- [x] 5.5 Parse `kube_node_labels` `label_*` entries; flatten into the K8s node `labels` under their original keys (e.g., `label_topology_kubernetes_io_zone` → `topology.kubernetes.io/zone`).
- [x] 5.6 Implement pod-restart handling: when multiple UIDs exist for the same `(cluster, namespace, pod)`, keep the latest as canonical, retain the prior, emit a `pod-replaced-by` synthetic edge.
- [x] 5.7 Implement `cluster="unknown"` bucketing for series missing the `cluster` label; surface in `kube_state_graph_clusters_observed`.
- [x] 5.8 Build `pod-runs-on-node` edges from `kube_pod_info{node=...}`.
- [x] 5.9 Build `pod-mounts-pvc` edges by joining `kube_pod_spec_volumes_persistentvolumeclaims_info` with the pod's host node within the same cluster.

## 6. Pod service-graph reader (capability: pod-service-graph)

- [x] 6.1 Implement `internal/build/servicegraph.Read(ctx, q, window, end, allowlist, externalPattern) ([]Edge, []ExternalNode, error)`.
- [x] 6.2 Compute `rate(traces_service_graph_request_total[<window>]) @ <end>`; drop series whose rate is exactly zero.
- [x] 6.3 For each surviving series, perform per-endpoint substitution: if `KSG_EXTERNAL_NAME_PATTERN` is non-empty AND the `client` (or `server`) label value contains the pattern → external node (`id="external/<value>"`, `name=<value>`, `type="external"`, `labels={"pattern":"<pattern>"}`); else pod-UID resolution via topology map.
- [x] 6.4 When pod-UID resolution finds no topology entry, emit a synthesised pod node (`name=<pod-uid>`, `labels.cluster=<cluster>`, no `ghost` flag).
- [x] 6.5 Set edge `labels.client_cluster` and `labels.server_cluster` (empty string for external endpoints).
- [x] 6.6 Confirm numeric metrics (`rate`, `p99_ms`, `error_rate`) are NOT written into `labels` in v1; add a regression test that asserts no such keys appear.
- [x] 6.7 Tolerate empty / sparse upstream — `nil` or empty Vector results MUST yield zero edges, never an error.

## 7. Build pipeline + cache stack

- [x] 7.1 Implement `internal/build.Build(ctx, q, window, end, allowlist, externalPattern) (*graph.Graph, error)`: runs topology + service-graph readers in parallel, joins, returns the global multi-cluster graph plus pre-computed adjacency.
- [x] 7.2 Implement `internal/cache.Cache` interface (`Get`, `Set`, `Delete`, `Stats`, `Close`) wrapping `ristretto.Cache[uint64, *graph.Graph]`.
- [x] 7.3 Implement time-class bucketing: classify `end` into `live | recent | historical | frozen`; round `start` and `end` down to the bucket boundary; expose `(start_actual, end_actual, bucket_seconds, ttl)`.
- [x] 7.4 Implement cache-key hashing: xxhash of canonical-JSON `{start_bucket, end_bucket, bucket_size}`.
- [x] 7.5 Implement singleflight wrapper keyed by the cache key; on miss, run `Build()` once; populate cache after success; release waiters with the same `*Graph`.
- [x] 7.6 Implement build-concurrency cap via `semaphore.Weighted`; on `TryAcquire` failure return a typed error mapped to `503 capacity`.
- [x] 7.7 Implement per-build timeout via `context.WithTimeout`; map timeout to `503 timeout`.
- [x] 7.8 Implement `--max-pods` enforcement: on probe overflow return typed error mapped to `503 cluster_too_large`.
- [x] 7.9 Implement outside-retention detection: zero topology rows + healthy `up{}` ⇒ `400 outside_retention`.

## 8. HTTP API (capability: graph-api)

- [x] 8.1 Stand up Gin engine with `/v1/` route group, request-ID + slog middleware.
- [x] 8.2 Implement `GET /v1/graph` handler: parse + validate `start`, `end`, filter params, traversal params; bucket + cache + project + serialise.
- [x] 8.3 Implement Cytoscape.js serialiser: `{ apiVersion, start, end, start_actual, end_actual, bucket_seconds, built_at, clusters, elements: { nodes, edges } }` with canonical node/edge `data` shape.
- [x] 8.4 Implement `GET /v1/graph/nodegraph` handler: project → Grafana Node Graph JSON (`nodes_fields`/`nodes`/`edges_fields`/`edges`); map `name`→`title`, cluster·namespace→`subTitle`, `type`→`mainStat`, edge `type`→edge `mainStat`, `secondaryStat` omitted.
- [x] 8.5 Implement `GET /v1/clusters` handler: live discovery query (cached 60 s under fixed key) intersected with `--clusters-allowlist`.
- [x] 8.6 Implement `GET /v1/edge-types` handler: serialise the in-code registry; long `Cache-Control` and registry-hash `ETag`; honour `If-None-Match`.
- [x] 8.7 Implement `ETag` (sha256 of body) + `Cache-Control` (max-age from time class) + `X-Cache` (`HIT|MISS|COALESCED`) headers on graph endpoints.
- [x] 8.8 Implement `If-None-Match` 304 short-circuit on graph endpoints.
- [x] 8.9 Implement traversal pruning: BFS over the cached graph's adjacency map bounded by `depth`; reject `depth > 6` with `400 depth_too_large`.
- [x] 8.10 Implement filter validation: reject obviously malformed values; treat unknown values as empty result, not error.
- [x] 8.11 Implement `GET /livez` (always 200) and `GET /readyz` (1 s `up{}` probe → 200 / 503).
- [x] 8.12 Implement `DELETE /admin/cache` (flush) and `GET /debug/last-queries` (behind `--enable-debug`).
- [x] 8.13 Implement uniform JSON error body `{ apiVersion, error: { reason, message } }` for 4xx/5xx and apply consistently.

## 9. Observability

- [x] 9.1 Register `kube_state_graph_*` Prometheus metrics: `build_duration_seconds{cache_status}`, `project_duration_seconds`, `serialise_duration_seconds{format}`, `cache_hits_total{layer}`, `cache_misses_total{layer}`, `cache_size_entries`, `cache_cost_bytes`, `cache_evictions_total{reason}`, `cache_rejected_total`, `singleflight_dedup_total`, `build_concurrency`, `build_rejected_total{reason}`, `graph_node_count{cluster,kind}`, `graph_edge_count{type,cross_cluster}`, `clusters_observed`, `upstream_query_duration_seconds{query}`, `upstream_query_failures_total{query}`, `http_requests_total{path,status}`.
- [x] 9.2 Wire metrics emission into the build pipeline, cache, singleflight, HTTP middleware, and upstream client.
- [x] 9.3 Expose `/metrics` (Prometheus exposition); confirm `cluster` and `cross_cluster` labels appear on observational gauges.
- [x] 9.4 Emit one structured `slog.Info("graph built", ...)` log line per build with the documented fields.

## 10. Unit tests

- [x] 10.1 Unit-test `graph.Project` against hand-crafted graphs covering: cluster filter, namespace filter, edge-type filter, traversal at depth 0/1/2/6, unknown root.
- [x] 10.2 Unit-test topology parser against canned `model.Vector` inputs: pod-restart handling, missing `cluster` bucketing, K8s node label flattening.
- [x] 10.3 Unit-test service-graph parser: zero-rate drop, ghost-pod fallback, cross-cluster edge labels, allowlist filtering on both endpoints.
- [x] 10.4 Unit-test external-name-pattern substitution: empty pattern (disabled), match on client only, match on server only, match on both, no match.
- [x] 10.5 Unit-test edge-ID generator: stability across rebuilds, RFC 4122 / UUIDv5 format, distinct IDs for distinct `(type, source, target)`.
- [x] 10.6 Unit-test bucket classification (live / recent / historical / frozen) and cache-key hashing canonicalisation (sorted-unique semantics, byte-identical key for equivalent input).

## 11. Component tests (httptest mock upstream)

- [x] 11.1 Build a reusable `httptest.Server` that serves canned PromQL JSON for a fixture set keyed by query string.
- [x] 11.2 Component-test the build pipeline end to end: cache miss populates cache, second request is a hit, `X-Cache` header reflects state.
- [x] 11.3 Component-test singleflight: N concurrent requests for the same bucket trigger exactly one upstream fan-out.
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
- [x] 13.4 Property: cross-cluster edges have `labels.client_cluster != labels.server_cluster`.
- [x] 13.5 Property: edge IDs are unique per `(type, source, target)` and stable across re-runs.

## 14. Verification harness (capability: verification-harness)

- [x] 14.1 Author `deploy/kind/kind-config.yaml` (single cluster, 2 worker nodes).
- [x] 14.2 Author `deploy/kind/bootstrap.sh` that creates the Kind cluster and applies all manifests.
- [x] 14.3 Author manifests for in-cluster VictoriaMetrics single-node (`vmsingle`); confirm no `vmstorage`/`vmselect`/`vminsert`.
- [x] 14.4 Implement `tests/harness/vm-fixtures/` Go program: reads YAML fixture file, exposes `/metrics`, reloads on SIGHUP, exposes `vm_fixtures_reloaded_total`.
- [x] 14.5 Author `tests/harness/vm-fixtures/fixtures.yaml` with multi-cluster `kube_*` series, at least one cross-cluster service-graph series, at least one `client="http://..."` series for external substitution.
- [x] 14.6 Author manifests for the `kube-state-graph` API server Deployment with `KSG_EXTERNAL_NAME_PATTERN="://"` set in the env section.
- [x] 14.7 Author `tests/smoke/run.sh` covering all assertions in the `verification-harness` spec (livez, readyz, clusters, edge-types, graph, multi-cluster filter, cross-cluster edge present, external node present, canonical schema enforced, metrics exposition).
- [x] 14.8 Author `deploy/kind/teardown.sh` for reproducible cluster deletion.
- [x] 14.9 Optional Grafana dashboard at `deploy/grafana/kube-state-graph-nodegraph.json` using the JSON / Infinity datasource against `/v1/graph/nodegraph`.

## 15. CI integration

- [x] 15.1 Add CI workflow stage running `go vet`, `golangci-lint`, `go test ./...` (unit + component + golden + property) on every PR.
- [x] 15.2 Add CI stage that runs the harness bootstrap + smoke script on PRs touching `cmd/`, `internal/build/`, `internal/cache/`, `deploy/kind/`, or `tests/harness/`, and on a nightly schedule.
- [x] 15.3 Publish a container image for `kube-state-graph` from CI for tagged commits; basic Dockerfile checked in.

## 16. Documentation

- [x] 16.1 Write `README.md`: what the server is, the data flow diagram, single-binary usage, env / flag reference.
- [x] 16.2 Write `docs/api.md`: response shapes, query parameters, time-bucket policy, status codes and `reason` values, ETag / Cache-Control semantics.
- [x] 16.3 Write `docs/multi-cluster.md`: producer-side scrape `external_labels: { cluster: ... }` requirement, OTel Collector `servicegraph` connector configuration with `dimensions: [k8s.pod.uid, k8s.namespace.name]`, expected `client_cluster` / `server_cluster` labels.
- [x] 16.4 Write `docs/external-substitution.md`: `KSG_EXTERNAL_NAME_PATTERN` semantics, recommended values (`://`, `@`), examples of resulting graphs.
- [x] 16.5 Write `docs/operations.md`: self-metrics, alert recipes, `/livez` / `/readyz` semantics, capacity planning notes.

## 17. Pre-archive verification

- [x] 17.1 Run `openspec verify "add-k8s-pod-graph-api"` and confirm every requirement maps to an implementation file or test.
- [ ] 17.2 Confirm `go test ./... -cover` reports ≥ 80 % coverage on `internal/build`, `internal/graph`, `internal/cache`, `internal/api`. _(Current: 41.4 % across the four packages; deferred — needs additional handler / orchestrator tests.)_
- [ ] 17.3 Run the manual Grafana rig locally; record the resulting Grafana panel screenshot in `docs/`. _(Requires Docker + Kind on the host; not exercised in this session.)_
- [ ] 17.4 Tag a `v0.1.0` release once all preceding tasks are checked.

## 18. Container integration tests (capability: container-integration)

- [x] 18.1 Add `github.com/testcontainers/testcontainers-go` (and its `wait` subpkg) as a direct test dependency.
- [x] 18.2 Create `internal/integration/` package with `VMSuite` (`testify/suite.Suite`) whose `SetupSuite` starts a single VictoriaMetrics container (image pinned `victoriametrics/victoria-metrics:v1.107.0`) and `TearDownSuite` tears it down.
- [x] 18.3 Implement `IngestExpFmt(exposition string)` on `VMSuite` that POSTs to `<vm.URL>/api/v1/import/prometheus`, plus `WaitForSeries(query, budget)` polling helper.
- [x] 18.4 Implement readiness wait that polls VM `/-/ready` until 200 within a configurable budget (default 10 s); fail with `vm_not_ready` on timeout.
- [x] 18.5 Implement an in-process API-server-under-test factory (`StartAPIServer(configure func(*config.Config)) *httptest.Server`) on `VMSuite` that wires `api.New(...).Handler()`.
- [x] 18.6 Author absolute-timestamp fixtures and corresponding tests for: single-cluster `pod-runs-on-node`, cross-cluster `pod-calls-pod`, `KSG_EXTERNAL_NAME_PATTERN` substitution producing an external node, ETag round-trip 304, `X-Cache: MISS` → `HIT`, `/v1/clusters` discovery, `/v1/edge-types` shape.
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

- [x] 20.1 Move integration-only assets out of CI-implying paths: `deploy/kind/` → `local/grafana/`; smoke script → `local/grafana/smoke.sh`. `tests/harness/vm-fixtures/` retained (Go program; bootstrap references it).
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
- [x] 21.3 Annotate every Gin handler in `internal/api/handlers.go` and `internal/api/docs.go` with `@Summary`, `@Description`, `@Tags`, `@Param` (per query parameter), `@Success` / `@Failure`, `@Router`. Covers all `/v1/*`, `/livez`, `/readyz`, `/metrics`, `/admin/cache`, `/debug/last-queries`, `/openapi.yaml`, `/openapi.json`, `/docs`, `/docs/assets/*`.
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
