# API reference

All routes are prefixed `/v1/`. Every JSON body carries `apiVersion: "v1"`.

## Tracing

Inbound W3C `traceparent` and `tracestate` headers are honoured on every `/v1/*` request — the server span chains under the caller's trace when tracing is enabled. Outbound PromQL HTTP calls inject `traceparent` automatically.

Response bodies are unaffected by tracing state: enabling or disabling the OTLP pipeline does not change the byte-level response. See `docs/operations.md` ("OpenTelemetry tracing and logging") for the env-var configuration surface and span attribute reference.

## Authentication

When the server is started with API keys configured (`--api-keys-file=<path>`
or `--api-keys=<csv>`), every request to `/v1/*` MUST carry an
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
| `end`        | yes      | no         | Same. Must be `> start`. |
| `cluster`    | no       | yes        | Restrict to clusters whose label matches. |
| `namespace`  | no       | yes        | Restrict pods/PVCs by namespace. |
| `edge_type`  | no       | yes        | Restrict edges by type. One of `pod-mounts-pvc`, `pod-calls-pod`, `service-selects-pod`. Unknown types ⇒ silently empty. |
| `name`       | no       | yes        | Restrict to nodes whose name matches exactly **across every node type** (pod, K8s node, PVC, external). Use it to anchor the view on any single node. Names are not globally unique (pods and K8s nodes can share a name; PVCs can repeat across namespaces); all matches are returned. Combine with `cluster` / `namespace` to disambiguate. |
| `root`       | no       | no         | Cluster-scoped node ID to anchor a traversal. |
| `depth`      | no       | no         | Traversal depth (0..6, default 2 when `root` is set). |
| `direction`  | no       | no         | `in | out | both` (default `both`). |

Multiple values for the same parameter are OR-combined; different parameters
are AND-combined. Unknown values yield 200 + empty result, never an error.

**Edge retention (unified across all filters).** An edge is retained when at
least one resolved endpoint is in scope after node filtering. When exactly one
endpoint is in scope, the missing endpoint is re-added from the freshly built
graph's node index provided it passes the namespace filter (types without a
namespace label pass through). This single rule covers (a) anchoring on a
named node and rendering its incident edges with their partner endpoints,
and (b) cross-cluster `pod-calls-pod` edges where only `cluster` narrows
scope and the partner pod lives in an out-of-scope cluster.

Because there is no `pod-runs-on-node` edge, a `name` filter or a `root`
traversal anchored on a pod does **not** pull in its host K8s node — nothing
links them in the graph. A `namespace` filter likewise **drops** K8s nodes:
they carry no namespace label and have no edge to re-add them, so a
namespace-narrowed view contains only namespaced entities.

### Response shape

```jsonc
{
  "apiVersion": "v1",
  "clusters": ["cluster-alpha", "cluster-beta"],
  "elements": {
    "nodes": [{"data": {"id": "<cluster>/<uid>", "name": "...", "type": "cluster|pod|node|pvc|service|others|external", "parent": "<container-id>", "labels": {"cluster": "..."}}}],
    "edges": [{"data": {"id": "<uuidv5>", "type": "...", "source": "...", "target": "...", "labels": {"cluster": "<trace-source-cluster>"}}}]
  }
}
```

The body carries only `apiVersion`, `clusters`, and `elements`. Caller-supplied
`start` / `end` are passed through to upstream PromQL verbatim (only `end > start`
is validated); there is no server-side bucketing, alignment, window cap, or
future-time guard, and the body does not echo any timestamp. Bounded query
cost is delegated to upstream VictoriaMetrics search limits
(`-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`,
`-search.maxSamplesPerQuery`).

`labels` is strictly `map[string]string`. Numeric metrics are deferred to a
future typed struct field (see the design doc, D9).

**Compound nodes (Cytoscape only).** The Cytoscape response groups nodes into
compound containers via an optional `data.parent` field: `cluster > node > pod`
(a pod nests in its K8s node, the node in its cluster), with services and PVCs
as cluster-level siblings (`cluster > service`, `cluster > pvc`). A synthetic
`type="cluster"` group node (`id="cluster/<name>"`, `labels={}`, no `ipaddress`)
is emitted per cluster so `parent` references resolve. The pod→node relationship
is expressed **only** by this compound nesting — a pod's `data.parent` is derived
from its `labels.node` (falling back to its cluster group when the host node is
out of scope). There is **no** `pod-runs-on-node` edge. K8s `node` nodes are
therefore edgeless graph nodes that act purely as compound containers (they still
carry `external_ip` on the `ipaddress` attribute). See the design doc, D31.

Service-graph peers whose `client` / `server` label is exactly `user` or
`unknown` — the `servicegraph` connector's virtual nodes for uninstrumented
callers and unresolved peers — are excluded upstream and never appear as nodes
or edges (see the design doc, D30). The match is exact and case-sensitive, so a
`://` connection string whose host merely *contains* `user` is unaffected.

### Headers

v1 ships **no HTTP cache validators**: graph endpoints emit no `ETag` and
honour no `If-None-Match` (there is no `304 Not Modified` path). Every request
runs a fresh upstream PromQL fan-out and recomputes the body; v1 ships no
in-process result cache (see the design doc, D6).

`/v1/graph` does not emit `Cache-Control`: the server
has no view of how long a freshly built graph remains "fresh" without
re-querying upstream, so cacheability decisions are left to the client /
intermediary. Whether any party caches the response body for some TTL is a
client-side concern, not a server contract. Routes whose content is stable
for the binary's lifetime (`/v1/edge-types`, `/openapi.{yaml,json}`,
`/docs`, `/docs/assets/*`) emit explicit `Cache-Control` because their
content stability is server-known.

### Status codes / `reason`

| Status | `reason`                | Description |
|--------|-------------------------|-------------|
| 400    | `missing_start`         | `start` missing. |
| 400    | `missing_end`           | `end` missing. |
| 400    | `invalid_start`/`invalid_end` | Failed to parse timestamp. |
| 400    | `invalid_range`         | `end <= start`. |
| 400    | `depth_too_large`       | `depth > 6`. |
| 400    | `outside_retention`     | Empty topology and healthy upstream. |
| 401    | `unauthorized`          | Missing or invalid `X-API-Key` (only when API key auth is configured). |
| 502    | `upstream`              | Upstream VictoriaMetrics returned a non-2xx response or otherwise failed (RFC 9110 §15.6.3). |
| 504    | `timeout`               | Build (`/v1/graph`) exceeded `--build-timeout`, or non-graph upstream call exceeded `--api-timeout` (RFC 9110 §15.6.5). |

## `GET /v1/clusters`

Returns the list of clusters seen in `kube_node_info` over a fixed `1h`
lookback. Each request hits VictoriaMetrics directly.

## `GET /v1/edge-types`

Static catalogue. Long `Cache-Control: max-age=3600`.

## `GET /livez`, `GET /readyz`

`/livez` always returns 200 while the process runs. `/readyz` runs a 1 s
`up{}` probe and returns 200 only if the upstream answers.

## `GET /metrics`

Prometheus exposition with `kube_state_graph_*` self-metrics.
