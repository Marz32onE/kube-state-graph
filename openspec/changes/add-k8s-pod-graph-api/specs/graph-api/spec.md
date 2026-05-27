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
- **THEN** the server returns 200 with a Cytoscape.js JSON body containing exactly `apiVersion`, `clusters`, and `elements` (with `elements.nodes` and `elements.edges`)

#### Scenario: Missing start parameter

- **WHEN** a client sends `GET /v1/graph?end=2026-05-01T12:05:00Z`
- **THEN** the server returns 400 Bad Request with `reason: "missing_start"`

#### Scenario: Missing end parameter

- **WHEN** a client sends `GET /v1/graph?start=2026-05-01T12:00:00Z`
- **THEN** the server returns 400 Bad Request with `reason: "missing_end"`

#### Scenario: end is not after start

- **WHEN** a client sends `GET /v1/graph?start=2026-05-01T12:05:00Z&end=2026-05-01T12:00:00Z`
- **THEN** the server returns 400 Bad Request with `reason: "invalid_range"`

### Requirement: Time-window passthrough

The server SHALL pass caller-supplied `start` and `end` through to upstream PromQL verbatim, after enforcing `end > start`. There is no server-side bucketing, alignment, grid, window cap, or future-time guard; the response body SHALL NOT echo `start`, `end`, or any derived timestamp. Operators relying on bounded query cost SHALL configure upstream VictoriaMetrics search limits (e.g. `-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`).

#### Scenario: Caller timestamps drive PromQL

- **WHEN** a client sends `GET /v1/graph?start=2026-05-02T12:04:17Z&end=2026-05-02T12:19:30Z`
- **THEN** the upstream PromQL is evaluated with `<window> = end - start` and `<end> = 2026-05-02T12:19:30Z`, and the response body contains only `apiVersion`, `clusters`, and `elements`

### Requirement: Cytoscape.js response shape

`GET /v1/graph` SHALL return a JSON document in Cytoscape.js shape: `{ apiVersion, clusters, elements: { nodes, edges } }`. The body SHALL NOT contain time-varying or echo-of-input fields, so identical inputs against the same upstream state produce byte-identical bodies.

Each **node** SHALL be `{ data: { id, name, type, labels } }`:
- `id` SHALL be a cluster-scoped composite for pods / K8s nodes / PVCs (pods: `<cluster>/<pod-uid>`; nodes: `<cluster>/<node-name>`; PVCs: `<cluster>/<namespace>/<claim>`). For external nodes, `id` SHALL be `external/<label-value>` (no cluster prefix).
- `name` SHALL be the human-readable pod / node / PVC name (used for the Grafana panel display label). For external nodes, `name` SHALL be the verbatim `client` or `server` label value from the source service-graph series.
- `type` SHALL be one of the strings `"pod"`, `"node"`, `"pvc"`, `"external"`.
- `labels` SHALL be a JSON object whose values are strings only (`map[string]string`). For pod / K8s node / PVC nodes it SHALL include at minimum a `cluster` entry; for pods and PVCs it SHALL also include a `namespace` entry; for pods it SHALL include `node` (the cluster-scoped node ID), and SHALL include `pod_ip` and `host_ip` whenever the upstream `kube_pod_info` series carried them; for K8s nodes it SHALL include `external_ip` when the upstream provided one. **For external nodes**, `labels` SHALL contain at least `pattern` (the configured `KSG_EXTERNAL_NAME_PATTERN` substring that matched) and SHALL NOT contain a `cluster` entry.

Each **edge** SHALL be `{ data: { id, type, source, target, labels } }`:
- `id` SHALL be a UUID, RFC 4122 compliant, encoded as a lowercase canonical string.
- `type` SHALL be one of the registered edge types from `/v1/edge-types`.
- `source` and `target` SHALL each match the `id` of a node present in the same response's `elements.nodes`.
- `labels` SHALL be a JSON object whose values are strings only (`map[string]string`). The exact key set per edge type is defined by the `pod-service-graph` and `cluster-topology-source` capabilities.

Implementations SHALL NOT encode booleans or numbers as strings inside `labels`. Non-string-typed data (numeric metrics, boolean flags) is deferred to a future typed struct field on `data` and is NOT part of the v1 contract.

#### Scenario: Pod node payload

- **WHEN** the response contains a pod node
- **THEN** its `data.type` equals `"pod"`, its `data.id` matches `<cluster>/<pod-uid>`, its `data.name` equals the pod's metadata name, and `data.labels.cluster` matches the cluster prefix in the ID

#### Scenario: Pod node payload includes pod_ip and host_ip when upstream emits them

- **WHEN** the response contains a pod node whose source `kube_pod_info` series carried `pod_ip` and `host_ip`
- **THEN** `data.labels.pod_ip` equals the upstream `pod_ip` value and `data.labels.host_ip` equals the upstream `host_ip` value

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

`GET /v1/graph` and `GET /v1/graph/nodegraph` SHALL accept the optional, repeatable filter parameters `cluster`, `namespace`, `edge_type`, `name`. Filters SHALL be applied at response time as a projection over the freshly built graph. Empty filter SHALL return the full multi-cluster graph for the time window. Multiple values for the same parameter SHALL be OR-combined; different parameters SHALL be AND-combined. An unknown filter value SHALL NOT cause an error.

The `name` parameter SHALL match `n.Name()` by exact string equality across **every** node type (`PodNode`, `K8sNode`, `PVCNode`, `ExternalNode`) — a single `?name=` value matches a pod, a K8s node, a PVC, or an external endpoint with the same name. Names are not globally unique (pods and K8s nodes can share a name; PVCs can repeat across namespaces); all matches SHALL be returned.

**Edge retention rule (unified across all filters).** An edge SHALL be retained when at least one resolved endpoint is in scope after node filtering. When exactly one endpoint is in scope, the missing endpoint SHALL be re-added from the freshly built graph's node index provided it passes the non-cluster filters (namespace check; types without a namespace label pass through). This single rule covers (a) anchoring on a named node and visualising its incident edges with their partner endpoints, and (b) cross-cluster `pod-calls-pod` edges where only `cluster` narrows scope and the partner pod lives outside the in-scope cluster set.

#### Scenario: Cluster filter narrows result

- **WHEN** the freshly built graph contains pods in `cluster-alpha` and `cluster-beta` and a client sends `?cluster=cluster-alpha`
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

#### Scenario: Name filter matches a pod

- **WHEN** the freshly built graph contains pods named `frontend` and `backend` in `cluster-alpha` and a client sends `?name=frontend`
- **THEN** the response contains the `frontend` pod node and any K8s-node, PVC, or external-endpoint nodes that are edge endpoints of `frontend`, but NOT the `backend` pod node

#### Scenario: Name filter matches a K8s node

- **WHEN** the freshly built graph contains a K8s node named `worker-1` in `cluster-alpha` and a client sends `?name=worker-1`
- **THEN** the response contains the `worker-1` K8s-node and any pod nodes that are edge endpoints of `worker-1` via `pod-runs-on-node`

#### Scenario: Name filter matches a PVC

- **WHEN** the freshly built graph contains a PVC named `checkout-data` in `cluster-alpha/shop` and a client sends `?name=checkout-data`
- **THEN** the response contains the `checkout-data` PVC node and any pod nodes that mount it via `pod-mounts-pvc`

#### Scenario: Name shared across types returns every match

- **WHEN** a pod and a K8s node both happen to be named `worker-1` and a client sends `?name=worker-1`
- **THEN** the response contains both the matching pod node AND the matching K8s-node node

#### Scenario: Name shared across clusters returns every match

- **WHEN** a pod named `api` exists in both `cluster-alpha` and `cluster-beta` and a client sends `?name=api`
- **THEN** the response contains both `cluster-alpha`'s `api` pod node and `cluster-beta`'s `api` pod node

#### Scenario: Name filter combined with cluster

- **WHEN** a pod named `api` exists in both `cluster-alpha` and `cluster-beta` and a client sends `?name=api&cluster=cluster-alpha`
- **THEN** the response contains only `cluster-alpha`'s `api` pod node

#### Scenario: Name filter retains incident edges with re-hydrated partner

- **WHEN** a `pod-calls-pod` edge crosses from `cluster-alpha/<uid-A>` (pod name `frontend`) to `cluster-beta/<uid-B>` (pod name `backend`) and a client sends `?name=frontend`
- **THEN** the response contains `cluster-alpha/<uid-A>` (the named match), `cluster-beta/<uid-B>` (re-added as the missing edge endpoint), and the cross-cluster edge

#### Scenario: Unknown name returns empty result

- **WHEN** a client sends `?name=does-not-exist`
- **THEN** the response is 200 with empty `elements.nodes` and `elements.edges`

### Requirement: Partial-graph traversal

`GET /v1/graph` SHALL accept `?root=<id>&depth=<n>&direction=in|out|both` for partial-graph traversal. `depth` SHALL default to 2 and SHALL NOT exceed 6. Traversal SHALL run a BFS on the freshly built graph's adjacency map, then any other filter parameters SHALL apply to the traversal result.

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

The server SHALL expose `GET /v1/clusters` that returns the list of clusters with data in centralised VictoriaMetrics over a fixed 1-hour lookback. The response SHALL be derived live from a single `group by (cluster) (last_over_time(kube_node_info[1h]))` query on every request — there is no in-process discovery cache in v1.

#### Scenario: Live discovery

- **WHEN** centralised VictoriaMetrics holds `kube_node_info` series with `cluster="cluster-alpha"` and `cluster="cluster-beta"` in the last hour
- **THEN** `GET /v1/clusters` returns 200 with a `clusters` array containing both names

### Requirement: Edge-type discovery endpoint

The server SHALL expose `GET /v1/edge-types` that returns the static catalogue of edge types this server can produce. The response SHALL list at least `pod-runs-on-node`, `pod-mounts-pvc`, and `pod-calls-pod`. Each catalogue entry SHALL describe `source_type` (one of `"pod"`, `"node"`, `"pvc"`, `"external"`, **or a JSON array of such strings** when more than one is permitted), `target_type` (same form as `source_type`), `directed`, `may_cross_cluster`, and a `labels` array enumerating the keys this edge type can emit on edge `labels`. The endpoint SHALL NOT issue any upstream calls and SHALL NOT depend on time-range or cluster parameters. The response SHALL include a long `Cache-Control: public, max-age=3600` header.

#### Scenario: Static catalogue

- **WHEN** a client sends `GET /v1/edge-types`
- **THEN** the response body contains an `edge_types` array including objects whose `type` values include `pod-runs-on-node`, `pod-mounts-pvc`, and `pod-calls-pod`

#### Scenario: pod-calls-pod marked may_cross_cluster

- **WHEN** a client inspects the catalogue entry for `pod-calls-pod`
- **THEN** its `may_cross_cluster` field is `true`, its `source_type` and `target_type` are arrays containing both `"pod"` and `"external"`, and its `labels` array enumerates an entry whose `name` is `cluster` with `value_type: "string"` (representing the trace source cluster; cross-cluster status is detected by comparing the source/target nodes' `labels.cluster` rather than from edge labels)

### Requirement: Cross-cluster edge representation

When the freshly built graph contains a `pod-calls-pod` edge whose source-node cluster differs from its target-node cluster, the API SHALL emit it as a single edge carrying `labels.cluster` (the trace source / client-side cluster) and SHALL include both endpoint nodes in the response `elements.nodes` whenever the projection scope includes either endpoint's cluster. Consumers detect cross-cluster status by comparing the `labels.cluster` of the edge's resolved source and target nodes — not from edge labels.

#### Scenario: Cross-cluster edge with both clusters in scope

- **WHEN** a client requests `?cluster=cluster-alpha&cluster=cluster-beta` for a window containing a cross-cluster edge whose client pod is in `cluster-alpha` and server pod is in `cluster-beta`
- **THEN** the response contains both endpoint pod nodes and one edge with `labels.cluster: "cluster-alpha"`, where the source node's `labels.cluster` is `"cluster-alpha"` and the target node's `labels.cluster` is `"cluster-beta"`

#### Scenario: Cross-cluster edge with one cluster in scope

- **WHEN** a client requests `?cluster=cluster-alpha` and a cross-cluster edge exists from a pod in `cluster-alpha` to a pod in `cluster-beta`
- **THEN** the response contains the `cluster-alpha` endpoint, the `cluster-beta` endpoint (so the edge resolves), and the edge with `labels.cluster: "cluster-alpha"`; the cross-cluster status is detected by comparing the two endpoint nodes' `labels.cluster` values

### Requirement: Deterministic response body

For identical input — same `(window, filters, traversal, upstream-data)` — the server SHALL produce a byte-identical response body across rebuilds. The server SHALL NOT emit any HTTP cache validator (no `ETag`, no `Last-Modified`): cacheability is intentionally a future-iteration concern and v1 has no in-process result cache.

The serialiser SHALL maintain determinism by sorting `view.Nodes` and `view.Edges`, sorting `Graph.ClusterNames()`, sorting `IPAddress` slices at construction, and keeping the response body shape fixed at `{apiVersion, clusters, elements}` for graph routes (no time-of-build or echo-of-input fields).

`GET /v1/edge-types`, `GET /openapi.yaml`, `GET /openapi.json`, `GET /docs`, and `GET /docs/assets/*` SHALL carry an explicit `Cache-Control` header (long max-age for the embedded assets). `GET /v1/graph`, `GET /v1/graph/nodegraph`, and `GET /v1/clusters` SHALL NOT emit a `Cache-Control` header.

#### Scenario: Body byte-identical across repeated requests

- **WHEN** a client sends two consecutive `GET /v1/graph` requests with identical query parameters and the upstream data has not changed between them
- **THEN** both response bodies are byte-identical, even though each request triggered an independent upstream fan-out

### Requirement: Node `ipaddress` attribute

Every `data` object for a node in the Cytoscape response SHALL expose a top-level `ipaddress` field of type `string[]` with `omitempty` semantics:

- `type="pod"` nodes SHALL carry the pod's IP from `kube_pod_info.pod_ip` (single-element slice) when the source metric surfaces it, and omit the field otherwise.
- `type="node"` nodes SHALL carry the K8s node's `ExternalIP` from `kube_node_status_addresses` (single-element slice) when present, and omit the field otherwise.
- `type="pvc"` and `type="external"` nodes SHALL NOT emit the `ipaddress` field.

The legacy `labels.pod_ip`, `labels.host_ip`, and `labels.external_ip` keys SHALL NOT appear on any node entry — they are replaced by the typed `ipaddress` attribute and the node entry respectively.

#### Scenario: Pod entry carries pod IP on ipaddress

- **WHEN** `kube_pod_info` exposes `pod_ip="10.244.0.10"` for a pod
- **THEN** the corresponding `type="pod"` node carries `data.ipaddress: ["10.244.0.10"]` and neither `data.labels.pod_ip` nor `data.labels.host_ip` is present

#### Scenario: Node entry carries ExternalIP on ipaddress

- **WHEN** `kube_node_status_addresses{type="ExternalIP",address="203.0.113.10"}` is present for a K8s node
- **THEN** the corresponding `type="node"` entry carries `data.ipaddress: ["203.0.113.10"]` and `data.labels.external_ip` is not present

#### Scenario: ipaddress omitted when source metric does not surface it

- **WHEN** a pod's `kube_pod_info` series omits `pod_ip`, or a K8s node has no `ExternalIP` row in `kube_node_status_addresses`
- **THEN** the corresponding node's `data` object does not include an `ipaddress` field

#### Scenario: PVC and external nodes never carry ipaddress

- **WHEN** the response contains nodes of `type="pvc"` or `type="external"`
- **THEN** those node `data` objects do not include an `ipaddress` field

### Requirement: API-key authentication on `/v1/*` and `/debug/*`

When the server is started with at least one API key configured (via `--api-keys-file` or `--api-keys`), every request to `/v1/*` and `/debug/*` SHALL carry an `X-API-Key: <key>` header. Requests without the header SHALL receive `401 Unauthorized` with reason `unauthorized` and a JSON message indicating the missing header. Requests with a header value that is not present in the configured key set SHALL receive `401 Unauthorized` with reason `unauthorized`.

When no keys are configured (both flags empty), the middleware SHALL be a no-op: every route SHALL behave as if auth were not configured. The server SHALL log a warning at boot identifying that auth is disabled.

The following routes SHALL be exempt from authentication regardless of configuration: `/livez`, `/readyz`, `/metrics`, `/openapi.yaml`, `/openapi.json`, `/docs`, and `/docs/assets/*`.

Key comparison SHALL be constant-time and SHALL iterate the full configured key set on every request so neither match latency nor early exit reveals the matching position.

The server SHALL increment `kube_state_graph_auth_rejected_total{reason="missing"}` on requests without the header and `kube_state_graph_auth_rejected_total{reason="invalid"}` on requests whose header value is unknown.

When `--api-keys-file` is set and `--api-keys-reload-interval` is positive, the server SHALL re-read the file on the configured cadence and atomically swap the active key set. A key removed from the file SHALL be rejected on subsequent requests; a key added SHALL be accepted.

#### Scenario: Missing header is rejected

- **WHEN** the server is started with `--api-keys=k1` and a client sends `GET /v1/graph?start=...&end=...` with no `X-API-Key`
- **THEN** the response is `401 Unauthorized` with body `{"error":{"reason":"unauthorized", ...}}`

#### Scenario: Wrong key is rejected

- **WHEN** the server is started with `--api-keys=k1` and a client sends `X-API-Key: wrong`
- **THEN** the response is `401 Unauthorized` with reason `unauthorized`

#### Scenario: Valid key is accepted

- **WHEN** the server is started with `--api-keys=k1,k2` and a client sends `X-API-Key: k2` to `/v1/edge-types`
- **THEN** the response is `200 OK` with the edge-type catalogue

#### Scenario: Open paths bypass auth even when keys are configured

- **WHEN** the server is started with keys configured and a client sends `GET /livez` / `GET /metrics` / `GET /docs` with no header
- **THEN** the response is `200 OK` (open routes ignore auth)

#### Scenario: Auth disabled when no keys configured

- **WHEN** the server is started with neither `--api-keys-file` nor `--api-keys` set
- **THEN** every route, including `/v1/graph`, accepts requests with no `X-API-Key` header, and the server boot log emits a warning identifying disabled auth

#### Scenario: Hot reload picks up rotated keys

- **WHEN** the operator updates `--api-keys-file` content (e.g., a Kubernetes `Secret` rotation propagates) and `--api-keys-reload-interval` elapses
- **THEN** subsequent requests presenting a key newly added to the file are accepted, and subsequent requests presenting a key removed from the file are rejected, all without process restart

### Requirement: Health endpoints

The server SHALL expose `GET /livez` that returns 200 while the process is running, and `GET /readyz` that returns 200 only when a 1-second `up{}` probe against the centralised VictoriaMetrics succeeds. `GET /readyz` SHALL return 503 otherwise.

#### Scenario: livez always healthy while running

- **WHEN** a client sends `GET /livez`
- **THEN** the response is 200 with body `"ok"` regardless of upstream state

#### Scenario: readyz fails when upstream unreachable

- **WHEN** the configured VictoriaMetrics URL refuses connections and a client sends `GET /readyz`
- **THEN** the response is 503 with a JSON body containing a `reason` field

### Requirement: Self-metrics endpoint

The server SHALL expose `GET /metrics` in Prometheus exposition format including at least: `kube_state_graph_build_duration_seconds`, `kube_state_graph_project_duration_seconds`, `kube_state_graph_serialise_duration_seconds`, `kube_state_graph_build_rejected_total`, `kube_state_graph_graph_node_count`, `kube_state_graph_graph_edge_count`, `kube_state_graph_clusters_observed`, `kube_state_graph_upstream_query_duration_seconds`, `kube_state_graph_upstream_query_failures_total`, `kube_state_graph_http_requests_total`, and `kube_state_graph_auth_rejected_total`.

#### Scenario: Metrics exposition

- **WHEN** a client sends `GET /metrics`
- **THEN** the response is 200 in `text/plain; version=0.0.4` exposition format and includes all metric names listed above

#### Scenario: cluster label on observational gauges

- **WHEN** a build has produced a multi-cluster graph
- **THEN** `kube_state_graph_graph_node_count` series include a `cluster` label and `kube_state_graph_graph_edge_count` series include a `cross_cluster` label

### Requirement: Per-build timeout (graph endpoints)

For `GET /v1/graph` and `GET /v1/graph/nodegraph`, the server SHALL apply a configurable per-build `context.WithTimeout` derived from `--build-timeout` (default 15 seconds). On `context.DeadlineExceeded`, the build SHALL be aborted, the `kube_state_graph_build_rejected_total{reason="timeout"}` counter SHALL be incremented, and the request SHALL receive `504 Gateway Timeout` with `reason: "timeout"` (RFC 9110 §15.6.5: gateway did not receive a timely response from an upstream server it needed to access in order to complete the request).

#### Scenario: Upstream stalls beyond build timeout

- **WHEN** centralised VictoriaMetrics fails to respond to a `/v1/graph` build within `--build-timeout`
- **THEN** the request returns 504 with `reason: "timeout"`

### Requirement: Per-request timeout (non-graph endpoints)

For non-graph endpoints that perform upstream calls (`GET /v1/clusters` discovery query, `GET /readyz` `up{}` probe), the server SHALL apply a `context.WithTimeout` derived from `--api-timeout` (default 5 seconds) to the upstream call. On `context.DeadlineExceeded`, the request SHALL receive `504 Gateway Timeout` with `reason: "timeout"`. Endpoints that do not perform upstream calls (`GET /v1/edge-types`, `GET /livez`, `GET /metrics`, `GET /openapi.*`, `GET /docs*`) are not subject to this timeout.

#### Scenario: Cluster discovery stalls beyond api timeout

- **WHEN** centralised VictoriaMetrics fails to respond to the `/v1/clusters` discovery query within `--api-timeout`
- **THEN** the request returns 504 with `reason: "timeout"`

### Requirement: Outside-retention error

When a topology query for the requested window returns zero rows but the upstream VictoriaMetrics is reachable (a parallel `up{}` probe succeeds), the server SHALL respond `400 Bad Request` with `reason: "outside_retention"`.

#### Scenario: Window beyond retention

- **WHEN** a client requests a window older than upstream `kube_pod_info` retention but `up{}` returns 1
- **THEN** the response is 400 with `reason: "outside_retention"`

### Requirement: Structured request logging

Every served HTTP request SHALL emit exactly one structured log line via `log/slog` JSON handler containing at least `method`, `path`, `status`, `duration_ms`, `request_id`, and applied `cluster` filter values.

#### Scenario: Request log line

- **WHEN** the server serves a request
- **THEN** stdout receives a JSON object with the listed fields and a top-level `level` field set to `INFO` for non-error responses

### Requirement: OpenAPI specification served by the API

The server SHALL serve the auto-generated OpenAPI 3.0 specification at two routes:

- `GET /openapi.yaml` SHALL return the YAML form with `Content-Type: application/yaml`.
- `GET /openapi.json` SHALL return the JSON form with `Content-Type: application/json`.

Both responses SHALL carry `Cache-Control: public, max-age=3600`. The spec SHALL be generated from handler annotations via `swaggo/swag` v2; the generated `docs/swagger.{json,yaml,go}` artefacts SHALL be checked into the repository.

#### Scenario: YAML spec served

- **WHEN** a client sends `GET /openapi.yaml`
- **THEN** the response is 200 with `Content-Type: application/yaml` and a body whose first non-empty line begins with `openapi:`

#### Scenario: JSON spec served

- **WHEN** a client sends `GET /openapi.json`
- **THEN** the response is 200 with `Content-Type: application/json` and a body whose top-level object contains an `"openapi"` key

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
- Every Gin route registered in `Server.Handler()` corresponds to a `(method, path)` pair declared in the spec, modulo an explicit allowlist of infrastructure paths (`/livez`, `/readyz`, `/metrics`, `/openapi.yaml`, `/openapi.json`, `/docs`, `/docs/assets/*`).

The test SHALL run on every PR via `go test ./...` and SHALL fail when annotations and routes drift.

#### Scenario: Handler added without annotation

- **WHEN** a contributor adds a new `/v1/<route>` handler without `// @Router` and `// @Summary` annotations
- **THEN** running `go test ./internal/api/` fails with a clear message naming the undocumented route

#### Scenario: Annotation pointing at removed route

- **WHEN** a contributor removes a Gin route but leaves the corresponding `// @Router` annotation in place (and forgets to regenerate the spec)
- **THEN** running `go test ./internal/api/` fails with a clear message naming the orphan documented path
