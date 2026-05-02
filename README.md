# kube-state-graph

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
- Caches per time bucket with Ristretto + singleflight + ETag so concurrent
  users sharing a dashboard cost the upstream a single fan-out.

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

## Configuration

| Flag                            | Env                              | Default              | Notes |
|---------------------------------|----------------------------------|----------------------|-------|
| `--prom-url`                    | `KSG_PROM_URL`                   | `http://localhost:8428` | VictoriaMetrics Prometheus-compatible endpoint. |
| `--listen-addr`                 | `KSG_LISTEN_ADDR`                | `:8080`              | HTTP listen address. |
| `--max-window`                  | `KSG_MAX_WINDOW`                 | `24h`                | Maximum allowed `end - start`. |
| `--max-skew`                    | `KSG_MAX_SKEW`                   | `1m`                 | Maximum `end - now`. |
| `--max-pods`                    | `KSG_MAX_PODS`                   | `5000`               | Cluster-too-large ceiling. |
| `--build-timeout`               | `KSG_BUILD_TIMEOUT`              | `15s`                | Per-build context timeout. |
| `--build-concurrency`           | `KSG_BUILD_CONCURRENCY`          | `8`                  | Max in-flight builds. |
| `--cluster-discovery-lookback`  | `KSG_CLUSTER_DISCOVERY_LOOKBACK` | `1h`                 | Cluster discovery lookback. |
| `--clusters-allowlist`          | `KSG_CLUSTERS_ALLOWLIST`         | (empty)              | Comma-separated allowlist. |
| `--external-name-pattern`       | `KSG_EXTERNAL_NAME_PATTERN`      | (empty)              | Substring; when matched on `client`/`server`, that endpoint becomes an `external` node. |
| `--cache-max-cost-bytes`        | `KSG_CACHE_MAX_COST_BYTES`       | `268435456` (256 MiB)| Ristretto budget. |
| `--enable-debug`                | `KSG_ENABLE_DEBUG`               | `false`              | Enable `/debug/*` endpoints. |
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
