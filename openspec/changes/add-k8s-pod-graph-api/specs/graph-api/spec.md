## ADDED Requirements

### Requirement: Versioned route prefix

The HTTP API SHALL expose every endpoint under the `/v1/` route prefix and SHALL include `apiVersion: "v1"` as a top-level field in every JSON response body.

#### Scenario: Body carries apiVersion

- **WHEN** a client sends `GET /v1/clusters`
- **THEN** the server returns 200 with a JSON body whose top-level object contains `"apiVersion": "v1"`

#### Scenario: Unversioned route is not served

- **WHEN** a client sends `GET /graph?start=...&end=...`
- **THEN** the server returns 404 Not Found

### Requirement: Time-ranged graph endpoint

The server SHALL expose `GET /v1/graph` that returns a multi-cluster pod / node / PVC graph for a caller-specified `[start, end]` time range. Both `start` and `end` SHALL be required query parameters in either RFC 3339 form or Unix-seconds integer form.

#### Scenario: Successful graph request with absolute timestamps

- **WHEN** a client sends `GET /v1/graph?start=2026-05-01T12:00:00Z&end=2026-05-01T12:05:00Z`
- **THEN** the server returns 200 with a Cytoscape.js JSON body containing `elements.nodes`, `elements.edges`, `start`, `end`, `start_actual`, `end_actual`, `bucket_seconds`, `built_at`, and `clusters` fields

#### Scenario: Missing start parameter

- **WHEN** a client sends `GET /v1/graph?end=2026-05-01T12:05:00Z`
- **THEN** the server returns 400 Bad Request with `reason: "missing_start"`

#### Scenario: Missing end parameter

- **WHEN** a client sends `GET /v1/graph?start=2026-05-01T12:00:00Z`
- **THEN** the server returns 400 Bad Request with `reason: "missing_end"`

#### Scenario: end is not after start

- **WHEN** a client sends `GET /v1/graph?start=2026-05-01T12:05:00Z&end=2026-05-01T12:00:00Z`
- **THEN** the server returns 400 Bad Request with `reason: "invalid_range"`

#### Scenario: Window exceeds maximum

- **WHEN** a client requests a window longer than `--max-window`
- **THEN** the server returns 400 Bad Request with `reason: "window_too_large"`

#### Scenario: end is too far in the future

- **WHEN** a client supplies `end > now + --max-skew`
- **THEN** the server returns 400 Bad Request with `reason: "end_in_future"`

### Requirement: Time-bucket alignment in response

The server SHALL floor `start` and `end` to the bucket boundary determined by the time class of `end` (live=15s, recent=60s, historical=5m, frozen=5m) and SHALL surface the bucketed values as `start_actual` and `end_actual` in the response body, alongside the original caller-supplied `start` and `end`.

#### Scenario: Live window bucketing

- **WHEN** a client sends `GET /v1/graph?start=...&end=<now-3s>` and the time class resolves to `live`
- **THEN** the response body has `start_actual` and `end_actual` rounded down to a 15-second boundary, and `bucket_seconds: 15`

#### Scenario: Frozen window bucketing

- **WHEN** a client sends a request whose `end` is older than 7 days
- **THEN** the response body has `start_actual` and `end_actual` rounded down to a 5-minute boundary, and `bucket_seconds: 300`

### Requirement: Cytoscape.js response shape

`GET /v1/graph` SHALL return a JSON document in Cytoscape.js shape: `{ apiVersion, start, end, start_actual, end_actual, bucket_seconds, built_at, clusters, elements: { nodes, edges } }`.

Each **node** SHALL be `{ data: { id, name, type, labels } }`:
- `id` SHALL be a cluster-scoped composite for pods / K8s nodes / PVCs (pods: `<cluster>/<pod-uid>`; nodes: `<cluster>/<node-name>`; PVCs: `<cluster>/<namespace>/<claim>`). For external nodes, `id` SHALL be `external/<label-value>` (no cluster prefix).
- `name` SHALL be the human-readable pod / node / PVC name (used for the Grafana panel display label). For external nodes, `name` SHALL be the verbatim `client` or `server` label value from the source service-graph series.
- `type` SHALL be one of the strings `"pod"`, `"node"`, `"pvc"`, `"external"`.
- `labels` SHALL be a JSON object whose values are strings only (`map[string]string`). For pod / K8s node / PVC nodes it SHALL include at minimum a `cluster` entry; for pods and PVCs it SHALL also include a `namespace` entry; for pods it SHALL include `node` (the cluster-scoped node ID); for K8s nodes it SHALL include `external_ip` when the upstream provided one. **For external nodes**, `labels` SHALL contain at least `pattern` (the configured `KSG_EXTERNAL_NAME_PATTERN` substring that matched) and SHALL NOT contain a `cluster` entry.

Each **edge** SHALL be `{ data: { id, type, source, target, labels } }`:
- `id` SHALL be a UUID, RFC 4122 compliant, encoded as a lowercase canonical string.
- `type` SHALL be one of the registered edge types from `/v1/edge-types`.
- `source` and `target` SHALL each match the `id` of a node present in the same response's `elements.nodes`.
- `labels` SHALL be a JSON object whose values are strings only (`map[string]string`). The exact key set per edge type is defined by the `pod-service-graph` and `cluster-topology-source` capabilities.

Implementations SHALL NOT encode booleans or numbers as strings inside `labels`. Non-string-typed data (numeric metrics, boolean flags) is deferred to a future typed struct field on `data` and is NOT part of the v1 contract.

#### Scenario: Pod node payload

- **WHEN** the response contains a pod node
- **THEN** its `data.type` equals `"pod"`, its `data.id` matches `<cluster>/<pod-uid>`, its `data.name` equals the pod's metadata name, and `data.labels.cluster` matches the cluster prefix in the ID

#### Scenario: K8s node payload

- **WHEN** the response contains a Kubernetes-node node
- **THEN** its `data.type` equals `"node"`, its `data.id` matches `<cluster>/<node-name>`, its `data.name` equals the node's metadata name, and `data.labels.external_ip` is present whenever the upstream metric provided one

#### Scenario: PVC node payload

- **WHEN** the response contains a PVC node
- **THEN** its `data.type` equals `"pvc"`, its `data.id` matches `<cluster>/<namespace>/<claim>`, its `data.name` equals the claim name, and `data.labels.namespace` equals the PVC namespace

#### Scenario: External node payload

- **WHEN** the response contains an external node produced by the `KSG_EXTERNAL_NAME_PATTERN` substitution rule
- **THEN** its `data.type` equals `"external"`, its `data.id` equals `external/<value>`, its `data.name` equals `<value>` (the verbatim service-graph `client` or `server` label that matched), `data.labels` contains a `pattern` key whose value is the configured substring, and `data.labels` does NOT contain a `cluster` key

#### Scenario: Edge payload references existing nodes

- **WHEN** the response contains any edge
- **THEN** both `data.source` and `data.target` SHALL match the `data.id` of a node present in the same response's `elements.nodes`

#### Scenario: Edge id is a UUID

- **WHEN** the response contains any edge
- **THEN** `data.id` matches the regex `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

#### Scenario: Edge id is stable across rebuilds

- **WHEN** the same logical edge (same `type`, `source`, `target`) is produced by two consecutive builds for the same time bucket
- **THEN** `data.id` is byte-identical between the two builds

### Requirement: Grafana Node Graph compatibility route

The server SHALL expose `GET /v1/graph/nodegraph` that returns the same underlying graph as `/v1/graph` projected into the Grafana Node Graph API datasource shape (parallel `nodes_fields`/`nodes` and `edges_fields`/`edges` arrays), accepting the same query parameters as `/v1/graph`. The serializer SHALL map the canonical schema as follows:

- Node `name` → `title`.
- Node `labels.cluster` ` · ` `labels.namespace` (or `labels.cluster` alone when no namespace) → `subTitle`.
- Node `type` → `mainStat`.
- Edge `type` → `mainStat`.
- Edge `secondaryStat` SHALL be omitted in v1 (numeric edge metrics are deferred to a future typed field).

#### Scenario: Nodegraph request returns Grafana shape

- **WHEN** a client sends `GET /v1/graph/nodegraph?start=...&end=...`
- **THEN** the response body contains `nodes_fields`, `nodes`, `edges_fields`, and `edges` arrays (Grafana Node Graph compatible) and does NOT contain a Cytoscape `elements` field

#### Scenario: Pod name shown as title in Grafana

- **WHEN** the nodegraph response contains a pod node
- **THEN** the row's `title` field equals the pod's canonical `name`

#### Scenario: Cluster context surfaced in subTitle

- **WHEN** the nodegraph response contains a pod node
- **THEN** the row's `subTitle` field includes the `<cluster>` value (and, when present, the namespace) so cross-cluster context is visible in Grafana

### Requirement: Filter parameters

`GET /v1/graph` and `GET /v1/graph/nodegraph` SHALL accept the optional, repeatable filter parameters `cluster`, `namespace`, `node`, `edge_type`. Filters SHALL be applied at response time over the cached graph. Empty filter SHALL return the full multi-cluster graph for the time window. Multiple values for the same parameter SHALL be OR-combined; different parameters SHALL be AND-combined. An unknown filter value SHALL NOT cause an error.

#### Scenario: Cluster filter narrows result

- **WHEN** the cached graph contains pods in `cluster-alpha` and `cluster-beta` and a client sends `?cluster=cluster-alpha`
- **THEN** the response contains pod nodes only for `cluster-alpha`, plus any cross-cluster edge endpoints in `cluster-beta` that participate in an edge to `cluster-alpha`

#### Scenario: Namespace filter combined with cluster

- **WHEN** a client sends `?cluster=cluster-alpha&namespace=ns-x&namespace=ns-y`
- **THEN** the response contains pods whose cluster is `cluster-alpha` AND whose namespace is `ns-x` OR `ns-y`

#### Scenario: Edge-type filter with no matching edges

- **WHEN** a client sends `?edge_type=pod-calls-pod` and the time window contains no service-graph data
- **THEN** the response is 200 with `elements.edges: []` and no error

#### Scenario: Unknown cluster name

- **WHEN** a client sends `?cluster=does-not-exist`
- **THEN** the response is 200 with empty `elements.nodes` and `elements.edges`

### Requirement: Partial-graph traversal

`GET /v1/graph` SHALL accept `?root=<id>&depth=<n>&direction=in|out|both` for partial-graph traversal. `depth` SHALL default to 2 and SHALL NOT exceed 6. Traversal SHALL run a BFS on the cached graph's adjacency map, then any other filter parameters SHALL apply to the traversal result.

#### Scenario: Outgoing traversal at depth 1

- **WHEN** a client sends `?root=cluster-alpha/<pod-uid>&depth=1&direction=out`
- **THEN** the response contains the root node and every node reachable in one outgoing edge from it

#### Scenario: depth above maximum

- **WHEN** a client supplies `depth=10`
- **THEN** the server returns 400 Bad Request with `reason: "depth_too_large"`

#### Scenario: Unknown root id

- **WHEN** a client supplies a `root` value that does not match any node in the graph
- **THEN** the response is 200 with empty `elements.nodes` and `elements.edges`

### Requirement: Cluster discovery endpoint

The server SHALL expose `GET /v1/clusters` that returns the list of clusters with data in centralised VictoriaMetrics over the discovery lookback window (default 1 hour). The response SHALL be derived live from a single `group by (cluster) (kube_node_info)` query and SHALL be cached internally for 60 seconds. When `--clusters-allowlist` is set, the result SHALL be intersected with the allowlist before being returned.

#### Scenario: Live discovery

- **WHEN** centralised VictoriaMetrics holds `kube_node_info` series with `cluster="cluster-alpha"` and `cluster="cluster-beta"` in the last hour
- **THEN** `GET /v1/clusters` returns 200 with a `clusters` array containing both names

#### Scenario: Allowlist applied

- **WHEN** the server is started with `--clusters-allowlist cluster-alpha` and centralised VictoriaMetrics also has data for `cluster-beta`
- **THEN** `GET /v1/clusters` returns only `cluster-alpha`

### Requirement: Edge-type discovery endpoint

The server SHALL expose `GET /v1/edge-types` that returns the static catalogue of edge types this server can produce. The response SHALL list at least `pod-runs-on-node`, `pod-mounts-pvc-on-node`, and `pod-calls-pod`. Each catalogue entry SHALL describe `source_type` (one of `"pod"`, `"node"`, `"pvc"`, `"external"`, **or a JSON array of such strings** when more than one is permitted), `target_type` (same form as `source_type`), `directed`, `may_cross_cluster`, and a `labels` array enumerating the keys this edge type can emit on edge `labels`. The endpoint SHALL NOT issue any upstream calls and SHALL NOT depend on time-range or cluster parameters. The response SHALL include a long `Cache-Control: public, max-age=3600` header and a stable `ETag` derived from the in-code registry.

#### Scenario: Static catalogue

- **WHEN** a client sends `GET /v1/edge-types`
- **THEN** the response body contains an `edge_types` array including objects whose `type` values include `pod-runs-on-node`, `pod-mounts-pvc-on-node`, and `pod-calls-pod`

#### Scenario: pod-calls-pod marked may_cross_cluster

- **WHEN** a client inspects the catalogue entry for `pod-calls-pod`
- **THEN** its `may_cross_cluster` field is `true`, its `source_type` and `target_type` are arrays containing both `"pod"` and `"external"`, and its `labels` array enumerates entries whose `name` values include `client_cluster` and `server_cluster`, each with `value_type: "string"`

#### Scenario: Conditional GET on /v1/edge-types

- **WHEN** a client repeats the request with `If-None-Match` matching the previous `ETag`
- **THEN** the server returns 304 Not Modified

### Requirement: Cross-cluster edge representation

When the cached graph contains a `pod-calls-pod` edge whose underlying `client_cluster` differs from `server_cluster`, the API SHALL emit it as a single edge carrying both `labels.client_cluster` and `labels.server_cluster` and SHALL include both endpoint nodes in the response `elements.nodes` whenever the projection scope includes either endpoint's cluster. Consumers detect cross-cluster status by string comparison of `labels.client_cluster` and `labels.server_cluster` on the edge.

#### Scenario: Cross-cluster edge with both clusters in scope

- **WHEN** a client requests `?cluster=cluster-alpha&cluster=cluster-beta` for a window containing a cross-cluster edge between the two
- **THEN** the response contains both endpoint pod nodes and one edge with `labels.client_cluster: "cluster-alpha"` and `labels.server_cluster: "cluster-beta"`

#### Scenario: Cross-cluster edge with one cluster in scope

- **WHEN** a client requests `?cluster=cluster-alpha` and a cross-cluster edge exists from `cluster-alpha` to `cluster-beta`
- **THEN** the response contains the `cluster-alpha` endpoint, the `cluster-beta` endpoint (so the edge resolves), and the edge whose `labels.client_cluster` and `labels.server_cluster` differ

### Requirement: HTTP caching headers

Every successful response from `GET /v1/graph`, `GET /v1/graph/nodegraph`, `GET /v1/clusters`, and `GET /v1/edge-types` SHALL carry a `Cache-Control: public, max-age=<n>` header derived from the time class of the underlying data and an `ETag` header derived from a SHA-256 of the response body. Requests carrying a matching `If-None-Match` SHALL receive `304 Not Modified`.

#### Scenario: 304 Not Modified on repeated request

- **WHEN** a client receives an `ETag` from a previous `GET /v1/graph` response and re-sends the same query with `If-None-Match: "<etag>"`
- **THEN** the server returns 304 Not Modified with no body

#### Scenario: X-Cache header on cache hit

- **WHEN** an in-process cache hit serves the request
- **THEN** the response carries `X-Cache: HIT`

#### Scenario: X-Cache header on cache miss

- **WHEN** the server builds the graph from upstream for the request
- **THEN** the response carries `X-Cache: MISS` (or `COALESCED` if the request was deduplicated by singleflight)

### Requirement: Health endpoints

The server SHALL expose `GET /livez` that returns 200 while the process is running, and `GET /readyz` that returns 200 only when a 1-second `up{}` probe against the centralised VictoriaMetrics succeeds. `GET /readyz` SHALL return 503 otherwise.

#### Scenario: livez always healthy while running

- **WHEN** a client sends `GET /livez`
- **THEN** the response is 200 with body `"ok"` regardless of upstream state

#### Scenario: readyz fails when upstream unreachable

- **WHEN** the configured VictoriaMetrics URL refuses connections and a client sends `GET /readyz`
- **THEN** the response is 503 with a JSON body containing a `reason` field

### Requirement: Self-metrics endpoint

The server SHALL expose `GET /metrics` in Prometheus exposition format including at least: `kube_state_graph_build_duration_seconds`, `kube_state_graph_project_duration_seconds`, `kube_state_graph_serialise_duration_seconds`, `kube_state_graph_cache_hits_total`, `kube_state_graph_cache_misses_total`, `kube_state_graph_cache_size_entries`, `kube_state_graph_cache_cost_bytes`, `kube_state_graph_singleflight_dedup_total`, `kube_state_graph_build_concurrency`, `kube_state_graph_build_rejected_total`, `kube_state_graph_graph_node_count`, `kube_state_graph_graph_edge_count`, `kube_state_graph_clusters_observed`, `kube_state_graph_upstream_query_duration_seconds`, `kube_state_graph_upstream_query_failures_total`, and `kube_state_graph_http_requests_total`.

#### Scenario: Metrics exposition

- **WHEN** a client sends `GET /metrics`
- **THEN** the response is 200 in `text/plain; version=0.0.4` exposition format and includes all metric names listed above

#### Scenario: cluster label on observational gauges

- **WHEN** a build has produced a multi-cluster graph
- **THEN** `kube_state_graph_graph_node_count` series include a `cluster` label and `kube_state_graph_graph_edge_count` series include a `cross_cluster` label

### Requirement: Concurrency cap

The server SHALL cap the number of concurrent in-flight graph builds to a configurable limit (default 8). Requests that would exceed the cap SHALL receive `503 Service Unavailable` with a `Retry-After: 1` header and a JSON body containing `reason: "capacity"`.

#### Scenario: Build over capacity

- **WHEN** the configured cap is 1 and two concurrent cache-missing requests arrive for different time buckets
- **THEN** the second request returns 503 with `reason: "capacity"` and `Retry-After: 1`

### Requirement: Per-build timeout

The server SHALL apply a configurable per-build context timeout (default 15 seconds). On timeout, the build SHALL be aborted, the `kube_state_graph_build_rejected_total{reason="timeout"}` counter SHALL be incremented, and the request SHALL receive `503 Service Unavailable` with `reason: "timeout"`.

#### Scenario: Upstream stalls beyond timeout

- **WHEN** centralised VictoriaMetrics fails to respond within the configured timeout
- **THEN** the request returns 503 with `reason: "timeout"` and `Retry-After: 1`

### Requirement: Cluster-size ceiling

When a probe `count(kube_pod_info)` for the current scope exceeds `--max-pods` (default 5000), the server SHALL fail the build fast and return `503 Service Unavailable` with `reason: "cluster_too_large"`.

#### Scenario: Cluster too large

- **WHEN** centralised VictoriaMetrics reports more than `--max-pods` pods in scope and a client sends `GET /v1/graph?start=...&end=...`
- **THEN** the response is 503 with `reason: "cluster_too_large"`

### Requirement: Outside-retention error

When a topology query for the requested window returns zero rows but the upstream VictoriaMetrics is reachable (a parallel `up{}` probe succeeds), the server SHALL respond `400 Bad Request` with `reason: "outside_retention"`.

#### Scenario: Window beyond retention

- **WHEN** a client requests a window older than upstream `kube_pod_info` retention but `up{}` returns 1
- **THEN** the response is 400 with `reason: "outside_retention"`

### Requirement: Operator endpoints

The server SHALL expose `DELETE /admin/cache` that flushes the in-process Ristretto cache and returns 204 No Content. Behind a `--enable-debug` flag the server SHALL additionally expose `GET /debug/last-queries` that returns the raw upstream query strings and a redacted summary (counts and labels, not values) of the most recent build.

#### Scenario: Cache flush

- **WHEN** a client sends `DELETE /admin/cache`
- **THEN** the server returns 204 No Content and the next graph request results in a cache miss

#### Scenario: Debug endpoint disabled by default

- **WHEN** the server is started without `--enable-debug` and a client sends `GET /debug/last-queries`
- **THEN** the response is 404 Not Found

### Requirement: Structured request logging

Every served HTTP request SHALL emit exactly one structured log line via `log/slog` JSON handler containing at least `method`, `path`, `status`, `duration_ms`, `request_id`, applied `cluster` filter values, and `cache_status`.

#### Scenario: Request log line

- **WHEN** the server serves a request
- **THEN** stdout receives a JSON object with the listed fields and a top-level `level` field set to `INFO` for non-error responses

### Requirement: OpenAPI specification served by the API

The server SHALL serve the auto-generated OpenAPI 3.0 specification at two routes:

- `GET /openapi.yaml` SHALL return the YAML form with `Content-Type: application/yaml`.
- `GET /openapi.json` SHALL return the JSON form with `Content-Type: application/json`.

Both responses SHALL carry `Cache-Control: public, max-age=3600` and a stable `ETag` derived from the embedded spec contents. Both SHALL honour `If-None-Match` with `304 Not Modified`. The spec SHALL be generated from handler annotations via `swaggo/swag` v2; the generated `docs/swagger.{json,yaml,go}` artefacts SHALL be checked into the repository.

#### Scenario: YAML spec served

- **WHEN** a client sends `GET /openapi.yaml`
- **THEN** the response is 200 with `Content-Type: application/yaml` and a body whose first non-empty line begins with `openapi:`

#### Scenario: JSON spec served

- **WHEN** a client sends `GET /openapi.json`
- **THEN** the response is 200 with `Content-Type: application/json` and a body whose top-level object contains an `"openapi"` key

#### Scenario: Conditional GET on the spec

- **WHEN** a client repeats the request with `If-None-Match` matching the previous `ETag`
- **THEN** the server returns 304 Not Modified

### Requirement: Scalar API Reference UI served at /docs

The server SHALL serve the Scalar API Reference UI at `GET /docs`, rendering the OpenAPI spec from `/openapi.yaml`. All Scalar JS / CSS assets MUST be vendored in the binary (embedded via `embed.FS`) and served from `/docs/assets/*`. The HTML page returned by `/docs` MUST reference those assets via relative paths so the UI renders correctly behind reverse proxies and in air-gapped environments without internet access.

#### Scenario: /docs renders without external network

- **WHEN** the server is reachable from a client and the host has no internet connectivity, and the client sends `GET /docs`
- **THEN** the response is 200, `Content-Type: text/html`, and every `<script>` and `<link>` tag references either the same origin or a relative path; no `<script src="https://…">` or `<link href="https://…">` tag is present

#### Scenario: Vendored bundle served at /docs/assets/

- **WHEN** the HTML page references `/docs/assets/<file>` for the Scalar bundle
- **THEN** `GET /docs/assets/<file>` returns 200 with appropriate `Content-Type` and `Cache-Control: public, max-age=86400, immutable`

### Requirement: Route ↔ spec drift contract test

The repository SHALL include a Go test that parses the embedded OpenAPI spec and asserts that:

- Every `(method, path)` pair declared in the spec corresponds to a registered Gin route in `Server.Handler()`.
- Every Gin route registered in `Server.Handler()` corresponds to a `(method, path)` pair declared in the spec, modulo an explicit allowlist of infrastructure paths (`/livez`, `/readyz`, `/metrics`, `/admin/cache`, `/debug/last-queries`, `/openapi.yaml`, `/openapi.json`, `/docs`, `/docs/assets/*`).

The test SHALL run on every PR via `go test ./...` and SHALL fail when annotations and routes drift.

#### Scenario: Handler added without annotation

- **WHEN** a contributor adds a new `/v1/<route>` handler without `// @Router` and `// @Summary` annotations
- **THEN** running `go test ./internal/api/` fails with a clear message naming the undocumented route

#### Scenario: Annotation pointing at removed route

- **WHEN** a contributor removes a Gin route but leaves the corresponding `// @Router` annotation in place (and forgets to regenerate the spec)
- **THEN** running `go test ./internal/api/` fails with a clear message naming the orphan documented path
