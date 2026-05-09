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
- Returns the graph as either Cytoscape.js JSON (`/v1/graph`) or Grafana Node
  Graph datasource shape (`/v1/graph/nodegraph`).
- Exposes cluster discovery (`/v1/clusters`) and a static edge-type catalogue
  (`/v1/edge-types`).
- Builds the graph on every request — v1 ships **no in-process result cache**
  and **no singleflight**. Responses carry an `ETag` (sha256 of the body) so
  clients may revalidate cheaply via `If-None-Match` and receive
  `304 Not Modified` when the body would be unchanged. A horizontally
  scalable cache mechanism for distributed deployment is anticipated as a
  future change. Caller-supplied `start` / `end` are passed through to
  upstream PromQL verbatim (after `--max-window` / `--max-skew` validation);
  there is no server-side bucketing or alignment. The response body carries
  only `apiVersion`, `clusters`, and `elements`.

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
`--api-keys`), every `/v1/*` and `/debug/*` request must carry an
`X-API-Key: <key>` header:

```bash
curl -H 'X-API-Key: my-secret-key' 'http://localhost:8080/v1/clusters'
```

Health probes (`/livez`, `/readyz`), `/metrics`, and the docs routes
(`/openapi.*`, `/docs`, `/docs/assets/*`) are exempt and require no key. With
no keys configured the middleware is a no-op and every route is open — see
[`docs/operations.md`](docs/operations.md) for the K8s `Secret` mount and
rotation procedure.

## Upstream metrics consumed

The graph build issues these PromQL queries against centralised VictoriaMetrics
on every request (v1 has no result cache). Every series is expected to carry a
`cluster` external label (injected by `vmagent` / Prometheus `external_labels`
per source cluster).

### Topology metrics — produced by [`kube-state-metrics`](https://github.com/kubernetes/kube-state-metrics)

| Metric | Used for | Labels read | Required? |
|---|---|---|---|
| `kube_pod_info` | Pod nodes; pod-runs-on-node edges | `cluster`, `namespace`, `pod`, `uid`, `node`, `pod_ip`, `host_ip` | **Yes** |
| `kube_node_info` | K8sNode nodes | `cluster`, `node` | **Yes** |
| `kube_node_status_addresses{type="ExternalIP"}` | Node `external_ip` label | `cluster`, `node`, `address` | Optional |
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
resolve (to a known pod UID, or to an `external` node when the upstream
`client`/`server` label matches `KSG_EXTERNAL_NAME_PATTERN`).

### Probes — diagnostics, not graph data

| PromQL | Purpose |
|---|---|
| `group by (cluster) (last_over_time(kube_node_info[1h]))` | Powers `GET /v1/clusters` discovery |
| `up` | Distinguishes "no data in window" (`outside_retention`) from "upstream healthy but window empty" |

### Edge → metric mapping

| Edge type | Source metric(s) |
|---|---|
| `pod-runs-on-node` | `kube_pod_info` (pod's `node` label) |
| `pod-mounts-pvc` | `kube_pod_spec_volumes_persistentvolumeclaims_info` |
| `pod-calls-pod` | `traces_service_graph_request_total` |

### Local rig coverage

The in-tree `local/kind/` rig scrapes a real Kind cluster via kube-state-metrics
(`kube_pod_info`, `kube_node_info`, `kube_node_labels`,
`kube_pod_spec_volumes_persistentvolumeclaims_info`) and produces
`traces_service_graph_request_total` locally via a Grafana Beyla DaemonSet that
auto-instruments pods in the `kube-state-graph` namespace and ships OTLP spans
to a Grafana Alloy Deployment. Alloy's `otelcol.connector.servicegraph`
(configured with `dimensions=["k8s.pod.uid"]`) promotes Beyla's per-pod resource
attribute to `client_k8s_pod_uid` + `server_k8s_pod_uid` and remote-writes to
VictoriaMetrics, so `pod-calls-pod` edges show up in the local rig's
`/v1/graph` response — driven by the existing in-cluster Go traffic
(`kube-state-graph → VictoriaMetrics → kube-state-metrics`, Grafana →
kube-state-graph, etc.) without any synthetic traffic generator. Cross-cluster
paths (which a single Kind cluster cannot demonstrate) remain covered by
`internal/integration/` tests against a `testcontainers-go` VictoriaMetrics
container.

## Configuration

| Flag                            | Env                              | Default              | Notes |
|---------------------------------|----------------------------------|----------------------|-------|
| `--prom-url`                    | `KSG_PROM_URL`                   | `http://localhost:8428` | VictoriaMetrics Prometheus-compatible endpoint. |
| `--listen-addr`                 | `KSG_LISTEN_ADDR`                | `:8080`              | HTTP listen address. |
| `--build-timeout`               | `KSG_BUILD_TIMEOUT`              | `15s`                | Per-build context timeout for `/v1/graph` + `/v1/graph/nodegraph`. |
| `--api-timeout`                 | `KSG_API_TIMEOUT`                | `5s`                 | Per-request timeout for non-graph endpoints with upstream calls (`/v1/clusters`, `/readyz`). |
| `--external-name-pattern`       | `KSG_EXTERNAL_NAME_PATTERN`      | (empty)              | Substring; when matched on `client`/`server`, that endpoint becomes an `external` node. |
| `--api-keys-file`               | `KSG_API_KEYS_FILE`              | (empty)              | Path to a file holding accepted API keys (one per line, `#` comments allowed). Designed for K8s `Secret` mounts. Reloaded periodically. |
| `--api-keys`                    | `KSG_API_KEYS`                   | (empty)              | Comma-separated literal keys. Dev only; ignored when `--api-keys-file` is set. |
| `--api-keys-reload-interval`    | `KSG_API_KEYS_RELOAD_INTERVAL`   | `30s`                | How often `--api-keys-file` is re-read. Set to `0` to disable hot reload. |
| `--log-level`                   | `KSG_LOG_LEVEL`                  | `info`               | `debug | info | warn | error`. |

## Documentation

- [API reference](docs/api.md)
- [Multi-cluster setup](docs/multi-cluster.md)
- [External-name substitution](docs/external-substitution.md)
- [Operations](docs/operations.md)

## Development

```bash
make build       # compile binary
make test        # unit + component + golden + property
make lint        # golangci-lint
make kind-up     # bootstrap integration cluster
make smoke       # run smoke test against running harness
make kind-down   # tear down
```

## License

Apache-2.0
