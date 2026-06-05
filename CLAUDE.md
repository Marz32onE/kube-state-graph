# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project purpose

`kube-state-graph` is a Go HTTP API that returns a unified pod / node / PVC graph
for **one or more Kubernetes clusters** read from a single centralised
VictoriaMetrics. Edges between pods come from `traces_service_graph_*` metrics
and may cross cluster boundaries.

The repo ships **only the API server**. `kube-state-metrics`, the service-graph
producer (Beyla / Alloy / Tempo or any compatible exporter), and VictoriaMetrics
are external dependencies. Topology comes from **kube-state-metrics** `kube_*`
series; service-graph edges come from `traces_service_graph_request_total`
(carrying `client_k8s_pod_uid` + `server_k8s_pod_uid`) — both read from the
centralised VictoriaMetrics. Multi-cluster, cross-cluster, and service-graph
code paths are exercised by the integration tests in `internal/integration/`
via the testcontainers-go VictoriaMetrics container, which ingests hand-crafted
fixture series through `POST /api/v1/import/prometheus`.

## Common commands

```bash
# First-time dev env bootstrap (run once after clone). Downloads modules and
# installs host-level dev tools (golangci-lint, govulncheck). Mockery is
# tracked via go.mod `tool` directive (Go 1.24+) and invoked through
# `go tool mockery` — no separate install step.
make init                                   # one-shot: init-go + init-tools
make doctor                                 # report toolchain versions / missing pieces
make init-hooks                             # optional: pre-commit gofmt+lint+quick-test, pre-push CI mirror

# Build / test loop
make build                                  # ./bin/kube-state-graph
make test                                   # go test ./... -count=1 -race -shuffle=on
make vet                                    # go vet
make lint                                   # golangci-lint (installed by `make init-tools`)
make vuln                                   # govulncheck
make cover                                  # go test ./... -coverprofile=coverage.out

# Mocks (regenerate after editing an interface listed in .mockery.yaml).
# Mocks are committed under internal/<pkg>/mocks/ so CI does not need
# mockery installed; the `mocks-drift` CI job verifies freshness.
make mocks                                  # go tool mockery
make verify-mocks                           # CI-style freshness check (regen + git diff)

# OpenAPI docs. Regenerate after editing @-annotations (cmd/.../main.go general
# info; internal/api/*.go operations) or handler signatures, then commit docs/.
# swag writes docs/swagger.{json,yaml}; docs/embed.go compiles them into the
# binary (served at /openapi.{json,yaml}). The /docs Scalar UI is CDN-loaded.
make docs                                   # go tool swag init --outputTypes json,yaml -> docs/
make check-docs                             # CI docs-drift mirror (regen + git diff docs/)

# Single test
go test ./pkg/graph/ -run TestProject_ClusterFilter -v
go test ./internal/api/ -run TestGolden -v

# Update golden files (after changing serialiser shape on purpose)
go test ./internal/api/ -update -run Golden

# Run binary directly
./bin/kube-state-graph --prom-url=http://localhost:8428 --listen-addr=:8080
```

Module path: `github.com/marz32one/kube-state-graph`. Minimum Go 1.25 (`go.mod`); build toolchain pinned to `go1.26.3` via the `toolchain` directive.

## Architecture (the 90 % you need to know)

### Request lifecycle

```
HTTP /v1/graph?start=&end=&...
   │
   ▼
parseGraphRequest        ── validates start/end (RFC 3339 or Unix seconds); only `end > start` is enforced
   │
   ▼
context.WithTimeout(ctx, --build-timeout)   ── graph endpoints only; deadline exceeded → 504 timeout
   └─ Builder.Build(ctx, window, end)
         ├─ ReadTopology  (errgroup of 8 PromQL queries in parallel: 5 KSM topology + 3 D29 service/endpointslice)
         ├─ ReadServiceGraph (1 PromQL, `user`/`unknown` peers excluded at selector — D30; joined with topology)
         └─ assemble + graph.NewGraph → *Graph (immutable, with adjacency)
   (no in-process concurrency cap; HPA + Pod resource limits handle load shedding)
   ▼
graph.Project(g, scope)            ── filters + traversal applied here, NOT during build
   ▼
serialiseCytoscape
```

v1 has **no in-process result cache** and **no singleflight**. Each request runs a fresh upstream fan-out and recomputes the body. A future iteration is expected to add a horizontally scalable cache mechanism for distributed deployment (Redis L2, background materialiser, or graph DB) — tracked as a separate change.

### Load-bearing design rules

These are non-obvious; read `openspec/changes/add-k8s-pod-graph-api/design.md`
(D1–D19) before changing any of them.

- **No server-side result cache.** Each `/v1/graph` request runs a fresh upstream PromQL fan-out. Filters (`cluster`, `namespace`, `edge_type`, `name`, traversal) are applied at response time as a projection over the freshly built `*Graph`. A horizontally scalable cache mechanism for distributed deployment is anticipated but out of scope for v1.
- **No time-window alignment, no window cap, no future-time guard.** `start` and `end` are passed through to upstream PromQL verbatim; only `end > start` is enforced. The previous 60 s `floor`/`ceil` grid was removed alongside the in-process cache it was bucketing for. Bounded query cost is delegated to upstream VictoriaMetrics search limits (`-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`, `-search.maxSamplesPerQuery`). Response body is `{apiVersion, clusters, elements}` — no time fields are echoed.
- **`labels` is strict `map[string]string`** on both nodes and edges. No bools,
  no numbers, no string-encoded numbers. Numeric edge metrics (`rate`, `p99_ms`,
  `error_rate`) and boolean flags (`cross_cluster`, `ghost`) are **deferred to a
  future typed struct field**. `pod-calls-pod` and `pod-calls-service` edges
  carry a single `labels.cluster` (the trace source / client-side cluster,
  omitted when the client side is non-pod). Cross-cluster status is derived by
  comparing the resolved source-node and target-node `labels.cluster` — D9.
- **Edge IDs are UUIDv5** with a fixed compiled-in namespace (`graph.edgeNamespace`)
  and the canonical input `<type>|<source>|<target>`. Stable across rebuilds —
  required for golden tests. Bumping the namespace UUID is a v2 break.
- **Cluster-scoped IDs everywhere.** Pods: `<cluster>/<uid>`, K8s nodes:
  `<cluster>/<node>`, PVCs: `<cluster>/<namespace>/<claim>`, externals:
  `external/<value>`. Node names are not globally unique without the prefix.
- **Connection-string resolution rule** (D29, hardcoded — no knob): for any
  service-graph endpoint whose pod UID is empty, the verbatim `client`/`server`
  label is checked for a `"://"` connection string. Detection is hardcoded —
  there is no operator-tunable substring and no config knob. Per-endpoint
  independent (both sides of a single edge are evaluated separately); edge `type`
  is `pod-calls-service` when the target resolves to a service node, otherwise
  `pod-calls-pod`. When a `"://"` label is found, its URL host is parsed and
  the optional `.svc.<domain>` suffix stripped, then resolved by dotted-label
  count. **Both** in-cluster DNS forms resolve to the **service** — there is no
  per-pod resolution; a `"://"` endpoint is never a pod:
  - **2 labels** `<service>.<namespace>` and **3 labels**
    `<pod>.<service>.<namespace>` (headless per-pod) both → a `type="service"`
    node (`id="<cluster>/<namespace>/<service>"`, `labels={cluster,namespace}`,
    `ipaddress=[cluster_ip]` unless headless `cluster_ip="None"`) plus on-demand
    `service-selects-pod` edges (service → pod, intra-cluster) fanning out to each
    backing pod. The 3-label form drops the leading pod-hostname and resolves as
    its parent service. A known service with zero backing endpoints still
    materialises the service node, with no fan-out edges.
  - **unresolvable** (host not a 2/3-label `.svc` name, or service absent from
    the trace cluster's topology) → an `external` node (`id="external/<label>"`,
    `labels={}`) with the verbatim label as `name`.
  A client-side `"://"` label resolves to `service` or `external` (never a pod),
  so the edge `labels.cluster` is always omitted for it.
- **Missing pod-UID human-label fallback** (D27, always on): when
  `client_k8s_pod_uid` or `server_k8s_pod_uid` is empty AND the corresponding
  `client`/`server` label is non-empty AND the label does NOT contain `"://"`,
  that endpoint is promoted to `external/<label>` (no cluster prefix; `labels={}`)
  instead of dropping the edge. Per-endpoint resolution order:
  (1) connection-string resolution (`"://"` in the label, empty UID) →
  `service` (+ `service-selects-pod` fan-out) or `external` per the D29 rule above
  (never a pod);
  (2) UID-based pod resolution / synth-pod fallback (only when UID is non-empty);
  (3) missing-UID human-label fallback (this rule) → external with `labels={}`
  (**only for non-`"://"` labels**);
  (4) drop (both UID and label empty). A `"://"` label never reaches this fallback
  — it is resolved (or produces an `external` node) at step (1). Edge
  `labels.cluster` is omitted whenever the client side resolves to a non-pod node,
  whether via the connection-string rule (`service` / `external`) or this fallback
  (`external`).
- **Self-loop UID guard** (D33, always on, no knob): a pre-resolution
  normalisation in `parseServiceGraph`, applied **before** the resolution order
  above. Some `servicegraph` exporters stamp the **caller's own** pod UID onto
  **both** sides for a peer they could only identify as a `"://"` connection
  string, so `client_k8s_pod_uid == server_k8s_pod_uid` (non-empty, equal) while
  the real target lives only in the `"://"` label. A populated UID normally
  short-circuits Stage 0 (step 1 above), so the `"://"` side would collapse onto
  the caller's own pod — a self-loop `pod-calls-pod` edge, **no service node**.
  The guard: when the two UIDs are non-empty AND equal, clear the UID on **any
  side whose label contains `"://"`** (that side only), so it falls through to
  connection-string resolution; the non-`"://"` side keeps the shared UID and
  resolves to its real pod. Fires ONLY on the conjunction (UID collision AND a
  `"://"` label on the cleared side): differing UIDs are untouched (`"://"` with
  a populated UID still takes pod-UID resolution), and a UID collision with no
  `"://"` label stays a legitimate `pod-calls-pod` self-loop. Do NOT broaden this
  into a global "`"://"` always beats UID" reorder — that breaks the
  populated-UID-means-pod contract; the collision is the specific fingerprint of
  the exporter defect. Determinism unaffected (pure function of the two UID + two
  string labels); no new node/edge type. Tests:
  `pkg/build/servicegraph_test.go` (`TestParseServiceGraph_SelfLoopUID_*`) and
  `internal/integration` (`TestConnStringSelfLoopUIDResolvesToServiceNode`).
- **Sentinel-endpoint exclusion at the query layer** (D30, hardcoded — no knob):
  the `servicegraph` connector emits virtual peers for endpoints it cannot pair
  to an instrumented span — an uninstrumented caller as `client="user"`, an
  unresolved peer as `"unknown"`. The service-graph selector drops these
  **upstream** via anchored negative matchers —
  `rate(traces_service_graph_request_total{client!~"user|unknown",server!~"user|unknown"}[w])`
  — so the series never reach the resolver: no node (`pod` / synth / `service` /
  `external`) and no edge is produced for a `user` / `unknown` peer.
  PromQL `!~` is fully anchored, so the match is **exact** and **case-sensitive**
  (a `http://user/...` connection string is NOT excluded — it is not equal to
  `user`). Applied to both `client` and `server` (either side matching drops the
  series). This is a fixed selector contract on the `client` / `server` labels
  only — it does NOT touch the `cluster="unknown"` bucketing (a different label).
  The matcher fragment lives in `promql.serviceGraphSentinelSelector`; the
  `QServiceGraphTotal` constant stays the bare metric name so `query_name`
  self-metric / span dimensions are unchanged. Deferred numeric service-graph
  metrics MUST reuse the same fragment when added.
- **Server-side pod resolution** uses `Topology.PodsByUID` — a global pod-UID
  index built from all loaded clusters. Service-graph metrics carry only the
  trace-source `cluster` (client side); the server side's cluster is recovered
  by looking up `server_k8s_pod_uid` against this index, since K8s pod UIDs
  are unique cross-cluster in practice. Missing UIDs (with non-empty server
  label) follow the missing-UID fallback above; UIDs present but unknown
  to topology become synth pods with `cluster=""` (server-side cluster
  unknown).
- **No filters pushed to PromQL.** Each build loads every cluster present in upstream VictoriaMetrics. Caller-supplied filters (`cluster`, `namespace`, `edge_type`, `name`, traversal) are applied at projection time over the freshly built `*Graph`. Bounded query cost is delegated to upstream VictoriaMetrics search limits. The one fixed exception is the D30 sentinel matcher (`client!~"user|unknown",server!~"user|unknown"`) on the service-graph selector — it is a **request-invariant metric-selection contract**, not a caller filter, so it never varies per request and does not break the projection-over-graph contract a future cache relies on.
- **`/v1/edge-types` reads from `graph.EdgeTypes` only** — a single in-code
  registry shared with the builder. Adding an edge type = update both the
  builder and the registry in the same change; the API can never list a type
  the builder cannot produce. Current edge types include `pod-calls-pod`,
  `pod-calls-service` (emitted when a `"://"` connection-string resolves to an
  in-cluster service node; always intra-cluster), and `service-selects-pod`
  (directed service → pod, intra-cluster; emitted on demand by the D29
  connection-string resolution).
- **API-key auth is the only HTTP auth in v1.** Header is `X-API-Key`. Keys
  come from `--api-keys-file` (K8s `Secret` mount, hot-reloaded) or
  `--api-keys`. Empty keyset = auth disabled (dev default). Open paths
  (no key required): `/livez`, `/readyz`, `/metrics`, `/openapi.*`, `/docs`.
  The Scalar UI at `/docs` is a tiny HTML page that loads the Scalar bundle
  from the jsDelivr CDN and renders the same-origin `/openapi.json`; the spec
  itself is generated by `swag` into `docs/` and embedded via `docs/embed.go`.
  Validation is constant-time and iterates the whole set —
  do NOT add early-return optimisations to `auth.KeySet.Validate`. Logs must
  never include the presented key value.
- **Deterministic response body.** The serialiser produces byte-identical output for the same `(window, filters, upstream-data)`: node/edge slices MUST go through `graph.SortNodes`/`SortEdges`, `Graph.ClusterNames()` MUST sort, and the response body MUST NOT carry time-of-build or echo-of-input fields. Body shape is fixed at `{apiVersion, clusters, elements}`. Don't add timestamps, random IDs, or unsorted map iteration to the response — golden tests will break.
- **IP addresses live on the typed `ipaddress` attribute, never in `labels`.** `PodNode.IPAddress()` carries `[pod_ip]` from `kube_pod_info` (when present). `K8sNode.IPAddress()` carries `[external_ip]` from `kube_node_status_addresses{type="ExternalIP"}` (when present). `ServiceNode.IPAddress()` carries `[cluster_ip]` from `kube_service_info` (when present, omitted for headless `cluster_ip="None"`). `PVCNode` and `ExternalNode` always return nil. `host_ip` from `kube_pod_info` is intentionally dropped — it is the node's IP, surfaced via the node entry instead. The serialiser emits `data.ipaddress` (with `omitempty`); `labels.pod_ip`, `labels.host_ip`, `labels.external_ip`, and `labels.cluster_ip` MUST NOT appear.
- **Cytoscape compound nodes are presentation-only (D31).** `serialiseCytoscape` synthesises a `type="cluster"` group node per cluster (`id="cluster/<name>"`, `labels={}`, no `ipaddress`, sorted first) and sets `data.parent` (`omitempty`) for nesting: `cluster > node > pod` (pod → its `labels.node`, falling back to its cluster group when that node is out of scope), and `cluster > service` / `cluster > pvc` (services/PVCs are cluster-level siblings, NEVER pod containers — a Service spans nodes and a pod can back many Services). `external` nodes get no parent. There is **no `pod-runs-on-node` edge type** — the `cluster > node > pod` nesting (from each pod's `labels.node`) is the sole representation of the pod→node relationship, so K8s `node` nodes carry no edges. A consequence: a `name` filter or a traversal anchored on a pod no longer pulls in its host node. A `?namespace=` filter is the exception — although a K8s node carries no namespace label of its own, it is **retained iff some pod that survived the namespace filter is scheduled on it** (its `labels.node`), so `cluster > node > pod` nesting is preserved for nodes hosting the filtered pods while nodes hosting none of them drop. This host-of-in-scope-pod rule lives in `graph.Project`/`filterNodes` (`k8sNodePassesFilters`) — the one place K8s-node admission is namespace-aware; the nesting itself stays presentation-only. Cluster group nodes are serialiser-synthesised DTOs, not `GraphNode`s. The pod→node parent derives from `labels.node` (a contract field), so the nesting is independent of any edge and survives `?edge_type=` projection.
- **OTLP tracing/logging is config'd by OTel env vars only** (`OTEL_EXPORTER_OTLP_*`, `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER`). No bespoke `--otlp-*` flags. Telemetry defaults to no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset (zero export overhead, no background goroutines). Tracing MUST NOT alter response bodies — resource attrs and span IDs live on spans, never in JSON. `otelgin` is mounted on `/v1/*` only; `/livez`, `/readyz`, `/metrics`, and `/docs/*` are deliberately untraced. The auth middleware MUST NEVER log or attribute the presented `X-API-Key` value via either the local handler or the OTLP slog bridge.
- **Upstream metric-name prefix is an additive `KSG_METRIC_PREFIX` knob** applied to KSM-shaped series only (`kube_pod_info`, `kube_node_info`, `kube_node_status_addresses`, `kube_pod_spec_volumes_persistentvolumeclaims_info`, `kube_node_labels`, `kube_service_info`, `kube_endpointslice_endpoints`, `kube_endpointslice_labels`, and the `kube_node_info`-backed cluster-discovery query). The prefix is prepended verbatim — trailing underscore is the operator's responsibility. NOT applied to `traces_service_graph_request_total` (different exporter family — Alloy/Tempo) or `up{}` (Prometheus-native). The D29 endpointslice → service join reads `kube_endpointslice_labels{label_kubernetes_io_service_name}`, which KSM only emits when `--metric-labels-allowlist=endpointslices=[kubernetes.io/service-name]` is set (NOT exposed by default). The metric-name suffix and the label-name set per series are a fixed contract any compatible exporter MUST honour; see design.md D26. Threaded via `promql.Renderer{Prefix}` held on `build.Builder` and `api.Server`; the `Query` string constants remain bare so `query=` / `query_name=` dimensions on self-metrics and spans stay stable across deployments that differ only by prefix.

### Reusable `pkg/` graph engine (D32)

The graph engine lives under `pkg/` so other Go modules can import it in-process
(no HTTP, no JSON round-trip); `internal/api` is a thin HTTP / auth shell over it:

- `pkg/graph` — `Graph`, the sealed `GraphNode` + five node types, `Edge`,
  `Project`, `Scope` / `NewScope`, `View`, `SortNodes` / `SortEdges`, `EdgeTypes`.
- `pkg/build` — `Builder` + `Build`; topology / service-graph readers. Takes a
  `build.Options{MetricPrefix, APITimeout}` and a no-op-tolerant `build.Metrics`
  interface — **not** `internal/config` / `internal/observability`, whose
  couplings were broken so the package is externally importable.
- `pkg/promql` — `Querier`, `Renderer`, `Client`, and a no-op-tolerant
  `promql.Metrics` interface.
- `pkg/clock`; `pkg/cytoscape` — `Serialise(g, view) Body` plus the Cytoscape DTO.
- `pkg/kubegraph` — the convenience facade: `Engine.BuildFromValues(ctx,
  url.Values) (cytoscape.Body, error)` folds parse → build → project → serialise
  into one call. `kubegraph.ParseValues` is the **single** request parser, shared
  by `internal/api`'s handler and the facade, so the `/v1/graph` request contract
  cannot drift between the server and an embedded consumer.

`pkg/` packages MUST NOT import `internal/*` — Go's internal rule would block any
external module from importing the engine. Metrics and OTLP tracing are injected
with no-op defaults, so an embedder does not inherit ksg's `kube_state_graph_*`
self-metrics; the concrete `*observability.Metrics` satisfies
`build.Metrics` / `promql.Metrics` structurally via wrappers in
`internal/observability/adapters.go`. The first external consumer is
`graph-api-gateway` (its `embed-ksg-graph-engine` change).

### Sealed graph types

`graph.GraphNode` is a sealed interface (`isGraphNode()` unexported). Concrete
types: `PodNode`, `K8sNode`, `PVCNode`, `ServiceNode`, `ExternalNode`. All
expose `ID()`, `Name()`, `Type()`, `Labels()`, `IPAddress()`. Serialisation
goes through these methods — never through type switches in the serialiser.
`IPAddress()` returns nil for `PVCNode` / `ExternalNode`; `PodNode` returns
`[pod_ip]` when known;
`K8sNode` returns `[external_ip]` when known; `ServiceNode` returns
`[cluster_ip]` when known (nil when headless `cluster_ip="None"`).

### Test stack layers

Boundary rule: **unit tests must not contact a real upstream service**. Anything
that needs a TCP socket fronting upstream is integration. Unit tests substitute
upstream behind small interfaces (`promql.Querier`, `auth.Validator`,
`clock.Clock`) using mockery-generated mocks under `pkg/{clock,promql}/mocks/`
and `internal/auth/mocks/`.

| Layer | Where | Real I/O? |
|---|---|---|
| Unit | `pkg/{graph,build,promql,clock,cytoscape,kubegraph}/*_test.go` + `internal/{config,auth,telemetry}/*_test.go` | None — pure functions: parsers, joins, projection, edge IDs, request parsing, serialiser, KeySet, Clock. |
| Component | `internal/api/*_test.go` | None — gin handlers driven via a `MockQuerier` injected through `promql.Querier`; `httptest.NewServer` only wraps the server-under-test, never fakes upstream. Test helpers in `internal/api/helpers_test.go` (`newServerWithMocks`, `newMockQuerier`, `newErrQuerier`, `vec`). |
| Golden | `internal/api/golden_test.go` + `testdata/golden/*.json` | None. Wire-format snapshots; run with `-update` to refresh. |
| Property | `pkg/graph/property_test.go` | None. Random multi-cluster graphs → invariants (orphan edges, traversal depth, ID uniqueness). |
| Integration | `internal/integration/*` | **Docker required.** testcontainers-go VictoriaMetrics suite; gated `SkipIfDockerUnavailable` — skips locally without Docker, runs full on CI (ubuntu-latest). Inject hooks into the in-process API via `StartAPIServer(cfg, WithClock(...))`. |

When **adding a unit test that needs to fake upstream PromQL**, use
`newMockQuerier(t, fixtureSet{...})` — never spin up an `httptest.NewServer`
to impersonate the Prometheus HTTP API.

When **changing an interface** registered in `.mockery.yaml`
(`promql.Querier`, `auth.Validator`, `clock.Clock`), run `make mocks` and
commit the regenerated files. CI's `mocks-drift` job will fail otherwise.

## OpenSpec workflow

Spec-driven changes live under `openspec/changes/<name>/` with four artifacts
in dependency order: **proposal → design + specs → tasks**. The
`/opsx:*` commands and the `openspec` CLI manage the lifecycle.

Common openspec commands:

```bash
openspec list                                       # all active changes
openspec status --change "<name>"                   # artifact progress + tasks
openspec validate "<name>"                          # checks structure
openspec instructions <artifact> --change "<name>" --json   # what to write
openspec verify "<name>"                            # before archive
openspec archive "<name>"                           # promote to openspec/specs/
```

The active change for the v1 implementation is **`add-k8s-pod-graph-api`**.
When making non-trivial behaviour changes, update the relevant artifact
(usually `specs/<capability>/spec.md` or `design.md`) before touching code.

## Repository conventions

- All HTTP routes live under `/v1/`. Adding a route means committing to keeping
  it for v1's lifetime. Schema changes that aren't additive are v2 — see D14.
- Self-metric names are stable contracts: `kube_state_graph_*`. Adding a label
  to an existing metric is a contract change — see design.md D26.
- Errors returned to HTTP carry a typed `build.Reason` mapped to a fixed
  status + `reason` string in `internal/api/errors.go`. Adding new failure
  modes means adding both a `Reason` constant and an entry in `mapBuildError`.
- Don't import `k8s.io/client-go` or any Kubernetes API into the API server.
  All cluster facts come from VictoriaMetrics. Informers were considered and
  rejected — see D1 / D16. Tests and harness tooling are exempt.
- Don't add dependencies casually. Current direct deps: Gin, Prometheus
  client_golang, google/uuid, golang.org/x/sync, testify v1.11.x (test-only,
  also drives mockery-generated mocks), testcontainers-go (integration
  test-only), swaggo/swag/v2 (codegen tool, not imported at runtime),
  vektra/mockery v2.x (codegen tool tracked via go.mod `tool` directive,
  not imported at runtime, not linked into the production binary), and the
  OpenTelemetry Go SDK family (`go.opentelemetry.io/otel`, `sdk`, `sdk/log`,
  OTLP gRPC + HTTP exporters for `otlptrace` and `otlplog`, `semconv/v1.27.0`,
  `contrib/...otelgin`, `contrib/...otelhttp`, `contrib/bridges/otelslog`).
  Adding more requires a design-doc note.
- Production code MUST NOT carry test-only fields, methods, or constructors.
  Inject substitutable behaviour via the small interfaces in
  `internal/{promql,auth,clock}` (`Querier`, `Validator`, `Clock`); tests
  consume mockery-generated mocks under `internal/<pkg>/mocks/`. If a new
  hard-to-test dependency appears, add an interface + regenerate mocks rather
  than a `SetXxxFunc` setter.
