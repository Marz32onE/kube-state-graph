## Context

This repository delivers exactly one component: the **graph API server**. Everything else — a centralised VictoriaMetrics, the per-cluster scrape pipelines that feed it (`kube-state-metrics`, vmagent / Prometheus, the customised service-graph metrics source), and the Kind-based integration-test harness — is treated as an external dependency and is only present in this repo as test scaffolding.

Data flow that the API server assumes is already in place:

```
cluster A: kube-state-metrics ──┐
           service-graph source ┤
                                 │  (vmagent / Prometheus
cluster B: kube-state-metrics ──┤   with external_labels:
           service-graph source ┤   { cluster: "<name>" })
                                 │
       ...                       ├──► centralised VictoriaMetrics ◄── Graph API server (this repo)
                                 │                                     (Prometheus HTTP API client)
cluster N: kube-state-metrics ──┤
           service-graph source ─┘
```

- Each cluster's scrape pipeline applies a `cluster=<name>` external label uniformly to `kube-state-metrics` and service-graph metrics before remote-writing into a single shared VictoriaMetrics.
- `kube-state-metrics` exports `kube_pod_info{cluster=...,uid=...}`, `kube_node_info{cluster=...,node=...}`, `kube_node_status_addresses{cluster=...}`, `kube_pod_spec_volumes_persistentvolumeclaims_info{cluster=...}`, `kube_node_labels{cluster=...}`, etc.
- A separate (out-of-repo) service-graph producer (typically Tempo's metrics-generator running per source cluster) emits pod-UID-labelled metrics carrying a single `cluster` external label representing the trace source cluster — the cluster originating the RPC. The remote (server-side) cluster is **not** stamped on the metric; cross-cluster status is recovered at build time by joining the server pod UID against the global topology pod-UID index:
  - `traces_service_graph_request_total{cluster, client_k8s_pod_uid, server_k8s_pod_uid, client_k8s_namespace_name, server_k8s_namespace_name, connection_type}`.
- The API server reads everything it needs from VictoriaMetrics through the Prometheus HTTP API, **on demand per request**, scoped to a caller-specified time range. It never talks to the Kubernetes API server in any cluster, never scrapes `kube-state-metrics` directly, and never connects to the service-graph producer.

The integration-test harness in this repo (single Kind cluster, in-cluster VictoriaMetrics, fake-fixtures producer that synthesises multi-cluster series) exists only to give CI and developers a reproducible target. It deliberately does **not** spin up multiple Kind clusters or real per-cluster scrape pipelines — that work belongs to deployment, not to this repo.

Constraints on the API server:

- Go 1.22+ standard library `log/slog` for logging.
- Gin for HTTP routing.
- `github.com/prometheus/client_golang/api` and `.../api/v1` for outbound queries.
- `github.com/dgraph-io/ristretto/v2` for the in-process cache layer.
- `golang.org/x/sync/singleflight` for request coalescing.
- `golang.org/x/sync/errgroup` and `.../semaphore` for parallel fan-out and concurrency capping.
- No Kubernetes client-go, no informers, no direct VictoriaMetrics SDK.
- Single configurable upstream URL (the centralised VictoriaMetrics Prometheus-compatible endpoint).

## Goals / Non-Goals

**Goals:**
- Ship a Go (Gin) HTTP server that returns a unified nodes-and-edges JSON document for one or more Kubernetes clusters in a caller-specified time range `[start, end]`, computed from VictoriaMetrics on demand.
- Expose **cross-cluster** RPC edges (`pod-calls-pod` where the source and target pods resolve to different clusters via the global pod-UID index) as first-class graph elements.
- Build the graph by issuing PromQL queries with `@` timestamp modifiers and range-aware functions (`last_over_time`, `rate`) against centralised VictoriaMetrics, and joining the result sets in memory across all clusters in scope.
- Serve concurrent same-time-range queries from a tiered cache stack (HTTP `ETag`, singleflight, Ristretto) so multiple users sharing a dashboard amortise to one upstream fan-out per time bucket — independent of how many cluster / namespace / edge-type filter combinations they request.
- Use the `(cluster, pod-uid)` composite as the stable identity for pod nodes and the join key for pod-pod edges; node and PVC IDs are similarly cluster-scoped.
- Expose Cytoscape.js-shaped JSON as the primary response, plus a Grafana Node Graph compatibility route for visual verification.
- Provide cluster discovery (`GET /v1/clusters`) sourced live from VictoriaMetrics, plus a static edge-type catalogue (`GET /v1/edge-types`).
- Provide an integration-test harness (single Kind cluster, in-cluster VictoriaMetrics, fake-fixtures producer for multi-cluster `kube_*` and `traces_service_graph_*` series, smoke script) that proves the API server returns a non-empty, well-formed multi-cluster graph including a cross-cluster edge.

**Non-Goals:**
- Implementing the customised service-graph collector (Alloy / OTLP collector). The harness uses a fake-fixtures producer that writes the contract metrics directly.
- Operating, configuring, or hardening `kube-state-metrics` or VictoriaMetrics. They are dependencies, not deliverables.
- Talking to the Kubernetes API directly in any cluster. All cluster facts are read via metrics.
- Authentication, authorisation, multi-tenant isolation, or TLS termination on the HTTP API (assume reverse proxy handles it). Per-cluster RBAC is also out of scope — every reachable cluster is equally readable through this server.
- Ingesting traces. Trace-derived metrics are produced upstream; the API server only reads the resulting metric series.
- Real-time streaming or WebSocket APIs.
- Persisting cache entries across process restarts. Cache is in-memory only.
- A multi-instance distributed cache (Redis, memcached). Single-instance deployment is the v1 assumption.
- A graph database (Neo4j, Memgraph, ArangoDB) for partial / traversal queries. In-memory adjacency suffices for v1.
- VictoriaMetrics multi-tenant (vmcluster `accountID:projectID`) routing. Single-tenant centralised VM with `cluster` external labels is the v1 isolation model; multi-tenancy is a v1.1 escape hatch.
- Spinning up multiple Kind clusters, or real per-cluster scrape pipelines, in the integration-test harness.

## Decisions

### D1. Single upstream: centralised VictoriaMetrics via Prometheus API

The server takes one upstream URL (`--prom-url`, default `http://localhost:8428`) pointing at the centralised VictoriaMetrics' Prometheus-compatible endpoint. All inputs (kube-state-metrics series and service-graph series, from any cluster) are queried from this single backend.

Multi-cluster discrimination is by **label**: every series carries `cluster=<name>` — for both topology (`kube_*`) and service-graph (`traces_service_graph_*`) metrics. Service-graph metrics carry only the trace-source cluster as `cluster`; the remote (server-side) cluster is recovered at build time by joining the server pod UID against the global topology pod-UID index. The API server never knows about per-cluster URLs.

- Why: matches the centralised-observability deployment topology these systems already use; collapses N readers into one client; lets one PromQL query cover all clusters in a single round-trip.
- Alternatives considered:
  - One upstream per cluster, fanned out by the API server (rejected — duplicates connection plumbing and breaks cross-cluster edge resolution, since an edge's two ends would land in two separate query results).
  - VictoriaMetrics multi-tenant (`accountID:projectID` per cluster) (rejected — requires vmcluster, heavier ops, and breaks single-PromQL cross-cluster edges; v1.1 escape hatch).
  - Direct Kubernetes API access via client-go informers (rejected — informers know only the *current* state of clusters they watch, cannot answer historical time-range queries, and re-introduce N watch streams plus per-cluster RBAC).

### D2. On-demand time-ranged build, no server-side snapshot

Every request to `GET /v1/graph?start=...&end=...` triggers a fresh build of the multi-cluster graph for the supplied window:

1. Resolve and validate `start` / `end`.
2. Compute the canonical cache key (D5).
3. Look up the cache (D6). On hit, serve from cache (with `X-Cache: HIT`).
4. On miss, enter `singleflight.Do(key)` so concurrent identical requests collapse to one build.
5. Inside the singleflight call, run all required PromQL queries against centralised VictoriaMetrics in parallel via `errgroup.WithContext`, join the result sets across clusters in memory, produce the global multi-cluster `Graph`, and populate the cache.
6. Apply filters (`cluster`, `namespace`, `node`, `edge_type`) and traversal pruning (`root`, `depth`, `direction`) over the cached `Graph`, then serialise to the requested format (Cytoscape.js or Grafana Node Graph) and return.

There is no background `Snapshotter`, no `atomic.Pointer[Graph]`, no fixed refresh interval, and no `POST /admin/refresh`.

- Why: the API contract is time-ranged, so the server cannot privilege any single "current" snapshot; the cache makes repeated reads of the same window cheap; the design is naturally horizontally scalable (single-instance only in v1, but no shared mutable state to remove).
- Alternatives considered:
  - Periodic snapshot (rejected — incompatible with time-travel queries; staleness window forces a worst-case freshness penalty even when no caller needs it).
  - Fully cache-free per-request build (rejected — N concurrent dashboard tabs = N× upstream load; the cache is the only protection against herd damage).

### D3. Pod, node, and PVC identity is cluster-scoped

`Graph` IDs:

- Pod nodes: `(cluster, pod-uid)`. Serialised IDs use the form `<cluster>/<pod-uid>`.
- K8s nodes: `(cluster, node-name)`. Serialised IDs use the form `<cluster>/<node-name>`.
- PVC nodes: `(cluster, namespace, pvc-name)`. Serialised IDs use the form `<cluster>/<namespace>/<pvc-name>`.

Edge endpoints reference these composite IDs.

- Why: pod UIDs are UUIDv4 and globally unique in practice, but mixing them with cluster names anyway is essentially free, makes IDs self-describing in logs and JSON, and avoids any contract relying on UUIDv4 collision avoidance. Node names and PVC names are *not* globally unique across clusters — node names like `worker-0` collide trivially — so cluster scoping is mandatory there.
- Alternatives considered:
  - Pod UID alone (rejected — works for pods but inconsistent with nodes/PVCs which require cluster scoping; mixing styles invites bugs).
  - `cluster.namespace.name` triple for pods (rejected — collides on pod restarts; service-graph metrics still reference the old UID for a window).

### D4. Edge types

Edges fall into typed categories:

- `pod-runs-on-node` (intra-cluster only): derived from `kube_pod_info{node=..., cluster=...}` evaluated within the time range.
- `pod-mounts-pvc` (intra-cluster only): derived from joining `kube_pod_spec_volumes_persistentvolumeclaims_info` with the node hosting the pod, within a single cluster.
- `pod-calls-pod` (intra-cluster **or cross-cluster**): from `rate(traces_service_graph_request_total[<window>]) @ <end>` with non-zero rate. The client side joins on `(cluster, client_k8s_pod_uid)`. The server side joins via the **global pod-UID index** built from topology — `server_k8s_pod_uid` alone resolves to a single pod across all loaded clusters, since K8s pod UIDs are unique cross-cluster in practice. The edge carries `labels.cluster` set to the client-side cluster (omitted when the client is an external endpoint); cross-cluster status is derived by comparing the resolved source-node `labels.cluster` and target-node `labels.cluster` on the consumer side (no boolean flag in `labels` per D9's strict-string rule).

Each edge carries `type`, `source`, `target`, plus type-specific `attrs` (see D9 for serialised JSON shape).

- Why: lets consumers filter by edge type and mirrors Tempo's `serviceGraph` shape conceptually; exposes cross-cluster traffic as a first-class concept rather than a secondary annotation.
- Alternative: untyped edges with a free-form attributes map (rejected — harder to validate and render).
- New edge types are additive only; existing `type` strings are never repurposed (see D14).

### D5. Time-range semantics and cache-key bucketing

`start` and `end` are mandatory query parameters in either RFC 3339 or Unix seconds form. The server enforces:

- `end > start`.
- `end - start <= --max-window` (default `24h`).
- `end <= now + --max-skew` (default `1m`).

To make caching effective, both timestamps are **bucketed** before forming the cache key. The bucket grid is **uniformly 60 s for every time class** so callers receive a predictable `(start_actual, end_actual)` regardless of how recent the query is. The TTL ladder remains class-dependent because the *staleness tolerance* differs even when the alignment grid does not:

| Time class | Test on `end` | Bucket size | Cache TTL |
|-----------|---------------|-------------|-----------|
| `live` | `end >= now - 1m` | 60 s | 30 s |
| `recent` | `end >= now - 1d` | 60 s | 5 min |
| `historical` | `end >= now - 7d` | 60 s | 1 h |
| `frozen` | `end < now - 7d` | 60 s | 24 h |

Alignment rules:

- `start_actual = floor(start, 60s)` — the requested left edge can only widen leftward.
- `end_actual = ceil(end, 60s)` — the requested right edge can only widen rightward, so any user-specified instant (e.g. `end=12:19` covering 12:17) is fully inside the resulting window. `floor` was rejected because it silently dropped data between `floor(end)` and `end`.
- When `ceil(end, 60s)` would exceed `now`, `end_actual` is clamped to `floor(now, 60s)` so PromQL is never evaluated at a future timestamp.
- The upstream PromQL queries use the **bucketed** timestamps so the result is bit-stable for callers who land in the same bucket. Callers receive `start_actual` / `end_actual` in the response and **must** read those fields rather than echoing the original `start` / `end` when laying the response onto a chart axis.

The cache key is **time-only**, covering the full multi-cluster graph:

```
key = xxhash(canonical_json({
  start_bucket,
  end_bucket,
  bucket_size
}))
```

Filter parameters (`cluster`, `namespace`, `node`, `edge_type`, `root`, `depth`, `direction`) and `format` are **not** part of the cache key. They are applied at response time as a projection over the cached global multi-cluster `Graph` value (D6, D7).

- Why: filter combinations otherwise fragment the cache. With multi-cluster, the fragmentation problem is N× worse — adding `cluster` to the key would multiply the cache footprint by the number of distinct cluster-filter combinations. Time-only keying collapses every filter request for the same window to one cache entry.
- Why filtering at PromQL doesn't help: VictoriaMetrics scans the index regardless; label selectors trim the network payload but not upstream evaluation cost. The full multi-cluster graph is small enough (target ≤ 5 k pods × ≤ 10 clusters ≈ tens of MB) to cache and project from.
- Mitigation for unbounded cluster count: an optional `--clusters-allowlist` flag injects a `cluster=~"a|b|c"` selector into all PromQL queries and bounds upstream cost regardless of how many clusters exist in VM.
- Alternatives considered:
  - Filters in cache key (rejected — fragmentation as above, made worse by adding `cluster`).
  - Per-cluster cache entries (rejected — defeats cross-cluster edges and bloats memory).
  - Hash the raw timestamps (rejected — sub-second drift between callers destroys hit rate).
  - Hybrid (narrow scope → narrowed cache key) — kept as a v1.1 escape hatch only if profiling shows the full-graph approach hits memory limits.

### D6. Cache layer: Ristretto + singleflight + ETag

Three coordinated layers:

1. **HTTP layer — `ETag` and `Cache-Control`.** Each response carries `ETag: "<sha256 of body>"` and `Cache-Control: public, max-age=<ttl-seconds>` derived from the time class in D5. Caller can short-circuit with `If-None-Match` → server returns `304 Not Modified` without re-serialising.
2. **Singleflight (`golang.org/x/sync/singleflight`).** Keyed by the same time-only cache key as Ristretto. N concurrent identical requests collapse to one upstream fan-out; all callers receive the same shared `Graph` value. Mandatory.
3. **Ristretto (`github.com/dgraph-io/ristretto/v2`).** Cost-based, sharded, low-contention cache. Per-entry TTL (variable by time class). Default `MaxCost = 256 MiB`, `NumCounters = 1e6`, `BufferItems = 64` — all configurable. Cost per entry = approximate in-memory size of the cached `Graph` (computed from node + edge counts, not serialised JSON).

**Cache value is the typed `*Graph` Go struct** holding the full multi-cluster graph for the window — not serialised JSON. Each request:

1. Loads the cached `*Graph` (or builds it under singleflight on miss).
2. Applies filter spec (`cluster`, `namespace`, `node`, `edge_type`) and traversal pruning (`root`, `depth`, `direction`) **read-only** over the shared `Graph`. The filter+prune step returns a lightweight view, not a copy.
3. Serialises the view in the requested `format` (Cytoscape.js or Grafana Node Graph).
4. Computes `ETag` from the serialised body and writes the response.

Because waiters always read from the returned `*Graph` (never from a follow-up `cache.Get`), Ristretto's eventual-visibility on writes does not introduce a re-build race.

Optional small **L2 cache for serialised responses**, keyed by `(time_bucket_key, filter_hash, format)`, with the same TTL ladder as L1. Skip for v1 unless profiling shows serialise-and-ETag is hot. Documented as v1.1 escape hatch.

A small abstraction `Cache` interface (Get / Set / Delete / Stats / Close) wraps Ristretto so the implementation can be swapped without touching call sites.

- Why Ristretto over `hashicorp/golang-lru/v2`: per-entry variable TTL is mandatory (kills `expirable.LRU`); sharded internals avoid the single-mutex contention that plain LRU exhibits under concurrent dashboard reads; W-TinyLFU + Doorkeeper resists scan flooding from one-off historical queries; cost-based budget gives a real memory ceiling.
- Why not Otter or other newer caches: keeping a single, well-established cache library reduces v1 risk; Ristretto is production-proven at Dgraph scale.

### D7. Filtering, cluster scoping, and partial-graph traversal

`GET /v1/graph` accepts (in addition to mandatory `start` / `end`):

- `?cluster=<name>` — repeatable; restricts the response to nodes whose `cluster` is in the set. Cross-cluster edges with one end inside the set and one end outside are **kept** (the remote endpoint resolves correctly because the cached `*Graph` holds all clusters); the remote endpoint node is also kept (with its own `labels.cluster`). Cross-cluster status is conveyed by comparing the source-node and target-node `labels.cluster` — consumers derive the boolean from the two strings (the edge itself carries only `labels.cluster` = trace-source / client-side cluster). Setting `cluster` to an unknown value is not an error — it simply yields an empty result for that name.
- `?namespace=<ns>` — repeatable; restricts pod / PVC nodes whose `namespace` is in the set. A namespace value matches across clusters; combine with `?cluster=` to scope to a single cluster's namespace.
- `?node=<node-name>` — repeatable; restricts to those K8s node names. Combine with `?cluster=` if names are not unique across clusters.
- `?edge_type=<type>` — repeatable; restricts to those edge types only. If a requested type has no edges in the current `Graph`, that type is silently skipped (no error, just empty).
- `?root=<id>&depth=<n>&direction=in|out|both` — partial-graph traversal: BFS from the given composite ID (`<cluster>/<pod-uid>` or `<cluster>/<node-name>`), bounded by `depth` (default 2, max 6).

Filtering is applied **at response time over the cached `*Graph` value**, not by re-querying upstream. PromQL queries always fetch the full window across all clusters in scope (subject to `--clusters-allowlist`); the cached `*Graph` is the shared base from which all filtered views are projected.

- Why: keeps the cache key small and the hit rate high; filter+serialise is microseconds for typical graph sizes.
- Empty filter ⇒ full multi-cluster graph for the time range.
- Filters compose with AND across types and OR within a type.
- Traversal first prunes by `root`/`depth`/`direction`, then `cluster` / `namespace` / `node` / `edge_type` filters apply over the traversal result.
- Alternatives considered:
  - PromQL label-selector narrowing per request (rejected — see D5 rationale).
  - A graph database for traversal queries (rejected — operationally heavy for a workload in-memory adjacency handles in microseconds, see D16).

### D8. Producer contract and integration-test producer

The API server depends on a **metric contract**, not on any specific producer. Contract:

```
# Topology (per cluster)
kube_pod_info{cluster, namespace, pod, uid, node, ...}
kube_node_info{cluster, node, ...}
kube_node_status_addresses{cluster, node, type="ExternalIP", address=...}
kube_pod_spec_volumes_persistentvolumeclaims_info{cluster, namespace, pod, volume, claim_name, ...}
kube_node_labels{cluster, node, label_*=...}

# Service graph (single source cluster per series; cross-cluster recovered at build time via UID index)
traces_service_graph_request_total{
  client, server,
  cluster,                        # single trace-source cluster (client side)
  client_k8s_pod_uid, server_k8s_pod_uid,
  client_k8s_namespace_name, server_k8s_namespace_name,
  connection_type="virtual_node|messaging_system|database"
}
traces_service_graph_request_failed_total{ ...same labels... }
traces_service_graph_request_server_seconds_bucket{ ...same labels..., le="..." }
```

The `cluster` external label is applied by each cluster's scrape pipeline (`vmagent` / Prometheus `external_labels`) — for both `kube-state-metrics` series and service-graph series. Service-graph metrics are produced per source cluster by Tempo's metrics-generator (or an equivalent `servicegraph` connector); the producer only knows the cluster it runs in and stamps that as `cluster`. The remote (server-side) cluster is **not** stamped — recovery of cross-cluster targets happens in the API server by joining `server_k8s_pod_uid` against the global topology pod-UID index. The producer-side instrumentation requirement reduces to: emit `cluster` (typically already done as an external label) and pod-UID dimensions on each side.

**Integration-test fixture ingestion — direct exposition format:**

Integration tests in `internal/integration/` use [`testcontainers-go`](https://golang.testcontainers.org/) to start a real VictoriaMetrics container per suite, then push hand-crafted multi-cluster series via VictoriaMetrics' `POST /api/v1/import/prometheus` endpoint (Prometheus text exposition format). No separate fixture binary, no YAML, no `/metrics` endpoint, no SIGHUP reload — the test itself owns the series content and timestamps. Each test seeds:

- `kube_pod_info` / `kube_node_info` / `kube_node_status_addresses` series for several synthetic clusters (e.g., `cluster-alpha`, `cluster-beta`).
- `traces_service_graph_request_total` series including at least one **cross-cluster** edge: a series with `cluster="cluster-alpha"` whose `server_k8s_pod_uid` matches a pod whose `kube_pod_info` entry lives in `cluster-beta`, so the test asserts cross-cluster handling via UID-index resolution.

Service-graph counters are ingested as two monotonic samples (`t0` and `t1 = t0 + 60s`) so `rate(...[w]) @ t1` recovers a non-zero per-second rate. Tests use a fixed-time anchor (`fixedNow = 2026-05-01T12:00:00Z`) to keep time-bucket alignment deterministic — see D20.

- Why direct ingestion: the API server is the unit under test. Synthesising the metric contract directly in Go keeps tests focused on join / build / HTTP behaviour, makes multi-cluster scenarios trivial (just emit different `cluster` values), and avoids dragging in collector + tracing dependencies, fixture programs, YAML schemas, and reload protocols.
- Tests MUST emit the exact label set the production contract specifies, so swapping in real producers in deployment is a configuration change, not a code change.
- The local Kind rig (`local/kind/`) is **separate** and uses a **real** `kube-state-metrics` scraping the Kind cluster — that exercises the topology code path against real series. It does not produce `traces_service_graph_*` (no Tempo); the service-graph code path is exercised only by `internal/integration/`.

**Rejected: standalone fixtures binary (`cmd/vm-fixtures/`) + YAML config** — earlier sketch; superseded by direct in-test exposition ingestion. The binary added build complexity, deployment surface, and a YAML schema for no test-discrimination benefit. Tests can author exact series inline in Go.
**Rejected: multiple Kind clusters with real `kube-state-metrics`** — doubles harness setup cost, exhausts laptop resources, and validates the same metric contract that direct ingestion already covers.
**Rejected: synthetic OTLP trace generator + collector** — full pipeline exists in production but is upstream of this server; doubles the integration-test surface for no benefit.
**Rejected: `telemetrygen`** — emits standalone spans without parent/child propagation, so the `servicegraph` connector cannot pair them and no edge metrics result.
**Rejected: OpenTelemetry Demo (`otel-demo`)** — boots ~15 services and a heavy chart; too much for a per-PR integration test.

### D9. Output format: Cytoscape.js JSON, with Grafana Node Graph compatibility

**Node and edge schema (canonical, used in both formats):**

| Object | Field | Type | Source / Notes |
|---|---|---|---|
| Node | `id` | string | Cluster-scoped composite. Pods: `<cluster>/<pod-uid>`. Nodes: `<cluster>/<node-name>`. PVCs: `<cluster>/<namespace>/<claim>`. **External**: `external/<label-value>` (no cluster). |
| Node | `name` | string | Pod name / node name / PVC claim name. For external nodes, the verbatim `client` or `server` label value (e.g., `http://api.example.com`). Used as the display label in the Grafana panel. |
| Node | `type` | string | One of `"pod"`, `"node"`, `"pvc"`, `"external"`. |
| Node | `labels` | `map[string]string` | String-only key/value bag. Pod / node / PVC nodes always include `cluster`, `namespace` (pods/PVCs), `node` (pods, cluster-scoped node ID), `external_ip` (nodes when known). K8s pod / node labels are flattened in verbatim. **External nodes** carry minimal labels (the configured `pattern` value under `pattern`); they do NOT carry `cluster`, since they are not cluster-scoped. New keys are additive. |
| Edge | `id` | string | UUIDv5 derived from a fixed namespace UUID and the canonical tuple `(type, source, target)`. Stable across builds for the same edge; format compliant with RFC 4122. |
| Edge | `type` | string | One of the registered edge types in `/v1/edge-types` (e.g., `"pod-runs-on-node"`, `"pod-mounts-pvc"`, `"pod-calls-pod"`). |
| Edge | `source` | string | Source node `id`. Always references a node present in the same response. |
| Edge | `target` | string | Target node `id`. Always references a node present in the same response. |
| Edge | `labels` | `map[string]string` | String-only key/value bag. For `pod-calls-pod`: `cluster` (the trace source cluster, i.e. the client-side pod's cluster — omitted when the client is an external endpoint). For `pod-mounts-pvc`: `claim_name`, `storage_class`. For `pod-runs-on-node`: `scheduled_at`. New keys are additive. |

**Strictly string-typed values.** `labels` is `map[string]string` for both nodes and edges. Non-string-typed data (numeric edge metrics such as `rate`, `p99_ms`, `error_rate`; boolean flags such as `cross_cluster` or `ghost`) is **deferred to a future typed struct field** on node/edge data. v1 does not encode booleans as `"true"`/`"false"` strings inside `labels`; consumers derive cross-cluster status for `pod-calls-pod` edges by comparing the edge's resolved source-node `labels.cluster` with the target-node `labels.cluster` (both nodes are guaranteed present in the same response).

The primary `GET /v1/graph` response is **Cytoscape.js**-shaped JSON:

```json
{
  "apiVersion": "v1",
  "start": "2026-05-01T12:00:00Z",
  "end":   "2026-05-01T12:05:00Z",
  "start_actual": "2026-05-01T12:00:00Z",
  "end_actual":   "2026-05-01T12:05:00Z",
  "bucket_seconds": 15,
  "built_at": "2026-05-01T12:05:13Z",
  "clusters": ["cluster-alpha", "cluster-beta"],
  "elements": {
    "nodes": [
      { "data": { "id": "cluster-alpha/abc-123",
                  "name": "checkout-7c9d-x2p4",
                  "type": "pod",
                  "labels": { "cluster": "cluster-alpha", "namespace": "shop",
                              "node": "cluster-alpha/worker-0",
                              "app": "checkout", "version": "1.4.2" } } },
      { "data": { "id": "cluster-alpha/worker-0",
                  "name": "worker-0",
                  "type": "node",
                  "labels": { "cluster": "cluster-alpha",
                              "external_ip": "203.0.113.10",
                              "kubernetes.io/arch": "amd64",
                              "topology.kubernetes.io/zone": "us-east-1a" } } }
    ],
    "edges": [
      { "data": { "id": "5e7b8d6a-2c1f-5b3a-9b14-3a3f0a9e2c11",
                  "type": "pod-calls-pod",
                  "source": "cluster-alpha/abc-123",
                  "target": "cluster-beta/def-456",
                  "labels": { "cluster": "cluster-alpha" } } }
    ]
  }
}
```

The second route, `GET /v1/graph/nodegraph`, returns the same data projected into the **Grafana Node Graph** API datasource shape (parallel `nodes_fields`/`nodes` and `edges_fields`/`edges` arrays). The serializer maps the canonical fields as follows:

- Node `name` → `title`.
- Node `labels.cluster` ` · ` `labels.namespace` (or `labels.cluster` alone when no namespace) → `subTitle`.
- Node `type` → `mainStat`.
- Edge `type` → `mainStat`.
- Edge `secondaryStat` is left empty in v1 (numeric edge metrics are deferred to a future typed struct field; see the strictly-string-typed labels note above).

This makes the integration-test Grafana panel show pod / node names directly without per-deployment template tweaking.

- Why: a single canonical schema (`id`, `name`, `type`, `labels`) drives both formats; any future field addition lives in `labels` and is therefore non-breaking.
- Why UUIDv5 for edge `id`: deterministic (cache and golden tests stay stable; same edge → same ID across rebuilds), RFC 4122 compliant, and decoupled from the human-readable `(source, target, type)` triple so renaming convention later does not change IDs already exposed.
- Alternatives considered:
  - `kind`/`label`/`attrs` field names (rejected — divergent from user-requested schema).
  - Random UUIDv4 for edges (rejected — breaks cache stability and golden tests; same edge would get a different ID every build).
  - Plain `{nodes, edges}` only (rejected — locks out Grafana Node Graph compat without an adapter layer).
  - GraphQL (rejected — adds dependency surface for v1 with no clear caller).

### D10. Logging via `log/slog`, JSON handler

`slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: ...}))` set as default logger; level configurable. Every HTTP request emits one structured log line with method, path, status, duration, request ID, applied `cluster` filter values, and `cache_status` (`hit | miss | coalesced`).

One additional log line per build: `slog.Info("graph built", "duration_ms", ..., "clusters", ..., "nodes", ..., "edges", ..., "cross_cluster_edges", ..., "queries", ..., "failures", ..., "start", ..., "end", ...)`.

- Why: required by the user; standard library only.
- Alternative: zap / zerolog (rejected — extra dependency, not requested).

### D11. Implementation tactics

These are mandatory for the v1 implementation:

- **Sealed graph node types**: Go interface `GraphNode` with concrete `PodNode`, `NodeNode`, `PVCNode`. Each implementation surfaces the canonical fields (`ID()`, `Name()`, `Type()`, `Labels()`) consumed by the serialisers in D9. The `cluster` value lives inside `Labels()["cluster"]` rather than as a separate first-class field on the wire.
- **Pure join layer**: `Build(topology Topology, edges []ServiceGraphEdge, clustersAllowlist []string) *Graph` is a pure function over typed Go structs and produces the full, unfiltered multi-cluster graph for the time window. All HTTP- and Prometheus-free unit tests target this function. Cross-cluster edges are produced when the resolved source-pod cluster differs from the resolved target-pod cluster (server-side cluster comes from the topology pod-UID index lookup, not from a metric label).
- **Pure projection layer**: `Project(g *Graph, scope Scope) GraphView` applies cluster / namespace / node / edge_type filters and traversal over an immutable `*Graph` and returns a read-only view. No allocations of new node/edge structs, just slices of pointers.
- **Query registry**: PromQL strings as named constants in one file, parameterised on `<window>`, `<end>`, and an optional `<clusters_allowlist>` fragment (`{cluster=~"a|b|c"}`). Paired with a parser that maps Prometheus `model.Vector` results into typed Go structs.
- **One PromQL instant query per metric family**, evaluated at the bucketed `end` with `last_over_time` / `rate` over the window. Queries do **not** include filter-derived selectors; they include only the static `--clusters-allowlist` if configured. Parse Vector client-side.
- **Parallel upstream fan-out** via `errgroup.WithContext`. Wall-clock latency = O(slowest query), not O(sum of queries).
- **Per-build context timeout**, default 15 s, configurable. On any sub-query failure, the whole build is aborted, the failure counter increments, and the request returns `503` with `Retry-After: 1`.
- **Concurrency cap** via `golang.org/x/sync/semaphore` — default 8 concurrent builds. Excess returns `503 Service Unavailable`.
- **Cache key hashing**: xxhash of canonical-JSON form so the key is a single `uint64` and Ristretto operates on numeric keys.
- **Adjacency maps**: forward and reverse `map[NodeID][]*Edge` built once inside `Build()`; reused for traversal pruning during `Project()`. Built on the immutable `*Graph` so concurrent projections from different requests share them safely.

### D12. Self-metrics and operability

The server exposes its own `/metrics` endpoint (Prometheus exposition) with at least:

- `kube_state_graph_build_duration_seconds{cache_status}` (histogram — `cache_status` ∈ `{hit, miss, coalesced}`).
- `kube_state_graph_project_duration_seconds` (histogram — filter + traversal pruning).
- `kube_state_graph_serialise_duration_seconds{format}` (histogram — JSON encode + ETag computation).
- `kube_state_graph_cache_hits_total{layer="ristretto|singleflight|etag"}` (counter).
- `kube_state_graph_cache_misses_total{layer}` (counter).
- `kube_state_graph_cache_size_entries` (gauge — keyed by time bucket only; cardinality bounded by the time-window space).
- `kube_state_graph_cache_cost_bytes` (gauge).
- `kube_state_graph_cache_evictions_total{reason="cost|ttl"}` (counter).
- `kube_state_graph_cache_rejected_total` (counter — Ristretto admission rejections).
- `kube_state_graph_singleflight_dedup_total` (counter).
- `kube_state_graph_build_concurrency` (gauge).
- `kube_state_graph_build_rejected_total{reason="capacity|timeout"}` (counter).
- `kube_state_graph_graph_node_count{cluster,kind}` (gauge — last build only, observational; bounded by configured cluster count).
- `kube_state_graph_graph_edge_count{type,cross_cluster}` (gauge — `cross_cluster` ∈ `{"true","false"}`).
- `kube_state_graph_clusters_observed` (gauge — unique `cluster` values seen in the last build).
- `kube_state_graph_upstream_query_duration_seconds{query}` (histogram).
- `kube_state_graph_upstream_query_failures_total{query}` (counter).
- `kube_state_graph_http_requests_total{path,status}` (counter).

Health endpoints:

- `GET /livez` — always 200 while the process is up.
- `GET /readyz` — 200 iff a cheap upstream probe (`up{}` instant query, 1 s timeout) succeeds. 503 otherwise.

Operator endpoints:

- `DELETE /admin/cache` — flushes the Ristretto cache (debugging only).
- `GET /debug/last-queries` — returns the raw upstream query strings and a redacted summary of the last build's responses (counts and labels, not values). Behind a `--enable-debug` flag.

### D13. Testing layers

The test stack has six layers, all CI-runnable except the last; each MUST exist before this change is archived:

| Layer | CI? | Scope | Tool |
|------|-----|------|------|
| Unit | yes | Pure join / parse / project functions on hand-crafted multi-cluster `model.Vector` and `model.Matrix` inputs (intra-cluster, cross-cluster, and mixed) | `go test` |
| Component | yes | Build pipeline end-to-end against an `httptest.Server` mocking the Prometheus query API; covers cache, singleflight, concurrency cap, time-bucket alignment, and `--clusters-allowlist` injection | `go test` |
| Golden | yes | Canned scenarios (single-cluster, two-cluster with cross-cluster edge, three-cluster with traversal pruning) → `/v1/graph`, `/v1/graph/nodegraph`, `/v1/clusters`, `/v1/edge-types` JSON compared to checked-in `.golden.json` | `go test` |
| Property | yes | Random topology + edge inputs across N synthetic clusters + random filters → invariants (no orphan edges, no duplicate IDs, every endpoint resolves, filtered ⊆ unfiltered, traversal stays within `depth`, cross-cluster edges have distinct cluster endpoints) | `testing/quick` or `gopter` |
| **Container integration** (capability `container-integration`) | yes | Per-package VictoriaMetrics container started via testcontainers-go; series injected via VM's `/api/v1/import/prometheus`; in-process API server pointed at the container; assertions over real PromQL evaluation, real cache, real ETag flow | `go test` + Docker |
| **Manual visual rig** (capability `verification-harness`) | **no** | Single Kind cluster with VictoriaMetrics + fake-fixtures producer + API server + Grafana Pod with the checked-in Node Graph dashboard, run on demand by an operator. Used for visual sanity verification of the rendered graph; not exercised by CI | `bash` bootstrap + browser |

The first five layers run on every PR via `go test ./...`. The Kind manual rig is exercised by operators on demand only — see D20 for testcontainer rationale and D21 for static analysis / vulnerability scanning policy.

- Why: integration alone leaves logic regressions undetectable in PR feedback; mock-only component tests miss real PromQL semantics; Kind alone is too slow and fragile for per-PR feedback. The split puts every behavioural assertion in the CI path against real PromQL, while the Grafana rig keeps human-in-the-loop verification first-class without coupling it to merge gates.

### D14. Versioning

- All HTTP routes are prefixed `/v1/`. v2 can coexist on the same binary if the JSON shape ever breaks.
- The body carries `apiVersion: "v1"` so off-the-wire consumers can detect breaks.
- New edge types and new `attrs` fields are additive only; removed fields are a v2 break.
- `connection_type` values from the producer contract are mapped to a stable internal enum so a producer-side rename does not propagate into the API contract.
- `cluster` label values pass through as opaque strings; renaming a cluster upstream is a caller-visible change, not an API break.
- Cache-key shape is treated as internal; cache survives only within a process, so changes to it never break clients.

### D15. Edge-type discovery API

`GET /v1/edge-types` returns the static catalogue of edge types this server can produce. No upstream calls; not parameterised by time range, cluster, or filters.

```json
{
  "apiVersion": "v1",
  "edge_types": [
    {
      "type": "pod-runs-on-node",
      "description": "Pod scheduled on a node, derived from kube_pod_info{node=...}. Always intra-cluster.",
      "source_type": "pod",
      "target_type": "node",
      "directed": true,
      "may_cross_cluster": false,
      "labels": [
        { "name": "scheduled_at", "value_type": "string" }
      ]
    },
    {
      "type": "pod-mounts-pvc",
      "description": "Pod mounts a PVC bound on the pod's host node. Always intra-cluster.",
      "source_type": "pod",
      "target_type": "pvc",
      "directed": true,
      "may_cross_cluster": false,
      "labels": [
        { "name": "claim_name",    "value_type": "string" },
        { "name": "storage_class", "value_type": "string" }
      ]
    },
    {
      "type": "pod-calls-pod",
      "description": "Pod-UID-resolved RPC edge from service-graph metrics. May cross clusters when the resolved source and target pods live in different clusters (recovered from the topology pod-UID index since the metric only carries the trace-source cluster). Endpoints may be 'external' nodes when KSG_EXTERNAL_NAME_PATTERN matches the upstream client/server label (D18).",
      "source_type": ["pod", "external"],
      "target_type": ["pod", "external"],
      "directed": true,
      "may_cross_cluster": true,
      "labels": [
        { "name": "cluster", "value_type": "string" }
      ]
    }
  ]
}
```

- Source: a single in-code registry shared with the graph builder. Adding a new edge type updates both atomically.
- Caching: response carries `Cache-Control: public, max-age=3600` and an `ETag` derived from the registry's compile-time hash. No Ristretto entry.
- Behaviour with `/v1/graph?edge_type=`: callers may pass any subset of `type` values from this endpoint. If a requested type has no edges in the current `Graph`, the response simply contains zero edges of that type — no error, no warning.

### D16. No graph database, no client-go informer for v1

Both options were considered and rejected for v1:

- **Graph DB (Neo4j / Memgraph / ArangoDB):** ~1 GB memory baseline, ops burden (backups, upgrades, auth, monitoring), sync complexity, ms-scale query latency. Traversal queries described in D7 are answered in microseconds by in-memory adjacency at v1 scale.
- **client-go informer for topology:** informers expose only the *current* cluster state and cannot answer historical time-range queries — the API's contract. Multi-cluster makes this worse: would need N watch streams plus per-cluster RBAC.

**Revisit triggers** (any of these promotes one to v1.1+):

- Total cluster size > 100 k objects across all clusters in scope.
- Time-travel queries beyond TSDB retention window become required.
- Cross-region cluster federation with isolation requirements (consider VictoriaMetrics multi-tenant routing instead).
- Free-form Cypher / Gremlin from operators.
- Sub-second freshness for "live" windows becomes a UI requirement.

### D17. Multi-cluster routing, discovery, and cross-cluster edges

**Routing.** All graph endpoints accept multi-cluster scope as a query parameter, not a path segment:

- `GET /v1/graph?start=...&end=...&cluster=<name>&cluster=<name>...`
- `GET /v1/graph/nodegraph?...`

`cluster` is repeatable; absent ⇒ all clusters in the cached graph (subject to `--clusters-allowlist`). Path-based per-cluster URLs were considered and rejected: cross-cluster edges naturally span more than one cluster, so a single-cluster path implies a scope smaller than the data — leading either to lossy responses (drop cross-cluster edges) or surprising responses (include endpoints outside the path). Query-param multi-select avoids this entirely.

**Discovery.** `GET /v1/clusters` returns the list of clusters that have data in centralised VictoriaMetrics, derived live from `group by (cluster) (kube_node_info)` over a configurable lookback (`--cluster-discovery-lookback`, default `1h`). Result is cached for 60 s under a fixed key so the discovery endpoint is cheap. If `--clusters-allowlist` is set, the discovery result is intersected with the allowlist before being returned.

**Cross-cluster edges.** `pod-calls-pod` edges where the resolved source and target pods live in different clusters are emitted as ordinary edges with both endpoint nodes present in the cached graph (since the cache holds the global multi-cluster graph). When a request scopes to a subset of clusters, cross-cluster edges that touch the selected set are kept along with both endpoint nodes — the remote node's `labels.cluster` makes the cross-cluster context obvious to renderers. The edge carries `labels.cluster` set to the trace-source cluster (i.e. the client-side pod's cluster); consumers detect cross-cluster status by comparing the source-node and target-node `labels.cluster` (a boolean shortcut field is deferred to the future typed struct described in D9).

**Cluster name handling.** Cluster names pass through as opaque strings. The server does no canonicalisation, no case-folding, and no length validation beyond the total URL length the HTTP stack already enforces. An unknown cluster name in `?cluster=` simply yields no nodes for that name — not an error.

### D18. External-endpoint substitution

Service-graph metrics carry a Tempo-style pair of human-readable labels alongside the pod-UID labels:

- `client` — the calling service's name (free-form, set by the producer).
- `server` — the callee's name (free-form, set by the producer).

By default the pod-service-graph reader resolves the client side via `(cluster, client_k8s_pod_uid)` and the server side via the global topology pod-UID index lookup of `server_k8s_pod_uid`, then uses the resulting pod's `name` for display. This loses dependencies whose remote end is not a pod (external HTTP APIs, managed databases, message queues, third-party SaaS, etc.) — pod UID is empty or arbitrary for those.

To preserve such endpoints in the graph, the server takes a **pattern substring** from the env var `KSG_EXTERNAL_NAME_PATTERN` (also flag `--external-name-pattern`). When set, the reader performs per-endpoint substitution:

```
for each service-graph series in the window, for endpoint side ∈ {client, server}:
  let label_value = the series' `client` or `server` label value
  if KSG_EXTERNAL_NAME_PATTERN != "" and contains(label_value, KSG_EXTERNAL_NAME_PATTERN):
    treat this endpoint as an external node
      id    = "external/<label_value>"
      name  = label_value
      type  = "external"
      labels = { "pattern": "<KSG_EXTERNAL_NAME_PATTERN>" }
  else:
    treat this endpoint as a pod node, resolved via (cluster, pod-uid) → kube_pod_info → pod name
```

Substitution is independent for client and server sides — a single `pod-calls-pod` edge can have any combination (`pod→pod`, `pod→external`, `external→pod`, `external→external`). The edge's `type` remains `pod-calls-pod`; only the source / target node `type` changes. The edge carries `labels.cluster` (the trace-source / client-side cluster) only when the **client** side is a pod; when the client side is an external endpoint, the edge `labels` map omits the `cluster` key entirely (external endpoints are not cluster-scoped).

Why a substring contains-check rather than a regex:

- Operators typically configure a single discriminator like `://` (matches `http://...`, `https://...`, `redis://...`) or `@` (matches `user@host`) without authoring a regex.
- Substring matching is unambiguous, has no escaping pitfalls, and benchmarks at hundreds of millions of operations per second so cost is negligible.
- A future v1.x revision MAY add `KSG_EXTERNAL_NAME_REGEX` if real deployments need it; for v1, contains is enough.

External node ID stability:

- The literal `client` / `server` value is appended verbatim after `external/`. Two different label values produce two different external nodes; two series with identical label values resolve to the same external node and edges to it merge correctly.
- The reader does not normalise (no lowercase, no whitespace trim, no scheme parsing). Producers control the label values; the API server is a faithful relay. This keeps semantics simple and matches Tempo's behaviour.

Empty pattern (`KSG_EXTERNAL_NAME_PATTERN` unset or `""`) disables the rule entirely; behaviour reverts to pure pod-UID resolution.

- Why expose this as a config knob: external-endpoint conventions vary by deployment. URL-shaped clients/servers are common but not universal; the operator decides what counts as "external" by choosing the discriminator.
- Alternatives considered:
  - Always treat both `client` and `server` as authoritative (rejected — defeats the pod-name resolution that is the point of this server).
  - Always introspect the value (URL parser, hostname extraction) (rejected — heuristic, brittle, language-specific).
  - Multiple patterns OR'd (rejected — over-engineered for v1; one pattern covers the typical case).

### D19. Allowlist and bounded upstream cost

Two flags bound the worst-case upstream load when many clusters share the centralised VictoriaMetrics:

- `--clusters-allowlist <comma-separated-names>` — when set, the API server injects `{cluster=~"a|b|c"}` into all PromQL queries and into the discovery query. Clusters outside the allowlist are invisible to this server, regardless of what the caller passes in `?cluster=`.
- `--max-pods <n>` — fail fast (`503` with `reason: "cluster_too_large"`) when the count of distinct `kube_pod_info` series in scope exceeds the configured ceiling (default `5000`).

Together these keep the v1 design within its stated cluster-size budget without surprising operators when their VictoriaMetrics grows.

### D20. Container integration via testcontainers-go

The CI integration layer uses **testcontainers-go** (`github.com/testcontainers/testcontainers-go`) to spin up a real VictoriaMetrics container from inside `go test`. Tests run in `internal/integration/`.

Architecture:

```
go test ./internal/integration/
  │
  ├─ TestMain (per package)
  │     └─ start vmsingle container (image pinned, e.g. victoriametrics/victoria-metrics:v1.107.0)
  │
  ├─ test helper: ingest(t, exposition string)
  │     POST <vm.URL>/api/v1/import/prometheus
  │
  └─ each test:
        ├─ ingest synthetic kube_* + traces_service_graph_* exposition with absolute timestamps
        ├─ wait for VM to acknowledge data (poll up{} or count(kube_pod_info) until non-empty, ≤ 10 s)
        ├─ start the API server in-process: srv := api.New(cfg, ...).Handler() + httptest.NewServer
        └─ exercise /v1/* endpoints, assert HTTP shape / headers / cache behaviour
```

Decisions:

- **One container per package**, not per test: bootstrapping VM costs ~5–10 s, far more than each test. Tests inside a package use unique series-label discriminators (e.g., a `test=<TestName>` label) so they never collide.
- **Direct injection via `/api/v1/import/prometheus`**, not a scrape stub: keeps the test process self-contained (no second container, no scrape interval to tune), and the API server only sees series in VM regardless of how they got there. The Prometheus exposition format is hand-written by the test, supports per-sample timestamps, and is the same format the manual-rig fixtures producer emits.
- **In-process API server** (`api.New(...).Handler()` against `httptest.NewServer`): no third container; tests can introspect server state, share types, and avoid Docker round-trips. Containerised server behaviour is covered by the manual rig instead.
- **Absolute timestamps in fixtures**: tests use fixed timestamps (e.g., `time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)`) and pass the same window to the API. This makes time-bucket alignment fully deterministic — a class of bugs the httptest mock layer cannot expose.
- **VM image is pinned** by tag in test code; no `:latest`. Image is pre-pulled in CI to remove first-run noise.
- **Docker socket required**: the integration-test job runs on `ubuntu-latest` GitHub Actions runners (Docker socket native). macOS / Windows runners are out of scope for this layer.
- **Ristretto async-write race**: the existing `cache.Wait()` after `Set` already serialises visibility for the next request. Tests that assert cache `MISS`/`HIT` ordering rely on that contract; do not relax it without re-reading D6.

What testcontainers does **not** cover (and so the manual rig still does):

- Kubernetes Service / Deployment / ConfigMap wiring.
- Real scrape pipeline (vmagent → VM).
- Visual rendering correctness in Grafana.

- Why this split: a container-only CI layer gives us real PromQL evaluation, deterministic time semantics, and parallel-safe per-package tests, without the operational cost of Kind on every PR.
- Alternatives considered:
  - Replace Kind harness entirely (rejected — visual verification in Grafana is still high-leverage for human review of edge-rendering correctness; Kind earns its keep as the platform for that, just not as a CI gate).
  - Run the API server as a third container (rejected — no benefit over in-process for the assertions this layer makes; the Dockerfile is exercised by the manual rig).
  - Inject series via a scrape stub container (rejected — adds a container, a scrape interval, and a startup race for no behavioural benefit since the API only ever reads from VM).

### D21. Static analysis and vulnerability scanning

Two CI gates beyond `go test`:

**1. `golangci-lint` — curated linter set.** A repository-level `.golangci.yml` enables the following linters (alphabetical):

- Correctness: `errcheck`, `gosimple`, `govet`, `ineffassign`, `staticcheck`, `unused`, `gocritic`, `exhaustive`.
- Modern Go idioms: `copyloopvar`, `intrange`, `revive`.
- Error handling: `errorlint`, `nilerr`.
- Security: `gosec`.
- Complexity: `gocyclo`, `gocognit`, `funlen`.
- Performance: `prealloc`, `bodyclose`, `unconvert`.
- Style: `misspell`, `gofmt`, `goimports`.
- Dead code / duplication: `dupl`, `unparam`.
- Magic numbers: `mnd`.

Complexity caps:
- `gocyclo`: cyclomatic complexity ≤ 15 per function.
- `gocognit`: cognitive complexity ≤ 20 per function.
- `funlen`: ≤ 100 lines / ≤ 50 statements per function.

Test files are exempted from `errcheck` and the strictest complexity / duplication rules (table-driven tests legitimately repeat structure).

**2. `govulncheck` — dependency vulnerability scanner.** A separate CI step runs `golang.org/x/vuln/cmd/govulncheck@latest ./...` on every PR. Detected vulnerabilities MUST be either fixed (dependency bump) or triaged with an explicit suppression comment + linked tracking issue before merge.

CI integration sketch (workflow snippet):

```yaml
jobs:
  lint:
    steps:
      - uses: golangci/golangci-lint-action@v8
        with: { args: --timeout=5m }
  vuln:
    steps:
      - run: |
          go install golang.org/x/vuln/cmd/govulncheck@latest
          govulncheck ./...
  test:
    steps:
      - run: go test ./... -count=1 -race -shuffle=on
```

`lint`, `vuln`, and `test` run as parallel jobs (no `needs` edges) so PR feedback latency = max, not sum.

- Why: linter set covers the trending Go quality dimensions (complexity, security, error handling, modern idioms) without enabling everything (`golangci-lint` ships ~100 linters; many overlap or fight each other). The set above is intentionally curated to maximise signal and minimise false positives.
- Why `govulncheck` separately from golangci-lint: govulncheck reads call-graph reachability, not just static patterns, and is the official Go security-team tool. Keeping it as its own CI job makes failure mode obvious in the GitHub UI.
- Alternatives considered:
  - Enable every golangci-lint linter (rejected — high false-positive rate, lint fatigue, churn from linter authors).
  - Use `gosec` only without `golangci-lint` (rejected — narrower coverage, redundant tooling).
  - Run `govulncheck` only on tagged releases (rejected — a vulnerable dependency lands the day it's introduced, not on release; per-PR is the only useful cadence).

### D22. OpenAPI generation and offline-capable Scalar UI

**Generation: `swaggo/swag` v2.** Handler functions in `internal/api/handlers.go` carry annotation comments (`// @Summary`, `// @Description`, `// @Tags`, `// @Param`, `// @Success`, `// @Failure`, `// @Router`, `// @Header`); the `cmd/kube-state-graph/main.go` entry point carries the document-level annotations (`// @title`, `// @version v1`, `// @license.name Apache 2.0`, `// @BasePath /v1`). Running `swag init -g cmd/kube-state-graph/main.go --output docs --parseDependency --parseInternal` regenerates `docs/swagger.json`, `docs/swagger.yaml`, and `docs/docs.go`. Generated files are checked in.

**OpenAPI version: 3.0.** Stable in swag v2; swag v2's 3.1 mode is still maturing and 3.0 covers everything we need (`additionalProperties: { type: string }` for the strict `map[string]string` `labels` invariant from D9, error-envelope component, etc.). Bump to 3.1 once swag v2's 3.1 path has shipped GA.

**Routes for spec + UI**:

| Route | What | Cache |
|------|------|------|
| `GET /openapi.yaml` | Generated YAML, served from embedded `docs/swagger.yaml` | `Cache-Control: public, max-age=3600` + ETag |
| `GET /openapi.json` | Generated JSON, served from embedded `docs/swagger.json` | same |
| `GET /docs` | HTML page that renders the spec via the Scalar API Reference viewer | `Cache-Control: public, max-age=300` |
| `GET /docs/assets/*` | Static Scalar JS / CSS bundle, served from embedded assets | `Cache-Control: public, max-age=86400, immutable` |

**Scalar UI is vendored in the binary**, not loaded from a CDN. The Scalar `@scalar/api-reference` standalone bundle (currently ~600 KB minified+gzipped) is checked in under `internal/api/static/scalar/` and embedded via `embed.FS`. The HTML at `/docs` references `/docs/assets/scalar.js` (relative path), so the page renders correctly behind reverse proxies, in air-gapped clusters, on isolated VPNs, and on developer laptops without internet — no exception cases.

The vendored bundle version is pinned (e.g., `@scalar/api-reference@1.x.y`) and refreshed via a `make refresh-docs-ui` script that re-downloads the pinned version, validates the SHA-256, and updates the embedded files. The script's expected SHA is committed alongside.

**Drift gate**: a `make check-docs` target re-runs `swag init` and exits non-zero if the working tree changes. The same step runs in CI; PRs that touch `internal/api/*.go` without regenerating `docs/swagger.{json,yaml,go}` fail.

**Route ↔ spec contract test** (Go-side): the test parses `docs/swagger.json` via `kin-openapi`, walks Gin's `engine.Routes()` after `Server.Handler()`, and asserts bidirectional `(method, path)` set-equality modulo a small allowlist for infrastructure paths (`/livez`, `/readyz`, `/metrics`, `/admin/cache`, `/debug/last-queries`, `/openapi.yaml`, `/openapi.json`, `/docs/*`). Any divergence — handler added without an annotation, annotation pointing at a removed route — fails the test.

- Why swag v2: lowest churn for an existing Gin codebase. Annotations live next to the handlers that implement the documented behaviour. Generated artefacts double as input to the drift gate and to the contract test.
- Why Scalar over Swagger UI: better default UX, smaller payload, native dark-mode, modern aesthetic. Drop-in replacement — both consume the same OpenAPI 3.0 spec.
- Why vendoring the UI bundle: deployment topology assumes restricted-network environments (Kubernetes operators, internal tools). A `/docs` route that requires reaching `cdn.jsdelivr.net` is silently broken in those environments. Vendoring guarantees the route works wherever the binary runs.
- Alternatives considered:
  - Hand-maintained `docs/openapi.yaml` (rejected — drift risk; swag v2 reduces effort while preserving control via annotations).
  - Huma (rejected — full framework refactor, see D20-style trade-off).
  - CDN-loaded UI (rejected — air-gap-incompatible).
  - Swagger UI bundled instead of Scalar (rejected — heavier bundle, dated UX; spec consumers still get the same JSON/YAML).

### D23. Test framework: testify across the repository

**Adoption scope**: every test file under `internal/`, `tests/`, and `cmd/` uses `github.com/stretchr/testify/{assert,require,suite}`. No test file mixes stdlib `t.Errorf` / `t.Fatal` patterns with testify in the same suite (one style per file).

**Migration cadence**: a single dedicated PR converts all 57 existing tests in one pass. Mechanical refactor; no behaviour changes. Smaller PRs deferred — the larger one-shot diff is easier to review than ten micro-PRs each with the same shape.

**Patterns**:

- **`require`** for "if this fails, the rest of the test is meaningless" — fixture setup, container start, JSON unmarshal of the response under test.
- **`assert`** for individual checks within a test — encourages the test to surface multiple failures per run.
- **`suite.Suite`** for the testcontainers integration package only. `SetupSuite` starts the VictoriaMetrics container; `TearDownSuite` stops it; `SetupTest` resets fixtures; tests are methods on the suite struct. Stdlib unit / golden / property tests stay function-shaped.
- **`assert.JSONEq`** for wire-shape comparisons in golden tests so byte-for-byte diff isn't required.
- **`assert.Eventually`** for the VM-readiness poll in integration tests.

**`testifylint`** is added to the curated `golangci-lint` set (D21) and configured with `enable-all: true`. It catches the common testify misuses (`assert.True(t, a == b)` → `assert.Equal`, missing `t.Helper()` in helpers, `assert` calls inside goroutines without `t.Cleanup`, etc.).

- Why testify across the whole repo, not just integration: a single style is easier for contributors to read and grep. The mass-migration cost is one focused PR; the long-tail cost of mixed styles is forever.
- Why one PR rather than per-package: the diff is mechanical, the cognitive load of reviewing it is the same across packages; bundling reduces churn windows where new code lands in the old style and needs re-translation.
- Alternatives considered:
  - Stay on stdlib (rejected — integration tests with testcontainers benefit materially from `suite.Suite` and `require`; the rest of the repo is along for consistency).
  - Adopt `gotest.tools/v3` instead (rejected — testify is the dominant Go ecosystem choice and what `testifylint` polices; staying with the stream).

## Risks / Trade-offs

- [Cold cache miss latency] → Document that first-time-bucket queries pay the full multi-cluster PromQL fan-out (target ≤ 3 s for ≤ 5 k pods aggregated across clusters in scope); subsequent same-bucket queries are cache hits. Surface `kube_state_graph_build_duration_seconds` per `cache_status`.
- [Pod UID churn on restart pollutes long lookback windows] → For windows where `last_over_time(kube_pod_info)` returns multiple UIDs for the same `(cluster, namespace, name)` tuple within the window, keep the latest UID and emit a `pod-replaced-by` synthetic edge linking the prior UID to the current one. Document in the spec.
- [Service-graph metrics absent or sparse] → Topology-only graph is still valid; missing service-graph series produce zero `pod-calls-pod` edges instead of a build failure.
- [PromQL fan-out large with many clusters] → `--clusters-allowlist` bounds the upstream cost; `--max-pods` triggers `503` with reason `cluster_too_large` when exceeded. The cache absorbs cost across callers.
- [Cache memory growth on diverse query patterns] → Bound by `MaxCost` (default 256 MiB); evictions exposed via `kube_state_graph_cache_evictions_total`.
- [Ristretto async-write race with singleflight] → Mitigated by populating singleflight return value in-band and treating cache as a best-effort warmup.
- [Inconsistent `cluster` external label across scrape pipelines] → Series missing the `cluster` label are bucketed under `cluster="unknown"` and surfaced via `kube_state_graph_clusters_observed`; document that operators must set the label uniformly.
- [Cross-cluster edge with one endpoint missing topology data] → If the producer emits a `traces_service_graph_request_total` series whose `client_k8s_pod_uid` or `server_k8s_pod_uid` does not appear in any cluster's `kube_pod_info` for the window, the missing endpoint is rendered as a synthetic ghost pod node (`attrs.ghost=true`) carrying only its `cluster` and `pod_uid`, instead of dropping the edge.
- [`kube-state-metrics` retention in VictoriaMetrics shorter than requested window] → `last_over_time` returns empty; respond `400 Bad Request` with `reason: "outside retention"` when zero topology rows are returned for a window covered by upstream `up{}` data.
- [Fake fixtures producer in the harness diverges from real producers] → Pin the metric names, label set, and cluster-label discipline the harness uses to D8, so swapping in real producers is a configuration change rather than a code change.
- [No auth on the API] → Document that the service is intended to sit behind a reverse proxy.
- [Single-instance cache lost on restart] → Acceptable for v1; warm-up cost is bounded by `--max-window` and typical caller traffic. v1.1 escape hatch is a shared Redis L2.
- [Multi-cluster cardinality on self-metrics] → `cluster` label appears only on observational gauges (`graph_node_count`, `graph_edge_count`); document expected `cluster` cardinality range (≤ 20 in v1) and recommend dropping the label at the scrape layer if it grows beyond budget.

## Migration Plan

Greenfield repository — no migration. Rollback is `git revert` of the merge commit. The JSON contract is versioned via a top-level `apiVersion: "v1"` field so consumers can detect breaking changes.

## Open Questions

- Final list of edge types beyond the three in D4 (e.g., `pod-replaced-by`, `pod-shares-node`, `pod-shares-namespace`) — resolve during spec drafting; whichever ship in v1 must appear in both `Build()` and the static `/v1/edge-types` registry.
- Default value of `--max-window` (current proposal `24h`) and whether different time classes should have different ceilings.
- Bucket-boundary policy across DST or leap seconds — likely "always UTC, no DST adjustment", confirm during spec.
- Whether to ship the optional L2 serialised-response cache (D6) in v1 or defer to v1.1 — defer until profiling shows serialise+ETag is hot.
- Whether `/v1/edge-types` should ever support time-window filtering — defer to v1.1.
- Whether `/v1/clusters` should also report per-cluster pod / node counts in its response, or keep it minimal (names + first-seen / last-seen) — defer to spec.
- ~~Fake-fixtures program shape: continuous Deployment with steady-state metrics vs YAML-driven snapshot replayer~~ — resolved: no fixtures program. Local rig uses real `kube-state-metrics`; integration tests (`internal/integration/`) ingest series directly via `POST /api/v1/import/prometheus` to a `testcontainers-go` VictoriaMetrics container.
- Exact Grafana Node Graph dashboard JSON to ship in `deploy/grafana/` for visual verification, including a layout that highlights cross-cluster edges — defer to harness spec.
- Whether `?format=` query parameter on `/v1/graph` is preferable to a separate `/v1/graph/nodegraph` route — defer to spec; current preference is the separate route.
- Whether `KSG_EXTERNAL_NAME_PATTERN` should evolve to a regex (`KSG_EXTERNAL_NAME_REGEX`) or accept multiple comma-separated patterns — defer to v1.x based on real deployment feedback.
- Whether external nodes should expose any additional `labels` (e.g., scheme parsed out of URL-shaped values) — defer; v1 keeps `labels.pattern` only.
