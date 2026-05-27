# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project purpose

`kube-state-graph` is a Go HTTP API that returns a unified pod / node / PVC graph
for **one or more Kubernetes clusters** read from a single centralised
VictoriaMetrics. Edges between pods come from `traces_service_graph_*` metrics
and may cross cluster boundaries.

The repo ships **only the API server**. `kube-state-metrics`, the service-graph
producer, VictoriaMetrics, and Kind are external dependencies. The in-repo
`local/kind/` rig is local-only scaffolding, not a deliverable. It uses
**kube-state-metrics** (scraping the kind cluster, with a `cluster=kind-local`
relabel injected by VictoriaMetrics' scrape config) to produce the kube_*
topology series the API consumes. Service-graph metrics
(`traces_service_graph_request_total`) are produced locally by a Beyla
DaemonSet that auto-instruments pods in the `kube-state-graph` namespace and
ships OTLP spans to a Grafana Alloy Deployment; Alloy's
`otelcol.connector.servicegraph` (configured with `dimensions=["k8s.pod.uid"]`)
emits the metric with `client_k8s_pod_uid` + `server_k8s_pod_uid` and remote-
writes to VictoriaMetrics. The rig deliberately ships no synthetic traffic
generator — the existing in-cluster Go traffic (kube-state-graph→VM→KSM,
Grafana→kube-state-graph, etc.) is enough to populate paired client+server
spans. Cross-cluster scenarios (which a single Kind cannot demonstrate) are
still exercised by integration tests in `internal/integration/` via the
testcontainers-go VictoriaMetrics container.

## Common commands

```bash
# First-time dev env bootstrap (run once after clone). Downloads modules and
# installs host-level dev tools (golangci-lint, govulncheck). Mockery is
# tracked via go.mod `tool` directive (Go 1.24+) and invoked through
# `go tool mockery` — no separate install step.
make init                                   # one-shot: init-go + init-tools
make doctor                                 # report toolchain versions / missing pieces
make init-hooks                             # optional: pre-commit gofmt + go vet hook

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

# Single test
go test ./internal/graph/ -run TestProject_ClusterFilter -v
go test ./internal/api/ -run TestGolden -v

# Update golden files (after changing serialiser shape on purpose)
go test ./internal/api/ -update -run Golden

# Local kind rig (NOT run by CI; requires Docker + Kind on host).
# Aliases: kind-up == local-up, kind-down == local-down, smoke == local-smoke.
make kind-up                                # ./local/kind/bootstrap.sh
make smoke                                  # ./local/kind/smoke.sh
make kind-down                              # ./local/kind/teardown.sh

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
         ├─ ReadTopology  (errgroup of 5 PromQL queries in parallel)
         ├─ ReadServiceGraph (1 PromQL, joined with topology)
         └─ assemble + graph.NewGraph → *Graph (immutable, with adjacency)
   (no in-process concurrency cap; HPA + Pod resource limits handle load shedding)
   ▼
graph.Project(g, scope)            ── filters + traversal applied here, NOT during build
   ▼
serialiseCytoscape / serialiseGrafanaNodeGraph
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
  future typed struct field**. `pod-calls-pod` edges carry a single
  `labels.cluster` (the trace source / client-side cluster, omitted when the
  client side is external). Cross-cluster status is derived by comparing the
  resolved source-node and target-node `labels.cluster` — D9.
- **Edge IDs are UUIDv5** with a fixed compiled-in namespace (`graph.edgeNamespace`)
  and the canonical input `<type>|<source>|<target>`. Stable across rebuilds —
  required for golden tests. Bumping the namespace UUID is a v2 break.
- **Cluster-scoped IDs everywhere.** Pods: `<cluster>/<uid>`, K8s nodes:
  `<cluster>/<node>`, PVCs: `<cluster>/<namespace>/<claim>`, externals:
  `external/<value>`. Node names are not globally unique without the prefix.
- **External-endpoint substitution rule** (`KSG_EXTERNAL_NAME_PATTERN`): when set
  and the substring matches the upstream `client` or `server` label, that
  endpoint becomes a `type="external"` node with `id="external/<value>"` and
  the verbatim label as `name`. Per-endpoint independent — both sides of a
  single edge can be evaluated separately. Edge `type` stays `pod-calls-pod`.
  When the client side is external, the edge `labels.cluster` is omitted.
- **Missing pod-UID human-label fallback** (D27, always on): when
  `client_k8s_pod_uid` or `server_k8s_pod_uid` is empty AND the corresponding
  `client`/`server` label is non-empty, that endpoint is promoted to
  `external/<label>` (no cluster prefix; `labels={}`, no `pattern` key)
  instead of dropping the edge. Per-endpoint resolution order:
  (1) `KSG_EXTERNAL_NAME_PATTERN` match → external with `labels.pattern`;
  (2) UID-based pod resolution / synth-pod fallback (only when UID is non-empty);
  (3) missing-UID human-label fallback (this rule); (4) drop (both UID and label
  empty). Edge `labels.cluster` rules are unchanged — omitted whenever the
  client side resolves to an external node, whether via pattern or via this
  fallback. The same `external/<label>` ID dedupes against pattern-matched
  externals in the externals map.
- **Server-side pod resolution** uses `Topology.PodsByUID` — a global pod-UID
  index built from all loaded clusters. Service-graph metrics carry only the
  trace-source `cluster` (client side); the server side's cluster is recovered
  by looking up `server_k8s_pod_uid` against this index, since K8s pod UIDs
  are unique cross-cluster in practice. Missing UIDs (with non-empty server
  label) follow the missing-UID fallback above; UIDs present but unknown
  to topology become synth pods with `cluster=""` (server-side cluster
  unknown).
- **No filters pushed to PromQL.** Each build loads every cluster present in upstream VictoriaMetrics. Caller-supplied filters (`cluster`, `namespace`, `edge_type`, `name`, traversal) are applied at projection time over the freshly built `*Graph`. Bounded query cost is delegated to upstream VictoriaMetrics search limits.
- **`/v1/edge-types` reads from `graph.EdgeTypes` only** — a single in-code
  registry shared with the builder. Adding an edge type = update both the
  builder and the registry in the same change; the API can never list a type
  the builder cannot produce.
- **API-key auth is the only HTTP auth in v1.** Header is `X-API-Key`. Keys
  come from `--api-keys-file` (K8s `Secret` mount, hot-reloaded) or
  `--api-keys`. Empty keyset = auth disabled (dev default). Open paths
  (no key required): `/livez`, `/readyz`, `/metrics`, `/openapi.*`, `/docs`,
  `/docs/assets/*`. Validation is constant-time and iterates the whole set —
  do NOT add early-return optimisations to `auth.KeySet.Validate`. Logs must
  never include the presented key value.
- **Deterministic response body.** The serialiser produces byte-identical output for the same `(window, filters, upstream-data)`: node/edge slices MUST go through `graph.SortNodes`/`SortEdges`, `Graph.ClusterNames()` MUST sort, and the response body MUST NOT carry time-of-build or echo-of-input fields. Body shape is fixed at `{apiVersion, clusters, elements}`. Don't add timestamps, random IDs, or unsorted map iteration to the response — golden tests will break.
- **IP addresses live on the typed `ipaddress` attribute, never in `labels`.** `PodNode.IPAddress()` carries `[pod_ip]` from `kube_pod_info` (when present). `K8sNode.IPAddress()` carries `[external_ip]` from `kube_node_status_addresses{type="ExternalIP"}` (when present). `PVCNode` and `ExternalNode` always return nil. `host_ip` from `kube_pod_info` is intentionally dropped — it is the node's IP, surfaced via the node entry instead. The serialiser emits `data.ipaddress` (with `omitempty`); `labels.pod_ip`, `labels.host_ip`, and `labels.external_ip` MUST NOT appear.
- **OTLP tracing/logging is config'd by OTel env vars only** (`OTEL_EXPORTER_OTLP_*`, `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER`). No bespoke `--otlp-*` flags. Telemetry defaults to no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset (zero export overhead, no background goroutines). Tracing MUST NOT alter response bodies — resource attrs and span IDs live on spans, never in JSON. `otelgin` is mounted on `/v1/*` only; `/livez`, `/readyz`, `/metrics`, and `/docs/*` are deliberately untraced. The auth middleware MUST NEVER log or attribute the presented `X-API-Key` value via either the local handler or the OTLP slog bridge.
- **Upstream metric-name prefix is an additive `KSG_METRIC_PREFIX` knob** applied to KSM-shaped series only (`kube_pod_info`, `kube_node_info`, `kube_node_status_addresses`, `kube_pod_spec_volumes_persistentvolumeclaims_info`, `kube_node_labels`, and the `kube_node_info`-backed cluster-discovery query). The prefix is prepended verbatim — trailing underscore is the operator's responsibility. NOT applied to `traces_service_graph_request_total` (different exporter family — Alloy/Tempo) or `up{}` (Prometheus-native). The metric-name suffix and the label-name set per series are a fixed contract any compatible exporter MUST honour; see design.md D26 and `docs/operations.md` § "Exporter compatibility contract". Threaded via `promql.Renderer{Prefix}` held on `build.Builder` and `api.Server`; the `Query` string constants remain bare so `query=` / `query_name=` dimensions on self-metrics and spans stay stable across deployments that differ only by prefix.

### Sealed graph types

`graph.GraphNode` is a sealed interface (`isGraphNode()` unexported). Concrete
types: `PodNode`, `K8sNode`, `PVCNode`, `ExternalNode`. All four expose
`ID()`, `Name()`, `Type()`, `Labels()`, `IPAddress()`. Serialisation goes
through these methods — never through type switches in the serialiser.
`IPAddress()` returns nil for `PVCNode` / `ExternalNode`; `PodNode` returns
`[pod_ip]` when known; `K8sNode` returns `[external_ip]` when known.

### Test stack layers

Boundary rule: **unit tests must not contact a real upstream service**. Anything
that needs a TCP socket fronting upstream is integration. Unit tests substitute
upstream behind small interfaces (`promql.Querier`, `auth.Validator`,
`clock.Clock`) using mockery-generated mocks under `internal/<pkg>/mocks/`.

| Layer | Where | Real I/O? |
|---|---|---|
| Unit | `internal/{graph,build,promql,config,clock,auth,telemetry}/*_test.go` | None — pure functions: parsers, joins, projection, edge IDs, KeySet, Clock. |
| Component | `internal/api/*_test.go` | None — gin handlers driven via a `MockQuerier` injected through `promql.Querier`; `httptest.NewServer` only wraps the server-under-test, never fakes upstream. Test helpers in `internal/api/helpers_test.go` (`newServerWithMocks`, `newMockQuerier`, `newErrQuerier`, `vec`). |
| Golden | `internal/api/golden_test.go` + `testdata/golden/*.json` | None. Wire-format snapshots; run with `-update` to refresh. |
| Property | `internal/graph/property_test.go` | None. Random multi-cluster graphs → invariants (orphan edges, traversal depth, ID uniqueness). |
| Integration | `internal/integration/*` | **Docker required.** testcontainers-go VictoriaMetrics suite; gated `SkipIfDockerUnavailable` — skips locally without Docker, runs full on CI (ubuntu-latest). Inject hooks into the in-process API via `StartAPIServer(cfg, WithClock(...))`. |
| Manual rig | `local/kind/smoke.sh` | Kind cluster + curl checks (kube-state-metrics + VM + API + Grafana); not executed by CI. |

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
  to an existing metric is a contract change; coordinate with `docs/operations.md`.
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
