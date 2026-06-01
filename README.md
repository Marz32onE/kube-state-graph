# kube-state-graph

Traditional Chinese: [README.zh-tw.md](README.zh-tw.md).

A Go REST API server that returns a unified pod / node / PVC graph for one or
more Kubernetes clusters, including pod-UID-resolved RPC edges that may cross
cluster boundaries.

```
cluster A: kube-state-metrics ──┐
           service-graph source ┤
                                 │  (vmagent / Prometheus
cluster B: kube-state-metrics ──┤   with external_labels:
           service-graph source ┤   { cluster: "<name>" })
                                 │
       ...                       ├──► centralised VictoriaMetrics ◄── kube-state-graph
                                 │                                     (Prometheus HTTP API)
cluster N: kube-state-metrics ──┤
           service-graph source ─┘
```

## What it does

- Reads `kube_*` topology and `traces_service_graph_*` runtime metrics from a
  single centralised VictoriaMetrics, on demand for a caller-specified
  `[start, end]` time range.
- Joins them into a multi-cluster graph keyed by cluster-scoped pod UIDs and
  node names.
- Returns the graph as Cytoscape.js JSON (`/v1/graph`).
- Exposes cluster discovery (`/v1/clusters`) and a static edge-type catalogue
  (`/v1/edge-types`).
- Builds the graph on every request — v1 ships **no in-process result cache**,
  **no singleflight**, and **no HTTP cache validators** (`ETag` /
  `If-None-Match` / `304`). A horizontally scalable cache mechanism for
  distributed deployment is anticipated as a future change. Caller-supplied
  `start` / `end` accept RFC 3339 or Unix seconds; the server enforces only
  `end > start`, then passes the window through to upstream PromQL verbatim —
  no server-side bucketing, alignment, max-window cap, or future-time guard.
  Bounded query cost is delegated to VictoriaMetrics search limits
  (`-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`,
  `-search.maxSamplesPerQuery`). The serialiser produces a deterministic body
  (`apiVersion`, `clusters`, `elements` only — no echoed time fields). Pod,
  node, and service IPs appear on the top-level `ipaddress` attribute, not in
  `labels`.

## Quick start

```bash
make build
./bin/kube-state-graph \
  --prom-url=http://victoria-metrics.example:8428 \
  --listen-addr=:8080
```

Then:

```bash
curl 'http://localhost:8080/v1/clusters'
curl 'http://localhost:8080/v1/graph?start=$(date -u -d "-5 min" +%s)&end=$(date -u +%s)' | jq '.elements'
```

When the server is started with API keys configured (`--api-keys-file` or
`--api-keys`), every `/v1/*` request must carry an `X-API-Key: <key>` header:

```bash
curl -H 'X-API-Key: my-secret-key' 'http://localhost:8080/v1/clusters'
```

Health probes (`/livez`, `/readyz`), `/metrics`, and the docs routes
(`/openapi.*`, `/docs`) are exempt and require no key. With no keys configured
the middleware is a no-op and every route is open.

## Upstream metrics consumed

The graph build issues these PromQL queries against centralised VictoriaMetrics
on every request (v1 has no result cache). Every series is expected to carry a
`cluster` external label (injected by `vmagent` / Prometheus `external_labels`
per source cluster).

### Topology metrics — produced by [`kube-state-metrics`](https://github.com/kubernetes/kube-state-metrics)

| Metric | Used for | Labels read | Required? |
|---|---|---|---|
| `kube_pod_info` | Pod nodes (`node` label drives Cytoscape `cluster > node > pod` compound nesting) | `cluster`, `namespace`, `pod`, `uid`, `node`, `pod_ip` (→ `data.ipaddress`; `host_ip` not exported) | **Yes** |
| `kube_node_info` | K8sNode nodes | `cluster`, `node` | **Yes** |
| `kube_node_status_addresses{type="ExternalIP"}` | Node external IP (→ `data.ipaddress`) | `cluster`, `node`, `address` | Optional |
| `kube_node_labels` | Node label propagation (`kubernetes.io/*` etc.) | `cluster`, `node`, `label_*` | Optional |
| `kube_pod_spec_volumes_persistentvolumeclaims_info` | PVC nodes; pod-mounts-pvc edges | `cluster`, `namespace`, `pod`, `persistentvolumeclaim`, `volume` | Optional (no PVCs ⇒ no PVC nodes/edges) |

Each is wrapped in `last_over_time(<metric>[<window>]) @ <end>` so the result
reflects the most recent value within the requested `[start, end]` window.

### Service-graph metric — produced by [Tempo](https://grafana.com/docs/tempo/latest/metrics-generator/service_graphs/) or compatible generator

| Metric | Used for | Labels read | Required? |
|---|---|---|---|
| `traces_service_graph_request_total` | `pod-calls-pod` edges (intra- and cross-cluster) | `cluster`, `client`, `server`, `client_k8s_pod_uid`, `server_k8s_pod_uid` | Optional (no series ⇒ no call edges) |

Wrapped in `rate(traces_service_graph_request_total[<window>]) @ <end>`. Each
series carries a single `cluster` external label representing the trace source
(typically the cluster running Tempo's metrics-generator); this is the
**client-side** cluster of the call. The **server-side** cluster is recovered
at build time by joining `server_k8s_pod_uid` against the global topology
pod-UID index — Kubernetes pod UIDs are unique across clusters in practice,
so the lookup is unambiguous. Edges are only emitted when both endpoints
resolve. When an endpoint's pod-UID label is empty, the human-readable
`client`/`server` label is resolved by built-in **connection-string detection**
(no knob): a label containing the literal `://` is parsed as a URL — an
in-cluster `<service>.<namespace>.svc` name becomes a `type="service"` node
(with on-demand `service-selects-pod` edges to its backing pods), a headless
`<pod>.<service>.<namespace>.svc` name resolves to the real backing pod, and an
unresolvable URL becomes an `others` node. A non-URL label (no `://`) becomes
an `external` node via the missing pod-UID human-label fallback.

The `servicegraph` connector's **virtual peers** — `client="user"` (an
uninstrumented caller) and `unknown` (an unresolved peer) — are dropped at the
query layer (`client!~"user|unknown",server!~"user|unknown"`) and never appear
as nodes or edges. The match is exact and case-sensitive, so a `://` host that
merely *contains* `user` is unaffected.

### Probes — diagnostics, not graph data

| PromQL | Purpose |
|---|---|
| `group by (cluster) (last_over_time(kube_node_info[1h]))` | Powers `GET /v1/clusters` discovery |
| `up` | Distinguishes "no data in window" (`outside_retention`) from "upstream healthy but window empty" |

### Edge → metric mapping

| Edge type | Source metric(s) |
|---|---|
| `pod-mounts-pvc` | `kube_pod_spec_volumes_persistentvolumeclaims_info` |
| `pod-calls-pod` | `traces_service_graph_request_total` |
| `service-selects-pod` | `traces_service_graph_request_total` (connection-string resolution + `kube_endpointslice_*` join) |

### Multi-cluster and cross-cluster coverage

Cross-cluster paths and service-graph scenarios are covered by
`internal/integration/` tests against a `testcontainers-go` VictoriaMetrics
container. The suite spins up a real VictoriaMetrics, pushes hand-crafted
fixture series via `POST /api/v1/import/prometheus`, and drives the in-process
API — this is the sole verification path for multi-cluster, cross-cluster, and
service-graph behaviour.

## Configuration

| Flag                            | Env                              | Default              | Notes |
|---------------------------------|----------------------------------|----------------------|-------|
| `--prom-url`                    | `KSG_PROM_URL`                   | `http://localhost:8428` | VictoriaMetrics Prometheus-compatible endpoint. |
| `--listen-addr`                 | `KSG_LISTEN_ADDR`                | `:8080`              | HTTP listen address. |
| `--build-timeout`               | `KSG_BUILD_TIMEOUT`              | `15s`                | Per-build context timeout for `/v1/graph`. |
| `--api-timeout`                 | `KSG_API_TIMEOUT`                | `5s`                 | Per-request timeout for non-graph endpoints with upstream calls (`/v1/clusters`, `/readyz`). |
| `--api-keys-file`               | `KSG_API_KEYS_FILE`              | (empty)              | Path to a file holding accepted API keys (one per line, `#` comments allowed). Designed for K8s `Secret` mounts. Reloaded periodically. |
| `--api-keys`                    | `KSG_API_KEYS`                   | (empty)              | Comma-separated literal keys. Dev only; ignored when `--api-keys-file` is set. |
| `--api-keys-reload-interval`    | `KSG_API_KEYS_RELOAD_INTERVAL`   | `30s`                | How often `--api-keys-file` is re-read. Set to `0` to disable hot reload. |
| `--log-level`                   | `KSG_LOG_LEVEL`                  | `info`               | `debug | info | warn | error`. |
| `--metric-prefix`               | `KSG_METRIC_PREFIX`              | (empty)              | Additive prefix prepended to every kube-state-metrics-shaped series the topology reader queries (e.g. `o11y_` → `o11y_kube_pod_info`). Does **not** affect `traces_service_graph_request_total` or `up{}`. The metric-name suffix and per-series label set are a fixed contract any compatible exporter must honour. |

## Documentation

The full API reference is served by the running server:

- **Interactive API reference (Scalar UI):** [`/docs`](http://localhost:8080/docs)
- **OpenAPI 3.1 spec:** [`/openapi.yaml`](http://localhost:8080/openapi.yaml) · [`/openapi.json`](http://localhost:8080/openapi.json)

The spec is generated from in-source annotations (`make docs`) and embedded into
the binary, so it always matches the running build. The Scalar UI loads its
front-end bundle from the jsDelivr CDN.

## Development

### First-time setup

Run **once** after cloning. Bootstraps the dev environment, downloads modules,
and installs host-level tools (`golangci-lint`, `govulncheck`). Mockery is
tracked via go.mod's `tool` directive (Go 1.24+) and invoked through
`go tool mockery` — no separate install step is required.

```bash
make init           # go mod download + dev tools
make doctor         # verify toolchain (go, golangci-lint, govulncheck, mockery, docker)
make init-hooks     # (optional) install pre-commit hook (gofmt + go vet)
```

Required: Go 1.25+. The toolchain pinned in `go.mod` (currently `go1.26.3`)
will be auto-fetched by Go on first build.

### Day-to-day commands

```bash
make build          # compile binary
make test           # unit + component + golden + property + integration (Docker required)
make lint           # golangci-lint
make vuln           # govulncheck
make cover          # coverage profile
```

### Mocks (mockery)

Production-side dependencies are exposed as small interfaces (`promql.Querier`,
`auth.Validator`, `clock.Clock`) so unit tests can substitute mockery-generated
mocks instead of fronting real services with `httptest.NewServer`. Mocks live
under `internal/<pkg>/mocks/` and are committed to git so CI does not need
mockery installed.

```bash
make mocks          # regenerate mocks after editing an interface
make verify-mocks   # CI-style freshness check (regen + git diff)
```

`.mockery.yaml` lists the configured interfaces. After **adding or editing any
interface** registered there, run `make mocks` and commit the regenerated
files — the `mocks-drift` CI job blocks merges otherwise.

### Test layout

| Suite | Where | Real I/O? |
|---|---|---|
| Unit | `internal/{graph,build,promql,config,clock,auth,telemetry}/*_test.go` | None — pure Go. |
| Component | `internal/api/*_test.go` | None — `MockQuerier` injected via interface; `httptest.NewServer` only wraps the server-under-test, never fakes upstream. |
| Golden | `internal/api/golden_test.go` + `testdata/golden/*.json` | None. Run with `-update` to refresh snapshots. |
| Integration | `internal/integration/*` | **Docker required.** testcontainers-go spins a real VictoriaMetrics container; `SkipIfDockerUnavailable` skips locally without Docker. CI runs the full suite. |

The boundary between unit and integration is strict: anything that touches a
TCP socket fronting an upstream service is integration. Unit tests must run
with no external dependencies.

## License

Apache-2.0
