# API reference

All routes are prefixed `/v1/`. Every JSON body carries `apiVersion: "v1"`.

## Authentication

When the server is started with API keys configured (`--api-keys-file=<path>`
or `--api-keys=<csv>`), every request to `/v1/*` and `/debug/*` MUST carry an
`X-API-Key: <key>` header.

```bash
curl -H 'X-API-Key: my-secret-key' \
  'http://localhost:8080/v1/graph?start=2026-05-01T12:00:00Z&end=2026-05-01T12:05:00Z'
```

- Missing header or unrecognised key → `401 Unauthorized` with body
  `{"error":{"reason":"unauthorized","message":"…"}}`. The server increments
  `kube_state_graph_auth_rejected_total{reason="missing|invalid"}`.
- Open routes that **never** require a key:
  `/livez`, `/readyz`, `/metrics`, `/openapi.yaml`, `/openapi.json`, `/docs`,
  `/docs/assets/*`. Health probes and Prometheus scrapes work unauthenticated;
  `/metrics` should be gated by `NetworkPolicy` or a separate listen address
  in production.
- When the server is started with **no** keys configured (both flags empty),
  the middleware is a no-op and every route accepts requests without a key. A
  startup log line warns that auth is disabled.
- File-backed keys (`--api-keys-file`, one key per line, `#` comments allowed)
  are re-read every `--api-keys-reload-interval` (default `30s`). A
  Kubernetes `Secret` rotation propagates without a Pod restart; combined
  end-to-end latency is roughly `kubelet sync (~60s) + reload-interval (30s)`.

## `GET /v1/graph`

Returns the multi-cluster graph for `[start, end]` in Cytoscape.js shape.

### Query parameters

| Param        | Required | Repeatable | Description |
|--------------|----------|------------|-------------|
| `start`      | yes      | no         | RFC 3339 or Unix seconds. |
| `end`        | yes      | no         | Same. Must be `> start` and `<= now + --max-skew`. |
| `cluster`    | no       | yes        | Restrict to clusters whose label matches. |
| `namespace`  | no       | yes        | Restrict pods/PVCs by namespace. |
| `edge_type`  | no       | yes        | Restrict edges by type. One of `pod-runs-on-node`, `pod-mounts-pvc`, `pod-calls-pod`. Unknown types ⇒ silently empty. |
| `pod`        | no       | yes        | Restrict to pods whose name matches (exact). Pod names are not unique across clusters; all matches are returned. Combine with `cluster` / `namespace` to disambiguate. |
| `root`       | no       | no         | Cluster-scoped node ID to anchor a traversal. |
| `depth`      | no       | no         | Traversal depth (0..6, default 2 when `root` is set). |
| `direction`  | no       | no         | `in | out | both` (default `both`). |

Multiple values for the same parameter are OR-combined; different parameters
are AND-combined. Unknown values yield 200 + empty result, never an error.

When `pod` is set, non-pod node types (`node`, `pvc`, `external`) survive only
as edge endpoints of an in-scope pod (so `pod-runs-on-node`, `pod-mounts-pvc`,
and external `pod-calls-pod` edges remain visible). The cross-cluster
partner-rehydration rule that re-adds the out-of-scope endpoint of a
`pod-calls-pod` edge under a `cluster` filter is **suppressed** when `pod` is
set — the caller has named an exact pod set, so partner pods outside that set
are not auto-included.

### Response shape

```jsonc
{
  "apiVersion": "v1",
  "start": "2026-05-01T12:00:00Z",
  "end":   "2026-05-01T12:05:00Z",
  "start_actual": "...", "end_actual": "...",
  "bucket_seconds": 60,
  "clusters": ["cluster-alpha", "cluster-beta"],
  "elements": {
    "nodes": [{"data": {"id": "<cluster>/<uid>", "name": "...", "type": "pod|node|pvc|external", "labels": {"cluster": "..."}}}],
    "edges": [{"data": {"id": "<uuidv5>", "type": "...", "source": "...", "target": "...", "labels": {"cluster": "<trace-source-cluster>"}}}]
  }
}
```

`labels` is strictly `map[string]string`. Numeric metrics are deferred to a
future typed struct field (see the design doc, D9).

### Headers

- `ETag: "<sha256-of-body>"`.
- `If-None-Match` ⇒ `304 Not Modified`.

`/v1/graph` and `/v1/graph/nodegraph` do **not** emit `Cache-Control` in v1 — there is no in-process result cache to advertise. Repeated identical requests return the same `ETag`, so clients save bandwidth via `If-None-Match` revalidation. A future cache mechanism for distributed deployment will reintroduce stronger caching headers.

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
| 401    | `unauthorized`          | Missing or invalid `X-API-Key` (only when API key auth is configured). |
| 503    | `capacity`              | Build concurrency exhausted (`Retry-After: 1`). |
| 503    | `timeout`               | Build exceeded `--build-timeout`. |
| 503    | `cluster_too_large`     | `count(kube_pod_info) > --max-pods`. |
| 502    | `upstream`              | Generic upstream failure. |

## `GET /v1/graph/nodegraph`

Same query parameters and ETag semantics as `/v1/graph`. Returns Grafana Node Graph
datasource shape with `nodes_fields` / `nodes` / `edges_fields` / `edges`
arrays. The serialiser maps:

- node `name` → `title`.
- node `labels.cluster · labels.namespace` → `subTitle`.
- node `type` → `mainStat`.
- edge `type` → `mainStat`.

## `GET /v1/clusters`

Returns the list of clusters seen in `kube_node_info` over
`--cluster-discovery-lookback`. Each request hits VictoriaMetrics directly; clients revalidate via the response `ETag`. Intersected with `--clusters-allowlist` when configured.

## `GET /v1/edge-types`

Static catalogue. Long `Cache-Control: max-age=3600` and registry-hash
`ETag`. Honours `If-None-Match`.

## `GET /livez`, `GET /readyz`

`/livez` always returns 200 while the process runs. `/readyz` runs a 1 s
`up{}` probe and returns 200 only if the upstream answers.

## `GET /metrics`

Prometheus exposition with `kube_state_graph_*` self-metrics.

## `GET /debug/last-queries`

Debug endpoint behind `--enable-debug`. Returns the raw upstream query strings of the most recent build. v1 does not expose a cache-flush endpoint because there is no result cache.
