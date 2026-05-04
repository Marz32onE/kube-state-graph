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
# Build / test loop
make build                                  # ./bin/kube-state-graph
make test                                   # go test ./... -count=1 -race -shuffle=on
make vet                                    # go vet
make lint                                   # golangci-lint (must be installed)
make cover                                  # go test ./... -coverprofile=coverage.out

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

Module path: `github.com/marz32one/kube-state-graph`. Minimum Go 1.25 (`go.mod`); build toolchain pinned to `go1.26.2` via the `toolchain` directive.

## Architecture (the 90 % you need to know)

### Request lifecycle

```
HTTP /v1/graph?start=&end=&...
   │
   ▼
parseGraphRequest        ── cache.Bucket(start, end, now) → (StartActual, EndActual, BucketSeconds, TTL)
   │                        cache.Key(bucket) → uint64   (time-only key)
   ▼
Orchestrator.Resolve(ctx, key, bucket)
   ├─ cache.Get(key)               ── HIT  → return *Graph
   └─ singleflight.Do(key, …)
         ├─ semaphore.TryAcquire   ── overflow → 503 capacity
         ├─ context.WithTimeout    ── exceeded → 503 timeout
         ├─ Builder.Build(ctx, window, end)
         │     ├─ probeClusterSize        ── overflow → 503 cluster_too_large
         │     ├─ ReadTopology  (errgroup of 5 PromQL queries in parallel)
         │     ├─ ReadServiceGraph (1 PromQL, joined with topology)
         │     └─ assemble + graph.NewGraph → *Graph (immutable, with adjacency)
         └─ cache.Set(key, *Graph, cost, TTL)
   ▼
graph.Project(g, scope)            ── filters + traversal applied here, NOT during build
   ▼
serialiseCytoscape / serialiseGrafanaNodeGraph
   ▼
ETag = sha256(body); Cache-Control = public, max-age=<from time class>
```

### Load-bearing design rules

These are non-obvious; read `openspec/changes/add-k8s-pod-graph-api/design.md`
(D1–D19) before changing any of them.

- **Cache key is time-only** (`start_bucket, end_bucket, bucket_size`). Filters
  (`cluster`, `namespace`, `node`, `edge_type`, traversal) are applied at
  response time over the cached `*Graph`. Adding filters to the key would
  fragment the cache catastrophically — D5/D7.
- **Time-bucketing classes**: bucket grid is **uniformly 60 s** for every class;
  TTL still varies — `live` (30 s), `recent` (5 m), `historical` (1 h), `frozen`
  (24 h). `start` is floored and `end` is **ceiled** to the 60 s grid (so a
  request for `end=12:19` covers 12:17 in its 12:00→12:20 window). When
  `ceil(end, 60s) > now`, `end_actual` is clamped to `floor(now, 60s)`;
  callers receive `start_actual`/`end_actual` so they know what window they got.
- **`labels` is strict `map[string]string`** on both nodes and edges. No bools,
  no numbers, no string-encoded numbers. Numeric edge metrics (`rate`, `p99_ms`,
  `error_rate`) and boolean flags (`cross_cluster`, `ghost`) are **deferred to a
  future typed struct field**. `pod-calls-pod` edges carry a single
  `labels.cluster` (the trace source / client-side cluster, omitted when the
  client side is external). Cross-cluster status is derived by comparing the
  resolved source-node and target-node `labels.cluster` — D9.
- **Edge IDs are UUIDv5** with a fixed compiled-in namespace (`graph.edgeNamespace`)
  and the canonical input `<type>|<source>|<target>`. Stable across rebuilds —
  required for golden tests and HTTP `ETag` reproducibility. Bumping the
  namespace UUID is a v2 break.
- **Cluster-scoped IDs everywhere.** Pods: `<cluster>/<uid>`, K8s nodes:
  `<cluster>/<node>`, PVCs: `<cluster>/<namespace>/<claim>`, externals:
  `external/<value>`. Node names are not globally unique without the prefix.
- **External-endpoint substitution rule** (`KSG_EXTERNAL_NAME_PATTERN`): when set
  and the substring matches the upstream `client` or `server` label, that
  endpoint becomes a `type="external"` node with `id="external/<value>"` and
  the verbatim label as `name`. Per-endpoint independent — both sides of a
  single edge can be evaluated separately. Edge `type` stays `pod-calls-pod`.
  When the client side is external, the edge `labels.cluster` is omitted.
- **Server-side pod resolution** uses `Topology.PodsByUID` — a global pod-UID
  index built from all loaded clusters. Service-graph metrics carry only the
  trace-source `cluster` (client side); the server side's cluster is recovered
  by looking up `server_k8s_pod_uid` against this index, since K8s pod UIDs
  are unique cross-cluster in practice. Missing UIDs become synth pods with
  `cluster=""` (server-side cluster unknown).
- **Allowlist injection** is the only filter pushed to PromQL. `--clusters-allowlist`
  injects `{cluster=~"a|b|c"}` into every query, including service-graph
  queries (server-side cluster filtering is not pushed to PromQL because the
  metric does not carry server-side cluster; cross-cluster edges whose target
  pod lives in a non-allowlisted cluster drop silently when the target
  topology is not loaded). All caller-supplied filters are projection-time only.
- **Ristretto async-write race with singleflight**: the `singleflight.Do`
  callback returns the *built `*Graph` value*, not a "go re-read the cache"
  signal. Waiters see the same `*Graph`. We additionally call `cache.Wait()`
  inside `cache.Set` so the entry is visible to the *next* request. Don't
  rewrite this without re-reading D6.
- **`/v1/edge-types` reads from `graph.EdgeTypes` only** — a single in-code
  registry shared with the builder. Adding an edge type = update both the
  builder and the registry in the same change; the API can never list a type
  the builder cannot produce.

### Sealed graph types

`graph.GraphNode` is a sealed interface (`isGraphNode()` unexported). Concrete
types: `PodNode`, `K8sNode`, `PVCNode`, `ExternalNode`. All four expose
`ID()`, `Name()`, `Type()`, `Labels()`. Serialisation goes through these
methods — never through type switches in the serialiser.

### Test stack layers

| Layer | Where | What it covers |
|---|---|---|
| Unit | `internal/{graph,cache,build,promql,config}/*_test.go` | Pure functions: parsers, joins, projection, cache key, edge IDs. |
| Component | `internal/api/server_test.go` | Gin handlers against a `httptest.Server` mocking the Prometheus HTTP API. |
| Golden | `internal/api/golden_test.go` + `testdata/golden/*.json` | Wire-format snapshots; run with `-update` to refresh. |
| Property | `internal/graph/property_test.go` | Random multi-cluster graphs → invariants (orphan edges, traversal depth, ID uniqueness). |
| Integration | `internal/integration/*` | testcontainers-go VictoriaMetrics suite; gated `SkipIfDockerUnavailable` — skips locally without Docker, runs full on CI (ubuntu-latest). |
| Manual rig | `local/kind/smoke.sh` | curl checks against the kind-based local rig (kube-state-metrics + VM + API + Grafana); not executed by CI. |

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
  client_golang, Ristretto v2, google/uuid, cespare/xxhash v2, golang.org/x/sync,
  testify v1.10.0 (test-only),
  testcontainers-go (integration test-only), swaggo/swag/v2 (codegen tool, not
  imported at runtime). Adding more requires a design-doc note.
