## MODIFIED Requirements

### Requirement: Versioned route prefix

The HTTP API SHALL expose every endpoint under the `/v1/` route prefix and SHALL include `apiVersion: "v1"` as a top-level field in every JSON response body.

#### Scenario: Body carries apiVersion

- **WHEN** a client sends `GET /v1/graph?start=...&end=...`
- **THEN** the server returns 200 with a JSON body whose top-level object contains `"apiVersion": "v1"`

#### Scenario: Unversioned route is not served

- **WHEN** a client sends `GET /graph?start=...&end=...`
- **THEN** the server returns 404 Not Found

### Requirement: Cytoscape.js response shape

`GET /v1/graph` SHALL return a JSON document in Cytoscape.js shape: `{ apiVersion, clusters, elements: { nodes, edges } }`. The body SHALL NOT contain time-varying or echo-of-input fields, so identical inputs against the same upstream state produce byte-identical bodies. The top-level `clusters` array SHALL list **Kubernetes** cluster identities only — the `<az>-<env>-<cluster>` identity of the `cluster-topology-source` capability ("Cluster identity composed from zone and environment labels"), or the raw name for a cluster that composed none — and an ONTAP cluster name (the `ontap_cluster` label of `netapp-aggr` / `netapp-node` nodes) SHALL NEVER appear in it. An identity is NOT a valid `?cluster=` value (see "Filter parameters"); a client that wants to narrow to one listed cluster sends its three components as `az`, `env` and `cluster`.

Each **node** SHALL be `{ data: { id, name, type, labels } }`:
- `id` SHALL be a cluster-scoped composite for pods / K8s nodes / PVCs / services (pods: `<cluster>/<pod-uid>`; nodes: `<cluster>/<node-name>`; PVCs: `<cluster>/<namespace>/<claim>`; services: `<cluster>/<namespace>/<service>`), where `<cluster>` is the cluster identity. For NetApp aggregates, `id` SHALL be `netapp/<ontap-cluster>/aggr/<aggr>`; for NetApp nodes, `netapp/<ontap-cluster>/<node>` (neither carries a Kubernetes cluster prefix). For external nodes (unresolvable `"://"` connection-string endpoints or missing-UID human-label fallback), `id` SHALL be `external/<label-value>` (no cluster prefix).
- `name` SHALL be the human-readable pod / node / PVC / service name; for NetApp aggregates, the ONTAP aggregate name; for NetApp nodes, the ONTAP controller name. For external nodes, `name` SHALL be the verbatim `client` or `server` label value from the source service-graph series.
- `type` SHALL be one of the strings `"pod"`, `"node"`, `"pvc"`, `"service"`, `"external"`, `"netapp-aggr"`, `"netapp-node"`. The Cytoscape serialiser additionally synthesises `"cluster"`, `"storage-cluster"`, `"namespace"`, `"application"`, and `"controller"` group nodes for compound nesting (see "Cytoscape compound node grouping").
- `data` MAY carry an optional `parent` field (`omitempty`) referencing the `id` of the node's Cytoscape compound container — see "Cytoscape compound node grouping".
- `labels` SHALL be a JSON object whose values are strings only (`map[string]string`). For pod / K8s node / PVC / service nodes it SHALL include at minimum a `cluster` entry carrying the cluster identity — the same value as the `id` prefix; for pods, PVCs, and services it SHALL also include a `namespace` entry; for pods it SHALL include `node` (the cluster-scoped node ID, identity-prefixed), and SHALL include `pod_ip` and `host_ip` whenever the upstream `kube_pod_info` series carried them; for K8s nodes it SHALL include `external_ip` when the upstream provided one. **For NetApp aggregates**, `labels` SHALL be exactly `{ontap_cluster, node}` (the owning controller); **for NetApp nodes**, exactly `{ontap_cluster}` — deliberately no `cluster` key on either. **For external nodes**, `labels` SHALL be an empty object `{}` (no `cluster` key).

Each **edge** SHALL be `{ data: { id, type, source, target, labels } }`:
- `id` SHALL be a UUID, RFC 4122 compliant, encoded as a lowercase canonical string.
- `type` SHALL be one of the edge types this endpoint produces — `pod-mounts-pvc`, `pod-calls-pod`, `pod-calls-service`, `service-selects-pod`, `pod-to-node`, `pvc-to-netapp-aggr` — each defined by the `pod-service-graph`, `cluster-topology-source`, and `netapp-storage-graph` capabilities. `storage-flow` is produced by `GET /v1/storage-graph` alone and SHALL NOT appear in a `/v1/graph` body. The set is a fixed contract of this specification; there is no discovery endpoint for it.
- `source` and `target` SHALL each match the `id` of a node present in the same response's `elements.nodes`.
- `labels` SHALL be a JSON object whose values are strings only (`map[string]string`). The exact key set per edge type is defined by the `pod-service-graph`, `cluster-topology-source`, and `netapp-storage-graph` capabilities. Wherever an edge carries a `cluster` key its value is a cluster identity, never a raw name that appears on no node of the body.
- `data` MAY carry an optional `metrics` object (`omitempty`) holding the edge's measurements — see "Edge `metrics` attribute".

Implementations SHALL NOT encode booleans or numbers as strings inside `labels`. Boolean flags remain deferred to a future typed field and are NOT part of the v1 contract. Numeric measurements are carried exclusively on the typed `data.metrics` object defined below — never inside `labels`.

#### Scenario: Pod node payload

- **WHEN** the response contains a pod node
- **THEN** its `data.type` equals `"pod"`, its `data.id` matches `<cluster>/<pod-uid>`, its `data.name` equals the pod's metadata name, and `data.labels.cluster` matches the cluster prefix in the ID

#### Scenario: Composed identity on every cluster-scoped element

- **WHEN** the build composed `zone-a-prod-cluster-alpha` for the raw cluster `cluster-alpha` and the response contains a pod, a K8s node, a PVC and a service of it
- **THEN** each `data.id` begins `zone-a-prod-cluster-alpha/`, each `data.labels.cluster` equals `zone-a-prod-cluster-alpha`, the pod's `data.labels.node` is `zone-a-prod-cluster-alpha/<node-name>`, the cluster group node is `{ id: "cluster/zone-a-prod-cluster-alpha", name: "zone-a-prod-cluster-alpha", type: "cluster" }`, the namespace groups beneath it are `zone-a-prod-cluster-alpha/namespace/<ns>`, and `clusters` is `["zone-a-prod-cluster-alpha"]` — the string `cluster-alpha` appears nowhere in the body

#### Scenario: Same raw name in two zones renders two clusters

- **WHEN** the build composed `us-dev-c1` and `eu-prod-c1` from the raw name `c1`
- **THEN** the response contains two cluster group nodes `cluster/us-dev-c1` and `cluster/eu-prod-c1`, `clusters` is `["eu-prod-c1","us-dev-c1"]`, and no node or edge carries `labels.cluster: "c1"`

#### Scenario: Unstamped cluster renders unchanged

- **WHEN** no series of `cluster-beta` carries both a zone and an environment label
- **THEN** every `cluster-beta` id, label and group is byte-identical to the body produced before cluster identities existed

#### Scenario: Pod node payload includes pod_ip and host_ip when upstream emits them

- **WHEN** the response contains a pod node whose source `kube_pod_info` series carried `pod_ip` and `host_ip`
- **THEN** `data.labels.pod_ip` equals the upstream `pod_ip` value and `data.labels.host_ip` equals the upstream `host_ip` value

#### Scenario: K8s node payload

- **WHEN** the response contains a Kubernetes-node node
- **THEN** its `data.type` equals `"node"`, its `data.id` matches `<cluster>/<node-name>`, its `data.name` equals the node's metadata name, and `data.labels.external_ip` is present whenever the upstream metric provided one

#### Scenario: PVC node payload

- **WHEN** the response contains a PVC node
- **THEN** its `data.type` equals `"pvc"`, its `data.id` matches `<cluster>/<namespace>/<claim>`, its `data.name` equals the claim name, and `data.labels.namespace` equals the PVC namespace

#### Scenario: PVC node carries no storageclass attribute

- **WHEN** the response contains a PVC node whose StorageClass was resolved from `kube_persistentvolumeclaim_info`
- **THEN** the former prohibition this scenario named is lifted: the PVC node's `data.storageclass` equals the resolved name (see "PVC `storageclass` and `usage` attributes"), its `labels` still has no `storageclass` key, and no `type="storageclass"` node or `pvc-to-storageclass` edge exists anywhere in the response

#### Scenario: ONTAP cluster names never appear in clusters[]

- **WHEN** the response contains a `netapp-aggr` or `netapp-node` node with `labels.ontap_cluster="ontap-prod"`
- **THEN** the top-level `clusters` array does not contain `"ontap-prod"` (it lists Kubernetes cluster names only)

#### Scenario: Service node payload

- **WHEN** the response contains a service node (a connection-string endpoint that resolved to an in-cluster service via `kube_service_info`)
- **THEN** its `data.type` equals `"service"`, its `data.id` matches `<cluster>/<namespace>/<service>`, its `data.name` equals the service name, `data.labels.cluster` matches the cluster prefix in the ID, `data.labels.namespace` equals the service namespace, and `data.ipaddress` equals `[cluster_ip]` whenever the upstream `kube_service_info` `cluster_ip` value is not `"None"`

#### Scenario: External node payload (unresolvable connection-string endpoint)

- **WHEN** the response contains an external node produced by an unresolvable `"://"` connection-string endpoint (a `client` or `server` label containing `"://"` whose host did not resolve to an in-cluster service)
- **THEN** its `data.type` equals `"external"`, its `data.id` equals `external/<value>`, its `data.name` equals `<value>` (the verbatim service-graph `client` or `server` label), and `data.labels` equals `{}`

#### Scenario: External node payload (missing-UID fallback)

- **WHEN** the response contains an external node produced by the missing-UID human-label fallback (a service-graph series whose `client_k8s_pod_uid` or `server_k8s_pod_uid` was empty but the corresponding `client`/`server` label was populated and contained no `"://"`)
- **THEN** its `data.type` equals `"external"`, its `data.id` equals `external/<value>`, its `data.name` equals `<value>`, and `data.labels` equals `{}`

#### Scenario: Edge payload references existing nodes

- **WHEN** the response contains any edge
- **THEN** both `data.source` and `data.target` SHALL match the `data.id` of a node present in the same response's `elements.nodes`

#### Scenario: Edge id is a UUID

- **WHEN** the response contains any edge
- **THEN** `data.id` matches the regex `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

#### Scenario: Edge id is stable across rebuilds

- **WHEN** the same logical edge (same `type`, `source`, `target`) is produced by two consecutive builds for the same time bucket
- **THEN** `data.id` is byte-identical between the two builds

#### Scenario: Edge labels never carry numbers

- **WHEN** the response contains an edge that carries a `data.metrics` object
- **THEN** its `data.labels` still contains only string values and no `rate`, `error_rate`, `p90_server_ms`, `read_ops`, `write_ops`, `read_latency_us`, `write_latency_us`, `read_bytes_per_sec`, `write_bytes_per_sec`, `max_iops`, or `max_bytes_per_sec` key

#### Scenario: storage-flow never appears in a graph body

- **WHEN** a client sends any `GET /v1/graph` request
- **THEN** no edge in the response has `data.type` equal to `"storage-flow"`

### Requirement: Filter parameters

`GET /v1/graph` SHALL accept the optional filter parameters `cluster`, `namespace`, `az`, `env` (each repeatable) and `prune` (single-valued, `true` | `false`, default `true`). The request surface is exactly `start`, `end`, `cluster`, `namespace`, `az`, `env`, `prune`; every parameter except `start` / `end` is optional. Multiple values for the same parameter SHALL be OR-combined; different parameters SHALL be AND-combined. An unknown filter **value** (a cluster, namespace, zone, or environment with no data) SHALL NOT cause an error — it yields an empty result. An unknown filter **parameter** — including the withdrawn `name`, `root`, `depth`, `direction` and `edge_type` — SHALL be ignored without error, whatever value it carries: a withdrawn parameter never narrows the body and never rejects the request.

The `cluster` value is the **raw** Kubernetes cluster name — the value of the upstream `cluster` label — not the composed identity the response lists in `clusters[]`. A raw name selects every cluster identity whose raw component equals it, across zones and environments; the triple `az` + `env` + `cluster` pins exactly one identity, because those three request dimensions are the identity's three components.

Filters fall into two classes:

- **Selector-level filters** — `cluster`, `namespace`, `az`, `env` — SHALL be rendered into the upstream PromQL queries of the build as label matchers, so the graph is narrowed by VictoriaMetrics before any sample is read. Which matcher reaches which series is the hardcoded contract of the `cluster-topology-source` capability ("Request-scoped upstream selectors"); the service-graph series are deliberately read unfiltered (`pod-service-graph`). A request with no selector-level filter SHALL issue exactly the queries it issues today and produce a byte-identical body.
- **Projection-level filters** — `cluster` and `namespace` (applied again over the built graph as defence in depth) and `prune` — SHALL be applied at response time as a projection over the freshly built graph. The projection-level `cluster` check compares the request's raw values against the **raw-name component** of each element's cluster identity (recovered from the built graph's identity table; an identity absent from the table compares as itself), so the projection admits exactly what the upstream matcher admitted. There is no projection over edge type: every edge of the built graph whose endpoints survive node filtering is emitted, and a client wanting fewer edge types drops them by `data.type`.

Empty filters SHALL return the **connectivity-connected subgraph** of the full multi-cluster graph for the time window (the default connectivity prune — see "Default projection prunes connectivity-disconnected workload"); it is NOT the full topology inventory. `prune=false` returns the inventory instead.

Selector-level values SHALL be validated before rendering: a value longer than 253 bytes, containing a control character (including newline), or — for `prune` — not exactly `true` / `false`, SHALL be rejected with `400 Bad Request` and `reason: "invalid_scope"`. A single value SHALL render an exact matcher (`<key>="<value>"`, with `"` and `\` escaped); several values for one parameter SHALL render one fully-anchored alternation (`<key>=~"<v1>|<v2>"`) over the sorted, de-duplicated, regex-quoted values. The request value `cluster=unknown` SHALL render `cluster=~"unknown|"` so that both spellings of the bucket — a series carrying no `cluster` label and one whose label is literally `unknown` — remain addressable, matching what the projection filter accepts.

**Edge retention rule (unified across all filters).** An edge SHALL be retained when at least one resolved endpoint is in scope after node filtering. When exactly one endpoint is in scope, the missing endpoint SHALL be re-added from the freshly built graph's node index provided it passes the non-cluster filters (namespace check; types without a namespace label — `node`, `external`, and the NetApp types `netapp-aggr` / `netapp-node` (which carry neither a namespace nor a `cluster` label) — pass through). This single rule is edge-type-agnostic and covers non-pod endpoints incident on in-scope pods — including the topology `pod-to-node` edge, the `pvc-to-netapp-aggr` edge, and the `external` partners that a filtered build produces for out-of-scope peers — and, in an **unfiltered** build, cross-cluster edges whose partner endpoint lies outside a projection-level `cluster` set.

#### Scenario: Cluster filter narrows result

- **WHEN** the upstream holds pods in `cluster-alpha` and `cluster-beta` and a client sends `?cluster=cluster-alpha`
- **THEN** every topology query carrying a `cluster` label is issued with `cluster="cluster-alpha"`, the response contains pod nodes only for `cluster-alpha`, and any `cluster-beta` peer of a `cluster-alpha` pod appears as an `external/<label>` node (not as a `cluster-beta` pod node — see "Cross-cluster edge representation")

#### Scenario: Raw cluster filter selects every zone's cluster of that name

- **WHEN** the upstream holds `c1` under `az="us",env="dev"` and `c1` under `az="eu",env="prod"` and a client sends `?cluster=c1`
- **THEN** the queries are issued with `cluster="c1"`, the response contains both `us-dev-c1` and `eu-prod-c1` workload, and `clusters` is `["eu-prod-c1","us-dev-c1"]`

#### Scenario: Zone, environment and cluster pin one identity

- **WHEN** the same upstream and a client sends `?az=us&env=dev&cluster=c1`
- **THEN** the queries are issued with `az="us",env="dev",cluster="c1"`, the response contains only `us-dev-c1` workload, and `clusters` is `["us-dev-c1"]`

#### Scenario: A listed identity is not a cluster filter value

- **WHEN** a client reads `clusters: ["us-dev-c1"]` from one response and sends `?cluster=us-dev-c1`
- **THEN** the queries are issued with `cluster="us-dev-c1"`, match no series, and the response is 200 with empty `elements` and an empty `clusters` list

#### Scenario: Namespace filter combined with cluster

- **WHEN** a client sends `?cluster=cluster-alpha&namespace=ns-x&namespace=ns-y`
- **THEN** the pod- and claim-scoped topology queries are issued with `cluster="cluster-alpha",namespace=~"ns-x|ns-y"` and the response contains pods whose cluster is `cluster-alpha` AND whose namespace is `ns-x` OR `ns-y`

#### Scenario: Zone and environment filters

- **WHEN** a client sends `?az=zone-a&env=prod`
- **THEN** every topology query is issued with `<az-key>="zone-a",<env-key>="prod"` (the keys defaulting to `az` / `env`), the service-graph queries are issued unchanged, and the response contains only the workload and infrastructure whose series carry both labels

#### Scenario: Multi-valued zone filter renders one anchored alternation

- **WHEN** a client sends `?az=zone-b&az=zone-a&az=zone-a`
- **THEN** the rendered matcher is `<az-key>=~"zone-a|zone-b"` (sorted, de-duplicated, regex-quoted) and the result is the union of both zones

#### Scenario: Invalid selector value is rejected

- **WHEN** a client sends `?env=prod%0A` (a value containing a newline) or `?prune=maybe`
- **THEN** the server returns 400 Bad Request with `reason: "invalid_scope"` and issues no upstream query

#### Scenario: Edge-type filter with no matching edges

- **WHEN** a client sends `?edge_type=pod-calls-pod` and the time window contains no service-graph data
- **THEN** the response is 200 with `elements.edges: []` and no error — because the default connectivity prune leaves nothing to draw, not because the parameter filtered anything; the body is identical to the same request without `edge_type`

#### Scenario: Withdrawn edge_type parameter is ignored

- **WHEN** a client sends `?edge_type=pod-calls-pod` for a window whose built graph holds `pod-calls-pod`, `pod-to-node` and `pod-mounts-pvc` edges
- **THEN** the response is 200 and byte-identical to the response for the same request without `edge_type` — every edge type the projection would otherwise emit is present, none is filtered out

#### Scenario: Unregistered edge_type value is not an error

- **WHEN** a client sends `?edge_type=pod-calls-pods` (a value no edge type has ever carried)
- **THEN** the response is 200 with the default view, not 400 `invalid_scope` — the parameter is unknown to the server and its value is never inspected

#### Scenario: Unknown cluster name

- **WHEN** a client sends `?cluster=does-not-exist`
- **THEN** the response is 200 with empty `elements.nodes`, empty `elements.edges`, and an empty `clusters` list

#### Scenario: Name filter matches a pod

- **WHEN** the freshly built graph contains pods named `frontend` and `backend` and a client sends `?name=frontend`
- **THEN** the `name` parameter is ignored (it is withdrawn) and the response is the default connectivity view, containing `backend` as well as `frontend`

#### Scenario: Name filter matches a K8s node

- **WHEN** a client sends `?name=worker-1`
- **THEN** the parameter is ignored; a podless node is surfaced with `?prune=false` (combined with `cluster` to bound the response), not by name

#### Scenario: Name filter matches a PVC

- **WHEN** a client sends `?name=checkout-data`
- **THEN** the parameter is ignored; an unmounted claim in namespace `shop` is surfaced with `?namespace=shop&prune=false`, not by name

#### Scenario: Name shared across types returns every match

- **WHEN** a pod and a K8s node both happen to be named `worker-1` and a client sends `?name=worker-1`
- **THEN** the parameter is ignored and the response is unaffected by the shared name

#### Scenario: Name shared across clusters returns every match

- **WHEN** a pod named `api` exists in both `cluster-alpha` and `cluster-beta` and a client sends `?name=api`
- **THEN** the parameter is ignored; a per-cluster view is obtained with `?cluster=cluster-alpha` or `?cluster=cluster-beta`

#### Scenario: Name filter combined with cluster

- **WHEN** a client sends `?name=api&cluster=cluster-alpha`
- **THEN** the `cluster` filter is applied at the source, the `name` parameter is ignored, and the response is identical to `?cluster=cluster-alpha`

#### Scenario: Name filter retains incident edges with re-hydrated partner

- **WHEN** a client sends `?name=frontend` for a window containing a cross-cluster `pod-calls-pod` edge
- **THEN** the parameter is ignored and the edge follows the "Cross-cluster edge representation" requirement for an unfiltered build (both real endpoints present)

#### Scenario: Unknown name returns empty result

- **WHEN** a client sends `?name=does-not-exist`
- **THEN** the parameter is ignored and the response is the full default connectivity view — NOT an empty result

#### Scenario: Withdrawn parameters are ignored

- **WHEN** a client sends `?name=frontend&root=cluster-alpha/abc&depth=1&edge_type=pod-calls-pod`
- **THEN** the server ignores the four parameters and returns the unanchored view for the remaining parameters (200), not a 400

#### Scenario: No selector-level filter issues today's queries

- **WHEN** a client sends `GET /v1/graph?start=...&end=...` with no other parameters
- **THEN** every upstream query is byte-identical to the query issued before request-scoped selectors existed, and the response body is byte-identical to the pre-change body for the same upstream data

### Requirement: Deterministic response body

For identical input — same `(window, filters, upstream-data)` — the server SHALL produce a byte-identical response body across rebuilds. The server SHALL NOT emit any HTTP cache validator (no `ETag`, no `Last-Modified`): cacheability is intentionally a future-iteration concern and v1 has no in-process result cache. A future cache is keyed by `(window, az, env, cluster-set, namespace-set)`; within one such key the projection-level filters remain a pure function of the built graph.

The serialiser SHALL maintain determinism by sorting `view.Nodes` and `view.Edges`, sorting `Graph.ClusterNames()`, sorting `IPAddress` slices at construction, and keeping the response body shape fixed at `{apiVersion, clusters, elements}` for graph routes (no time-of-build or echo-of-input fields). Every rendered upstream selector SHALL be a pure function of the sorted, de-duplicated parameter values.

`GET /openapi.yaml`, `GET /openapi.json`, and `GET /docs` SHALL carry an explicit `Cache-Control` header. `GET /v1/graph` SHALL NOT emit a `Cache-Control` header.

#### Scenario: Body byte-identical across repeated requests

- **WHEN** a client sends two consecutive `GET /v1/graph` requests with identical query parameters and the upstream data has not changed between them
- **THEN** both response bodies are byte-identical, even though each request triggered an independent upstream fan-out

#### Scenario: Parameter order does not change the body

- **WHEN** a client sends `?az=b&az=a&namespace=y&namespace=x` and then `?namespace=x&namespace=y&az=a&az=b` for the same window and upstream data
- **THEN** both requests render identical upstream selectors and return byte-identical bodies

### Requirement: API-key authentication on `/v1/*` and `/debug/*`

When the server is started with at least one API key configured (via `--api-keys-file` or `--api-keys`), every request to `/v1/*` and `/debug/*` SHALL carry an `X-API-Key: <key>` header. Requests without the header SHALL receive `401 Unauthorized` with reason `unauthorized` and a JSON message indicating the missing header. Requests with a header value that is not present in the configured key set SHALL receive `401 Unauthorized` with reason `unauthorized`.

When no keys are configured (both flags empty), the middleware SHALL be a no-op: every route SHALL behave as if auth were not configured. The server SHALL log a warning at boot identifying that auth is disabled.

The following routes SHALL be exempt from authentication regardless of configuration: `/livez`, `/readyz`, `/metrics`, `/openapi.yaml`, `/openapi.json`, and `/docs`.

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

- **WHEN** the server is started with `--api-keys=k1,k2` and a client sends `X-API-Key: k2` to `GET /v1/graph?start=...&end=...`
- **THEN** the response is `200 OK` with a Cytoscape.js graph body

#### Scenario: Open paths bypass auth even when keys are configured

- **WHEN** the server is started with keys configured and a client sends `GET /livez` / `GET /metrics` / `GET /docs` with no header
- **THEN** the response is `200 OK` (open routes ignore auth)

#### Scenario: Auth disabled when no keys configured

- **WHEN** the server is started with neither `--api-keys-file` nor `--api-keys` set
- **THEN** every route, including `/v1/graph`, accepts requests with no `X-API-Key` header, and the server boot log emits a warning identifying disabled auth

#### Scenario: Hot reload picks up rotated keys

- **WHEN** the operator updates `--api-keys-file` content (e.g., a Kubernetes `Secret` rotation propagates) and `--api-keys-reload-interval` elapses
- **THEN** subsequent requests presenting a key newly added to the file are accepted, and subsequent requests presenting a key removed from the file are rejected, all without process restart

### Requirement: Per-request timeout (non-graph endpoints)

For non-graph endpoints that perform upstream calls (`GET /readyz` `up{}` probe), the server SHALL apply a `context.WithTimeout` derived from `--api-timeout` (default 5 seconds) to the upstream call. On `context.DeadlineExceeded`, the request SHALL receive `504 Gateway Timeout` with `reason: "timeout"`. The same timeout bounds the build's `up{}` retention probe. Endpoints that do not perform upstream calls (`GET /livez`, `GET /metrics`, `GET /openapi.*`, `GET /docs*`) are not subject to this timeout.

#### Scenario: Readiness probe stalls beyond api timeout

- **WHEN** centralised VictoriaMetrics fails to respond to the `/readyz` `up{}` probe within `--api-timeout`
- **THEN** the request returns 504 with `reason: "timeout"`

#### Scenario: Cluster discovery stalls beyond api timeout

- **WHEN** a client sends `GET /v1/clusters` while centralised VictoriaMetrics is unresponsive
- **THEN** the request returns 404 Not Found immediately — the endpoint is removed, no upstream call is made, and the api timeout does not apply

#### Scenario: Edge-type catalogue request while upstream is unresponsive

- **WHEN** a client sends `GET /v1/edge-types` while centralised VictoriaMetrics is unresponsive
- **THEN** the request returns 404 Not Found immediately with the standard error body — the endpoint is removed, no upstream call is made, and the api timeout does not apply

## REMOVED Requirements

### Requirement: Edge-type discovery endpoint

**Reason**: `GET /v1/edge-types` existed to populate and validate the `edge_type` filter of `GET /v1/graph`, which is withdrawn by this change; its only known consumer, the `kube-state-graph-frontend` SPA, has dropped both the control and the catalogue request. With no parameter to validate against it, the catalogue is a second, prose-heavy description of the edge types that the OpenAPI document and the per-capability specifications already define, and every new edge type had to be documented in it for an audience that no longer exists.

**Migration**: There is no replacement endpoint. The set of edge types a `/v1/graph` body can carry is fixed by the "Cytoscape.js response shape" requirement (`pod-mounts-pvc`, `pod-calls-pod`, `pod-calls-service`, `service-selects-pod`, `pod-to-node`, `pvc-to-netapp-aggr`); `GET /v1/storage-graph` carries `storage-flow` only. A client that drew a legend or validated input from the catalogue reads `data.type` off the edges it receives instead, and a client that narrowed by `edge_type` drops edges by `data.type` after the fact. The path now returns the standard 404 body, exactly as the removed `GET /v1/clusters` does.
