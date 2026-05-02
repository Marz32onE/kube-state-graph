## ADDED Requirements

### Requirement: Pod-UID-resolved edge source

The pod-service-graph reader SHALL build edges from service-graph metrics scraped into centralised VictoriaMetrics. The reader SHALL consume at minimum the following series, joined by pod UID:

- `traces_service_graph_request_total{client, server, client_cluster, server_cluster, client_k8s_pod_uid, server_k8s_pod_uid, client_k8s_namespace_name, server_k8s_namespace_name, connection_type}`
- `traces_service_graph_request_failed_total{ ...same labels... }`
- `traces_service_graph_request_server_seconds_bucket{ ...same labels..., le }`

Edges SHALL be derived by computing `rate(...[<window>]) @ <end>` over each counter and joining the result back to topology by `(cluster, pod-uid)` on each end. The `client` and `server` string labels are consumed by the external-endpoint substitution rule (see "External-endpoint substitution") and are otherwise ignored for pod-resolved endpoints.

#### Scenario: Edge produced from non-zero rate

- **WHEN** for the requested window `rate(traces_service_graph_request_total{...})` is greater than zero for a series whose endpoints are present in topology
- **THEN** the reader emits one `pod-calls-pod` edge whose `source` is `<client_cluster>/<client_k8s_pod_uid>` and `target` is `<server_cluster>/<server_k8s_pod_uid>`

#### Scenario: Zero-rate series is dropped

- **WHEN** `rate(traces_service_graph_request_total{...})` evaluates to exactly zero for a series in the window
- **THEN** no edge is emitted for that series

### Requirement: Cross-cluster edge support

For every emitted `pod-calls-pod` edge, the reader SHALL set `labels.client_cluster = <client_cluster>` and `labels.server_cluster = <server_cluster>` from the source series. Both labels SHALL be present on every `pod-calls-pod` edge regardless of whether the call is intra-cluster or cross-cluster, so consumers can detect cross-cluster status by simple string comparison. The reader SHALL NOT encode a `cross_cluster` boolean inside `labels` (booleans are deferred to a future typed field).

#### Scenario: Cross-cluster RPC

- **WHEN** the reader processes a series with `client_cluster="cluster-alpha"` and `server_cluster="cluster-beta"`
- **THEN** the emitted edge has `labels.client_cluster: "cluster-alpha"` and `labels.server_cluster: "cluster-beta"` and contains no `cross_cluster` key under `labels`

#### Scenario: Intra-cluster RPC

- **WHEN** the reader processes a series with `client_cluster="cluster-alpha"` and `server_cluster="cluster-alpha"`
- **THEN** the emitted edge has `labels.client_cluster: "cluster-alpha"` and `labels.server_cluster: "cluster-alpha"` and contains no `cross_cluster` key under `labels`

### Requirement: Numeric metrics deferred from v1

The reader SHALL NOT attach numeric edge metrics (`rate`, `p99_ms`, `error_rate`, etc.) to the v1 edge `labels` map, because `labels` is strictly `map[string]string` per the `graph-api` capability. Numeric metrics are deferred to a future typed struct field added in a later spec revision; v1 edges expose only string labels.

#### Scenario: No numeric labels emitted in v1

- **WHEN** the reader emits any `pod-calls-pod` edge in v1
- **THEN** the edge's `labels` map contains no `rate`, no `error_rate`, and no `p99_ms` key

#### Scenario: Histogram absence does not produce labels

- **WHEN** `traces_service_graph_request_server_seconds_bucket` is present for a series
- **THEN** the reader still emits no `p99_ms` key on the edge's `labels` map (numeric metrics are out of scope for v1)

### Requirement: Empty / sparse data tolerance

The reader SHALL treat an empty or sparse service-graph dataset as a valid input. When all service-graph queries return zero series, the build SHALL still complete successfully with zero `pod-calls-pod` edges, and no error SHALL be emitted.

#### Scenario: Window with no service-graph data

- **WHEN** centralised VictoriaMetrics has no `traces_service_graph_*` series in the requested window but topology queries return data
- **THEN** the build completes with a graph containing topology nodes, zero `pod-calls-pod` edges, and a 200 response

### Requirement: External-endpoint substitution

The reader SHALL read a pattern substring from the env var `KSG_EXTERNAL_NAME_PATTERN` (also accepted as `--external-name-pattern`). The pattern SHALL default to the empty string, which disables the rule. When non-empty, for each service-graph series the reader SHALL evaluate the rule independently for the client side and the server side as follows:

1. Let `label_value` be the series' `client` (or `server`) label value.
2. If `label_value` contains the pattern substring, treat that endpoint as an **external** node:
   - `id`     = `external/<label_value>`
   - `name`   = `<label_value>` (verbatim — no normalisation, no trimming, no scheme parsing)
   - `type`   = `"external"`
   - `labels` = `{ "pattern": "<KSG_EXTERNAL_NAME_PATTERN>" }`
3. Otherwise, resolve the endpoint as a pod via `(client_cluster, client_k8s_pod_uid)` (or `(server_cluster, server_k8s_pod_uid)`) against topology, falling through to the synthesised pod node fallback when topology has no entry.

When the pattern is empty (rule disabled), the reader SHALL NOT inspect the `client` / `server` labels for substitution and SHALL resolve all endpoints via pod UID.

The decision is per endpoint: a single edge MAY have a pod source and an external target, an external source and a pod target, two pods, or two external nodes. The edge `type` SHALL remain `pod-calls-pod` regardless of endpoint kinds. For an external endpoint, the corresponding `client_cluster` (or `server_cluster`) value on `labels` SHALL be the empty string `""`.

#### Scenario: Pattern unset disables the rule

- **WHEN** the server starts without `KSG_EXTERNAL_NAME_PATTERN` and the upstream contains a series with `client="http://api.example.com"`
- **THEN** the resulting edge resolves the client endpoint via `(client_cluster, client_k8s_pod_uid)` and emits no external node

#### Scenario: Client side matches pattern

- **WHEN** the server is started with `KSG_EXTERNAL_NAME_PATTERN="://"` and the upstream contains a series with `client="http://api.example.com"`, `server="checkout"`, `server_cluster="cluster-alpha"`, `server_k8s_pod_uid="abc"`
- **THEN** the resulting edge has `type: "pod-calls-pod"`, `source: "external/http://api.example.com"`, `target: "cluster-alpha/abc"`; the source node has `type: "external"`, `name: "http://api.example.com"`, `labels.pattern: "://"`, and contains no `cluster` key under `labels`

#### Scenario: Server side matches pattern

- **WHEN** the server is started with `KSG_EXTERNAL_NAME_PATTERN="://"` and the upstream contains a series with `client="checkout"`, `server="https://payments.partner.example/api"`, `client_cluster="cluster-alpha"`, `client_k8s_pod_uid="abc"`
- **THEN** the resulting edge has `target: "external/https://payments.partner.example/api"`; the target node has `type: "external"`, `name: "https://payments.partner.example/api"`

#### Scenario: Both sides match pattern

- **WHEN** the configured pattern is `"://"` and a series has both `client` and `server` containing `://`
- **THEN** the resulting edge has both source and target as external nodes and the edge `type` is still `"pod-calls-pod"`

#### Scenario: Pattern does not match, falls through to pod resolution

- **WHEN** the configured pattern is `"://"` and a series has `client="checkout"` (no `://` in the value)
- **THEN** the client endpoint resolves to a pod via `(client_cluster, client_k8s_pod_uid)` exactly as if the pattern were unset

#### Scenario: External endpoint cluster label is empty

- **WHEN** the reader emits an edge whose source endpoint is external
- **THEN** the edge's `labels.client_cluster` is the empty string `""` and `labels.server_cluster` is the server endpoint's cluster

### Requirement: Synthesised pod node fallback

When a service-graph series references a `(cluster, pod-uid)` endpoint that does not appear in the topology produced for the same window, the reader SHALL synthesise a pod node with `id="<cluster>/<pod-uid>"`, `name="<pod-uid>"` (the pod UID stands in as the display name when no metadata name is available), `type="pod"`, and a `labels` map containing at least `cluster` and the `namespace` value when the metric provided one. The build SHALL NOT drop the edge. The reader SHALL NOT add a boolean `ghost` flag to `labels`; consumers detect synthesised endpoints by the absence of richer labels (e.g., missing `node` for pods).

#### Scenario: Server pod missing from topology

- **WHEN** a service-graph series has `server_cluster="cluster-beta"` and `server_k8s_pod_uid="missing-uid"` but no `kube_pod_info{cluster="cluster-beta", uid="missing-uid"}` exists in the window
- **THEN** the resulting graph contains a synthesised pod node with `id: "cluster-beta/missing-uid"`, `name: "missing-uid"`, `type: "pod"`, `labels.cluster: "cluster-beta"`, no `labels.ghost` key, and the edge is emitted with this node as `target`

### Requirement: Allowlist enforcement on service-graph queries

When the server is configured with `--clusters-allowlist`, every service-graph query SHALL inject the allowlist regex on **both** `client_cluster=~"..."` and `server_cluster=~"..."` so that edges entirely outside the allowlist are not fetched. Cross-cluster edges where exactly one endpoint is inside the allowlist SHALL be excluded for v1 because the remote endpoint cannot be resolved against an out-of-scope cluster.

#### Scenario: Both endpoints inside allowlist

- **WHEN** the allowlist is `{cluster-alpha, cluster-beta}` and a series has `client_cluster="cluster-alpha"`, `server_cluster="cluster-beta"`
- **THEN** the edge is fetched, joined, and emitted

#### Scenario: One endpoint outside allowlist

- **WHEN** the allowlist is `{cluster-alpha}` and a series has `client_cluster="cluster-alpha"`, `server_cluster="cluster-gamma"`
- **THEN** the issued PromQL excludes this series via the allowlist regex on `server_cluster` and no edge is emitted

### Requirement: Edge identity is a deterministic UUID

Edge IDs SHALL be RFC 4122 UUIDs in canonical lowercase string form. They SHALL be deterministic UUIDv5 values derived from a fixed namespace UUID compiled into the binary and the canonical input string `"<type>|<source>|<target>"` (where `<source>` and `<target>` are the cluster-scoped node IDs). Two builds that produce the same logical edge SHALL produce byte-identical edge IDs.

#### Scenario: Stable edge ID across rebuilds

- **WHEN** the same input series produces an edge in two consecutive builds for the same time bucket
- **THEN** the edge `id` is byte-identical between the two builds

#### Scenario: Edge ID is RFC 4122 compliant

- **WHEN** the reader emits any edge
- **THEN** the edge `id` matches the regex `^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$` (UUIDv5 in lowercase canonical form)

#### Scenario: Different edges produce different IDs

- **WHEN** two distinct logical edges differ only in `type`, or only in `source`, or only in `target`
- **THEN** their `id` values are not equal
