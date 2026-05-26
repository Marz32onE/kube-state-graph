## ADDED Requirements

### Requirement: Pod-UID-resolved edge source

The pod-service-graph reader SHALL build edges from service-graph metrics scraped into centralised VictoriaMetrics. The reader SHALL consume at minimum the following series, joined by pod UID:

- `traces_service_graph_request_total{client, server, cluster, client_k8s_pod_uid, server_k8s_pod_uid, client_k8s_namespace_name, server_k8s_namespace_name, connection_type}`
- `traces_service_graph_request_failed_total{ ...same labels... }`
- `traces_service_graph_request_server_seconds_bucket{ ...same labels..., le }`

Each series carries exactly one `cluster` external label, applied by the trace pipeline that produced it (typically Tempo's metrics-generator running in a single source cluster). The reader SHALL treat that `cluster` value as the **client-side cluster** — the cluster originating the call — and SHALL resolve the client pod via `(cluster, client_k8s_pod_uid)`. The server-side pod SHALL be resolved by looking up `server_k8s_pod_uid` against a global pod-UID index built from topology (Kubernetes pod UIDs are unique across clusters in practice). When the server UID matches a topology pod, the resolved pod's own `cluster` value provides the server-side cluster for the edge `target` ID.

Edges SHALL be derived by computing `rate(...[<window>]) @ <end>` over each counter. The `client` and `server` string labels are consumed by the external-endpoint substitution rule (see "External-endpoint substitution") and are otherwise ignored for pod-resolved endpoints.

#### Scenario: Edge produced from non-zero rate

- **WHEN** for the requested window `rate(traces_service_graph_request_total{...})` is greater than zero for a series whose client side and server-pod-UID resolve via topology
- **THEN** the reader emits one `pod-calls-pod` edge whose `source` is `<cluster>/<client_k8s_pod_uid>` and `target` is `<resolved-cluster>/<server_k8s_pod_uid>`, where `<resolved-cluster>` is the topology-side cluster of the matched server pod

#### Scenario: Zero-rate series is dropped

- **WHEN** `rate(traces_service_graph_request_total{...})` evaluates to exactly zero for a series in the window
- **THEN** no edge is emitted for that series

### Requirement: Edge cluster label

For every emitted `pod-calls-pod` edge whose **client side resolves to a pod**, the reader SHALL set `labels.cluster = <client-pod-cluster>` from the metric's `cluster` label. The label represents the cluster that originated the RPC. When the **client side resolves to an external node**, the reader SHALL omit the `cluster` key from the edge's `labels` (external endpoints are not cluster-scoped). The reader SHALL NOT emit `client_cluster` or `server_cluster` keys on edge `labels` (server-side cluster is derivable from `target` node's `labels.cluster`). The reader SHALL NOT encode a `cross_cluster` boolean inside `labels` (booleans are deferred to a future typed field); cross-cluster status is derived by comparing the resolved source and target nodes' `labels.cluster` values.

#### Scenario: Intra-cluster RPC

- **WHEN** the reader processes a series with `cluster="cluster-alpha"` whose `client_k8s_pod_uid` and `server_k8s_pod_uid` both resolve to pods in `cluster-alpha`
- **THEN** the emitted edge has `labels.cluster: "cluster-alpha"`, the `target` node's `labels.cluster` is also `"cluster-alpha"`, and the edge contains no `client_cluster`, `server_cluster`, or `cross_cluster` key

#### Scenario: Cross-cluster RPC

- **WHEN** the reader processes a series with `cluster="cluster-alpha"` whose `client_k8s_pod_uid` resolves to a pod in `cluster-alpha` and whose `server_k8s_pod_uid` resolves via the global UID index to a pod in `cluster-beta`
- **THEN** the emitted edge has `labels.cluster: "cluster-alpha"`, `source: "cluster-alpha/<client-uid>"`, `target: "cluster-beta/<server-uid>"`, and the cross-cluster status is detectable by comparing the source and target node `labels.cluster` values

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

- **WHEN** the server is started with `KSG_EXTERNAL_NAME_PATTERN="://"` and the upstream contains a series with `client="http://api.example.com"`, `server="checkout"`, `cluster="cluster-alpha"`, `server_k8s_pod_uid="abc"` (resolving to a pod in topology with `cluster: "cluster-alpha"`)
- **THEN** the resulting edge has `type: "pod-calls-pod"`, `source: "external/http://api.example.com"`, `target: "cluster-alpha/abc"`; the source node has `type: "external"`, `name: "http://api.example.com"`, `labels.pattern: "://"`, no `cluster` key under its `labels`; and the **edge** itself contains no `cluster` key under `labels` (the client side is external)

#### Scenario: Server side matches pattern

- **WHEN** the server is started with `KSG_EXTERNAL_NAME_PATTERN="://"` and the upstream contains a series with `client="checkout"`, `server="https://payments.partner.example/api"`, `cluster="cluster-alpha"`, `client_k8s_pod_uid="abc"`
- **THEN** the resulting edge has `target: "external/https://payments.partner.example/api"`; the target node has `type: "external"`, `name: "https://payments.partner.example/api"`; and the edge has `labels.cluster: "cluster-alpha"` (the client side is a pod in `cluster-alpha`)

#### Scenario: Both sides match pattern

- **WHEN** the configured pattern is `"://"` and a series has both `client` and `server` containing `://`
- **THEN** the resulting edge has both source and target as external nodes, the edge `type` is still `"pod-calls-pod"`, and the edge `labels` contain no `cluster` key

#### Scenario: Pattern does not match, falls through to pod resolution

- **WHEN** the configured pattern is `"://"` and a series has `client="checkout"` (no `://` in the value)
- **THEN** the client endpoint resolves to a pod via `(cluster, client_k8s_pod_uid)` exactly as if the pattern were unset

### Requirement: Missing pod-UID human-label fallback

When a service-graph series lacks a pod UID for an endpoint (`client_k8s_pod_uid` or `server_k8s_pod_uid` is empty or absent) AND the corresponding human-readable label (`client` or `server`) is non-empty, the reader SHALL promote that endpoint to an **external** node derived from the human label, instead of dropping the edge.

This fallback fires AFTER the external-pattern rule (`KSG_EXTERNAL_NAME_PATTERN`) and BEFORE the synthesised-pod fallback. It is unconditionally on (no knob) and SHALL apply symmetrically to client and server sides.

For the affected endpoint, the reader SHALL produce a node with:

- `id`     = `external/<label_value>`  (no cluster prefix — the endpoint is not a pod and has no cluster identity)
- `name`   = `<label_value>` (verbatim — no normalisation, no trimming)
- `type`   = `"external"`
- `labels` = `{}` (empty map — no `cluster` key, no `pattern` key)

The `external/<label_value>` ID is intentionally identical in shape to IDs produced by the `KSG_EXTERNAL_NAME_PATTERN` rule. Two upstream series whose label values collapse to the same ID (whether produced by the pattern rule, the missing-UID fallback, or one of each) SHALL resolve to a single external node in the output.

The edge `labels.cluster` rule is unchanged: present (set to the metric's `cluster` label) when the **client** side resolves to a pod; omitted when the client side is external — whether the client became external via the pattern rule or via this missing-UID fallback.

When BOTH the pod UID AND the human label are empty for an endpoint, the reader SHALL drop the edge (no identity remains to construct any node).

The per-endpoint resolution order is:

1. `KSG_EXTERNAL_NAME_PATTERN` substring match → external node (with `labels.pattern`).
2. Pod-UID resolution against topology / synth-pod fallback (only when UID is non-empty).
3. Missing-UID human-label fallback (this requirement; only when UID is empty AND label is non-empty).
4. Drop (both UID and label empty).

#### Scenario: Client UID missing, client label promoted to external

- **WHEN** a service-graph series has `client="admin"`, `cluster="cluster-alpha"`, `server="rest-api"`, `server_k8s_pod_uid="abc"` (resolving to a pod with `cluster="cluster-alpha"`), and `client_k8s_pod_uid` is absent (empty string)
- **THEN** the resulting edge has `type: "pod-calls-pod"`, `source: "external/admin"`, `target: "cluster-alpha/abc"`; the source node has `id: "external/admin"`, `name: "admin"`, `type: "external"`, no `cluster` key under its `labels`; and the **edge** `labels` map contains no `cluster` key (client side is external)

#### Scenario: Server UID missing, server label promoted to external

- **WHEN** a service-graph series has `client="checkout"`, `cluster="cluster-alpha"`, `client_k8s_pod_uid="abc"` (resolving to a pod), `server="payments"`, and `server_k8s_pod_uid` is absent
- **THEN** the resulting edge has `target: "external/payments"`; the target node has `id: "external/payments"`, `name: "payments"`, `type: "external"`, no `cluster` key under its `labels`; and the edge has `labels.cluster: "cluster-alpha"` (the client side is still a pod)

#### Scenario: Both UIDs missing, both human labels present

- **WHEN** a series has `client="admin"`, `server="payments"`, `cluster="cluster-alpha"`, and both `client_k8s_pod_uid` and `server_k8s_pod_uid` are absent
- **THEN** the resulting edge has `source: "external/admin"`, `target: "external/payments"`, edge `type: "pod-calls-pod"`, and the edge `labels` map contains no `cluster` key (client side is external)

#### Scenario: Both UID and human label empty — edge dropped

- **WHEN** a series has `client_k8s_pod_uid=""` AND `client=""` (or symmetrically empty server pair)
- **THEN** no edge is emitted for that series and no node is synthesised for that endpoint

#### Scenario: Pattern rule wins over missing-UID fallback

- **WHEN** `KSG_EXTERNAL_NAME_PATTERN="://"` is set and a series has `client="http://api.example.com"` with `client_k8s_pod_uid` also empty
- **THEN** the client side resolves to `external/http://api.example.com` via the pattern rule with `labels.pattern: "://"` set; the missing-UID fallback is NOT consulted (the pattern rule already produced the external node)

#### Scenario: Pattern unset, UID present — fallback does not fire

- **WHEN** `KSG_EXTERNAL_NAME_PATTERN=""` and a series has `client="checkout"` with `client_k8s_pod_uid="abc"`
- **THEN** the client side resolves via pod-UID lookup (with the synth-pod fallback on topology miss); the missing-UID fallback is NOT consulted (UID is non-empty)

#### Scenario: Pattern-matched and fallback-matched series produce the same external node

- **WHEN** `KSG_EXTERNAL_NAME_PATTERN="admin"`, series A has `client="admin-cli"`, `client_k8s_pod_uid="some-uid"` (pattern matches, UID is non-empty but pattern wins), and series B has `client="admin-cli"`, `client_k8s_pod_uid=""` (UID empty, falls back)
- **THEN** both series resolve their client side to the single node `id="external/admin-cli"`; the surviving node's `labels` are whichever the first observed series produced (the dedupe is by ID, not by labels — operators MUST NOT depend on a specific labels payload when both code paths reach the same ID)

### Requirement: Synthesised pod node fallback

When a service-graph series references a **non-empty** pod-UID endpoint that does not appear in the topology produced for the same window, the reader SHALL synthesise a pod node and SHALL NOT drop the edge. (Empty pod UIDs are handled by the "Missing pod-UID human-label fallback" requirement above, not by this rule.)

For the **client** side, the synthesised pod uses the metric's `cluster` label as its cluster value: `id="<cluster>/<client_k8s_pod_uid>"`, `labels.cluster=<cluster>`.

For the **server** side, when the global pod-UID index has no entry for `server_k8s_pod_uid`, the synthesised pod has `cluster` unknown: `id="/<server_k8s_pod_uid>"`, `labels.cluster=""`. (The metric does not carry a `server_cluster` label under Option A; the trace pipeline only knows the source cluster.)

In both cases, `name="<pod-uid>"`, `type="pod"`, and `labels` SHALL contain `namespace` when the metric provided `client_k8s_namespace_name` / `server_k8s_namespace_name`. The reader SHALL NOT add a boolean `ghost` flag to `labels`; consumers detect synthesised endpoints by the absence of richer labels (e.g., missing `node` for pods, or empty `labels.cluster` for unknown-cluster server pods).

#### Scenario: Server pod missing from topology

- **WHEN** a service-graph series has `cluster="cluster-alpha"` and `server_k8s_pod_uid="missing-uid"` but no pod with `uid="missing-uid"` exists in the topology global pod-UID index
- **THEN** the resulting graph contains a synthesised pod node with `id: "/missing-uid"`, `name: "missing-uid"`, `type: "pod"`, `labels.cluster: ""` (server-side cluster is unknown), no `labels.ghost` key, and the edge is emitted with this node as `target`

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
