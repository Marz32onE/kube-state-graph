# API reference

All routes are prefixed `/v1/`. Every JSON body carries `apiVersion: "v1"`.

## `GET /v1/graph`

Returns the multi-cluster graph for `[start, end]` in Cytoscape.js shape.

### Query parameters

| Param        | Required | Repeatable | Description |
|--------------|----------|------------|-------------|
| `start`      | yes      | no         | RFC 3339 or Unix seconds. |
| `end`        | yes      | no         | Same. Must be `> start` and `<= now + --max-skew`. |
| `cluster`    | no       | yes        | Restrict to clusters whose label matches. |
| `namespace`  | no       | yes        | Restrict pods/PVCs by namespace. |
| `node`       | no       | yes        | Restrict pods/nodes by K8s node name. |
| `edge_type`  | no       | yes        | Restrict edges by type. Unknown types ⇒ silently empty. |
| `root`       | no       | no         | Cluster-scoped node ID to anchor a traversal. |
| `depth`      | no       | no         | Traversal depth (0..6, default 2 when `root` is set). |
| `direction`  | no       | no         | `in | out | both` (default `both`). |

### Response shape

```jsonc
{
  "apiVersion": "v1",
  "start": "2026-05-01T12:00:00Z",
  "end":   "2026-05-01T12:05:00Z",
  "start_actual": "...", "end_actual": "...",
  "bucket_seconds": 15,
  "built_at": "...",
  "clusters": ["cluster-alpha", "cluster-beta"],
  "elements": {
    "nodes": [{"data": {"id": "<cluster>/<uid>", "name": "...", "type": "pod|node|pvc|external", "labels": {"cluster": "..."}}}],
    "edges": [{"data": {"id": "<uuidv5>", "type": "...", "source": "...", "target": "...", "labels": {"client_cluster": "...", "server_cluster": "..."}}}]
  }
}
```

`labels` is strictly `map[string]string`. Numeric metrics are deferred to a
future typed struct field (see the design doc, D9).

### Headers

- `Cache-Control: public, max-age=<n>` (n derived from time class).
- `ETag: "<sha256-of-body>"`.
- `X-Cache: HIT | MISS | COALESCED`.
- `If-None-Match` ⇒ `304 Not Modified`.

### Status codes / `reason`

| Status | `reason`                | Description |
|--------|-------------------------|-------------|
| 400    | `missing_start`         | `start` missing. |
| 400    | `missing_end`           | `end` missing. |
| 400    | `invalid_start`/`invalid_end` | Failed to parse timestamp. |
| 400    | `invalid_range`         | `end <= start`. |
| 400    | `window_too_large`      | `end - start > --max-window`. |
| 400    | `end_in_future`         | `end > now + --max-skew`. |
| 400    | `depth_too_large`       | `depth > 6`. |
| 400    | `outside_retention`     | Empty topology and healthy upstream. |
| 503    | `capacity`              | Build concurrency exhausted (`Retry-After: 1`). |
| 503    | `timeout`               | Build exceeded `--build-timeout`. |
| 503    | `cluster_too_large`     | `count(kube_pod_info) > --max-pods`. |
| 502    | `upstream`              | Generic upstream failure. |

## `GET /v1/graph/nodegraph`

Same query parameters and caching as `/v1/graph`. Returns Grafana Node Graph
datasource shape with `nodes_fields` / `nodes` / `edges_fields` / `edges`
arrays. The serialiser maps:

- node `name` → `title`.
- node `labels.cluster · labels.namespace` → `subTitle`.
- node `type` → `mainStat`.
- edge `type` → `mainStat`.

## `GET /v1/clusters`

Returns the list of clusters seen in `kube_node_info` over
`--cluster-discovery-lookback`. Cached internally for 60 s. Intersected with
`--clusters-allowlist` when configured.

## `GET /v1/edge-types`

Static catalogue. Long `Cache-Control: max-age=3600` and registry-hash
`ETag`. Honours `If-None-Match`.

## `GET /livez`, `GET /readyz`

`/livez` always returns 200 while the process runs. `/readyz` runs a 1 s
`up{}` probe and returns 200 only if the upstream answers.

## `GET /metrics`

Prometheus exposition with `kube_state_graph_*` self-metrics.

## `DELETE /admin/cache`, `GET /debug/last-queries`

Admin / debug endpoints (debug behind `--enable-debug`).
