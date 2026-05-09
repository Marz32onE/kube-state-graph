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

- Go 1.25+ standard library `log/slog` for logging (toolchain pinned to `go1.26.2`).
- Gin for HTTP routing.
- `github.com/prometheus/client_golang/api` and `.../api/v1` for outbound queries.
- `golang.org/x/sync/errgroup` and `.../semaphore` for parallel fan-out and concurrency capping.
- No Kubernetes client-go, no informers, no direct VictoriaMetrics SDK.
- Single configurable upstream URL (the centralised VictoriaMetrics Prometheus-compatible endpoint).

## Goals / Non-Goals

**Goals:**
- Ship a Go (Gin) HTTP server that returns a unified nodes-and-edges JSON document for one or more Kubernetes clusters in a caller-specified time range `[start, end]`, computed from VictoriaMetrics on demand.
- Expose **cross-cluster** RPC edges (`pod-calls-pod` where the source and target pods resolve to different clusters via the global pod-UID index) as first-class graph elements.
- Build the graph by issuing PromQL queries with `@` timestamp modifiers and range-aware functions (`last_over_time`, `rate`) against centralised VictoriaMetrics, and joining the result sets in memory across all clusters in scope.
- Each request runs a fresh upstream fan-out — there is no in-process result cache and no singleflight. Responses still carry an HTTP `ETag` (sha256 of the body) so clients may revalidate via `If-None-Match` and skip transferring the body when it would be unchanged. A horizontally scalable cache mechanism is deferred to a future iteration (see "Future cache mechanism" below).
- Use the `(cluster, pod-uid)` composite as the stable identity for pod nodes and the join key for pod-pod edges; node and PVC IDs are similarly cluster-scoped.
- Expose Cytoscape.js-shaped JSON as the primary response, plus a Grafana Node Graph compatibility route for visual verification.
- Provide cluster discovery (`GET /v1/clusters`) sourced live from VictoriaMetrics, plus a static edge-type catalogue (`GET /v1/edge-types`).
- Provide an integration-test harness (single Kind cluster, in-cluster VictoriaMetrics, fake-fixtures producer for multi-cluster `kube_*` and `traces_service_graph_*` series, smoke script) that proves the API server returns a non-empty, well-formed multi-cluster graph including a cross-cluster edge.

**Non-Goals:**
- Implementing the customised service-graph collector (Alloy / OTLP collector). The harness uses a fake-fixtures producer that writes the contract metrics directly.
- Operating, configuring, or hardening `kube-state-metrics` or VictoriaMetrics. They are dependencies, not deliverables.
- Talking to the Kubernetes API directly in any cluster. All cluster facts are read via metrics.
- Authorisation, multi-tenant isolation, or TLS termination on the HTTP API (assume reverse proxy handles it). Per-cluster RBAC is also out of scope — every reachable cluster is equally readable through this server. v1 ships static **API-key authentication** only (single shared secret tier, no per-caller scoping); see D24.
- Ingesting traces. Trace-derived metrics are produced upstream; the API server only reads the resulting metric series.
- Real-time streaming or WebSocket APIs.
- An in-process result cache. v1 deliberately ships **no** server-side build cache and **no** singleflight; every request runs a fresh upstream fan-out. ETag-based HTTP revalidation is the only caching layer.
- A distributed / shared cache (Redis, memcached) or background materialiser. These are explicitly deferred — a future iteration will add a horizontally scalable cache mechanism for distributed deployment; the design space is captured under "Future cache mechanism".
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

### D2. On-demand time-ranged build, no server-side snapshot, no result cache

Every request to `GET /v1/graph?start=...&end=...` triggers a fresh build of the multi-cluster graph for the supplied window:

1. Resolve and validate `start` / `end` (only `end > start`; D5).
2. Run all required PromQL queries against centralised VictoriaMetrics in parallel via `errgroup.WithContext` under a per-build `context.WithTimeout(ctx, --build-timeout)`. On `context.DeadlineExceeded`, abort and return `504 Gateway Timeout` with `reason: "timeout"`. Concurrency limiting is delegated to horizontal scaling (HPA) and Pod resource limits — there is no in-process semaphore.
3. Join the result sets across clusters in memory and produce the global multi-cluster `Graph`.
4. Apply filters (`cluster`, `namespace`, `edge_type`, `name`) and traversal pruning (`root`, `depth`, `direction`) over the freshly built `Graph` as a projection, then serialise to the requested format (Cytoscape.js or Grafana Node Graph), compute `ETag = sha256(body)`, honour `If-None-Match` if present, and return.

There is no in-process result cache, no singleflight, no background `Snapshotter`, no `atomic.Pointer[Graph]`, no fixed refresh interval, and no `POST /admin/refresh`.

- Why: keeps the v1 implementation small and lets a future iteration choose a cache mechanism appropriate for distributed deployment (Redis, materialised-view tier, graph DB) without unwinding an in-process cache assumption first. ETag still gives clients a free conditional-GET path; the upstream cost remains O(requests) until that future iteration lands.
- Alternatives considered:
  - In-process Ristretto + singleflight (the previous design — moved out of v1; revisit when distributed deployment is on the table).
  - Periodic snapshot (rejected — incompatible with time-travel queries; staleness window forces a worst-case freshness penalty even when no caller needs it).
  - Background materialiser writing to a shared store (deferred — captured under "Future cache mechanism").

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

### D5. Time-range passthrough

`start` and `end` are mandatory query parameters in either RFC 3339 or Unix seconds form. The only server-side validation is `end > start`. Beyond that check, the timestamps are passed through to upstream PromQL verbatim (`<window> = end - start`, `<end>` is the caller-supplied `end`). There is no server-side bucketing, alignment, grid, max-window cap, or future-time guard.

- Why no window cap: bounded query cost is delegated to upstream VictoriaMetrics search limits (`-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`, `-search.maxSamplesPerQuery`). Duplicating these in KSG adds a configuration knob with weak business value and a confusing layered failure mode.
- Why no skew guard: NTP drift is a deployment concern; future-time queries against PromQL return empty results, which the caller sees as an empty graph. A dedicated KSG-side check provided marginal diagnostic value at the cost of a config knob.
- Why pass-through: with no cache, alignment provides no hit-rate benefit; `last_over_time` / `rate` lookbacks span minutes, so sub-second `@end` drift is not load-bearing for upstream evaluation. Removing alignment also removes a Go package, a `Window` struct in handler signatures, and several rounds of doc/spec coordination.
- Bounded query cost is delegated entirely to upstream VictoriaMetrics search limits.
- Alternatives considered:
  - 60 s `floor`/`ceil` grid (removed in this change — was originally a cache-key bucket; ETag stability argument was post-hoc and weak in practice since real callers don't refresh sub-second).
  - Per-class TTL ladder (deferred — would only matter once a server-side cache returns; revisit in the future cache mechanism).

### D6. Conditional GET via response validator (ETag); no in-process result cache

v1 emits an HTTP `ETag` **strong validator** on every graph response. ETag is a response identity mechanism per RFC 9110 §8.8.3 used for **conditional GET / revalidation** (§13.1) — it is not a cache. The previous design's three-tier stack (Ristretto + singleflight + server-side cache) is removed. There is no in-process result cache, no request coalescing, and no `/admin/cache` route. Each request runs a fresh upstream fan-out and recomputes the response body.

**Validator mechanism.** After projection + serialisation, the server computes `ETag: "<sha256 of body>"` and writes it on the response. A client (or any HTTP intermediary) may store that validator and, on the next request to the same URL, send `If-None-Match: "<etag>"`. The server still runs the full upstream fan-out and serialisation pipeline, then compares the freshly computed sha256 against the supplied validator: identical ⇒ `304 Not Modified` with empty body and the same `ETag`; different ⇒ `200 OK` with the new body and the new `ETag`. The validator is content-addressed; no server state persists between requests.

The 304 path saves the response body's bytes-on-the-wire (and the client's deserialisation cost) but does NOT save the upstream PromQL fan-out — that is by design at v1 scale and is the trade-off for shipping no server-side build cache. Whether any party (browser, reverse proxy, downstream service) caches the response body for some TTL is a client / intermediary policy and is independent of the server.

**ETag determinism prerequisites.** sha256(body) is stable iff the body is byte-identical for the same `(window, filters, upstream-data)`. The serialiser guarantees this by:

- Sorting `view.Nodes` and `view.Edges` (`graph.SortNodes` / `graph.SortEdges`) before encoding.
- Sorting `Graph.ClusterNames()`.
- Relying on `encoding/json` map-key sorting for `labels map[string]string`.
- Keeping body shape fixed at `{apiVersion, clusters, elements}`. No time-varying or echo-of-input fields are serialised; observability moves to logs/metrics.

Routes whose content is stable and long-lived emit explicit `Cache-Control` (`/v1/edge-types`: 3600 s, `/openapi.{yaml,json}`: 3600 s, `/docs/assets/*`: 86400 s, `/docs`: 300 s). Those headers communicate freshness windows for resources whose content rarely changes — they are independent of the ETag validator on `/v1/graph` and reflect content stability rather than a server-side build cache. `/v1/graph`, `/v1/graph/nodegraph`, and `/v1/clusters` emit only `ETag`: the server cannot tell the client how long a freshly built graph remains "fresh" without re-querying upstream, so cacheability is left to the client / intermediary.

**No singleflight.** Concurrent identical requests each run their own upstream fan-out. At dev / pre-distributed-deployment traffic this is acceptable. Cluster-wide deduplication is part of the future cache mechanism, not v1.

**Future cache mechanism.** Out of scope for v1 but explicitly anticipated. The likely shape (subject to a separate change) is one of:

- **Background materialiser + shared store** — a leader-elected worker pulls VM on a fixed cadence, writes the graph to a shared store (Redis Cluster, graph DB, or columnar archive). API replicas become stateless readers with pushdown filtering. Suits 1 M+ nodes / 10 M+ edges; bounds memory per replica.
- **Per-replica L1 + shared L2 (Redis)** — Ristretto reappears in front of a network-shared encoded `*Graph`. Cheaper to add than a materialiser, but does not solve heap pressure at million-node scale.
- **Graph DB as the materialised store** — only justifiable once in-memory `*Graph` ceases to fit; trades pointer-walk traversal for indexed Cypher with disk-backed working set.

Whichever shape ships will need to revisit D5 (time-class TTL ladder), D11 (cache-key hashing), D12 (cache metrics), and D14 (cache contract). v1 deliberately leaves these holes empty rather than committing to an implementation that may not match the chosen distributed shape.

### D7. Filtering, cluster scoping, and partial-graph traversal

`GET /v1/graph` accepts (in addition to mandatory `start` / `end`):

- `?cluster=<name>` — repeatable; restricts the response to nodes whose `cluster` is in the set. Cross-cluster edges with one end inside the set and one end outside are **kept** (the remote endpoint resolves correctly because the freshly built `*Graph` holds all clusters loaded for the window); the remote endpoint node is also kept (with its own `labels.cluster`). Cross-cluster status is conveyed by comparing the source-node and target-node `labels.cluster` — consumers derive the boolean from the two strings (the edge itself carries only `labels.cluster` = trace-source / client-side cluster). Setting `cluster` to an unknown value is not an error — it simply yields an empty result for that name.
- `?namespace=<ns>` — repeatable; restricts pod / PVC nodes whose `namespace` is in the set. A namespace value matches across clusters; combine with `?cluster=` to scope to a single cluster's namespace.
- `?edge_type=<type>` — repeatable; restricts to those edge types only. If a requested type has no edges in the current `Graph`, that type is silently skipped (no error, just empty).
- `?name=<value>` — repeatable; matches `n.Name()` by exact equality across **every** node type (`PodNode`, `K8sNode`, `PVCNode`, `ExternalNode`). Use it to anchor the view on any single node — a pod, a host node, a PVC, or an external endpoint — without the caller having to encode the type. Names are not globally unique (a pod and a K8s node can share a name; a PVC name can repeat across namespaces); all matches are returned. Combine with `?cluster=` / `?namespace=` to disambiguate.
- `?root=<id>&depth=<n>&direction=in|out|both` — partial-graph traversal: BFS from the given composite ID (`<cluster>/<pod-uid>` or `<cluster>/<node-name>`), bounded by `depth` (default 2, max 6).

Filtering is applied **at response time over the freshly built `*Graph` value**, not by re-querying upstream. PromQL queries always fetch the full window across every cluster present in upstream VictoriaMetrics; the in-memory `*Graph` is the shared base from which all filtered views are projected.

- Why: filter+serialise is microseconds for typical v1 graph sizes (≤ 5 k pods × ≤ 10 clusters); pushing filters to PromQL would re-evaluate per filter combination at upstream cost. When the future cache mechanism lands, the same projection-over-graph contract preserves filter shareability across cache entries.
- Empty filter ⇒ full multi-cluster graph for the time range.
- Filters compose with AND across types and OR within a type.
- Traversal first prunes by `root`/`depth`/`direction`, then `cluster` / `namespace` / `edge_type` / `name` filters apply over the traversal result.
- Edge re-add rule (unified across all filters): an edge survives when at least one resolved endpoint is in scope after node filtering. When exactly one endpoint is in scope, the missing endpoint is re-added from `g.NodesByID` provided it passes the non-cluster filters (namespace). This covers cross-cluster `pod-calls-pod` edges where only `cluster` narrows scope (the partner pod lives in an out-of-scope cluster), `pod-runs-on-node` / `pod-mounts-pvc` edges incident on an in-scope pod, and name-anchored views that need to render incident edges of the named node together with their partners. There is no name-specific suppression: anchoring on a named node intentionally surfaces the edges that connect it to its neighbourhood, otherwise the rendered graph would have dangling edge endpoints.
- The `pod_uid` filter parameter was considered and rejected: pod UIDs are opaque internal identifiers callers cannot obtain without first making a `/v1/graph` call. Callers scope by `cluster` + `name` instead, accepting that names are not globally unique.
- Alternatives considered:
  - PromQL label-selector narrowing per request (rejected — VictoriaMetrics scans the index regardless; label selectors trim the network payload but not upstream evaluation cost. The full multi-cluster graph is small enough at v1 scale to build once and project per request).
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
- Why UUIDv5 for edge `id`: deterministic (golden tests and ETag stay stable; same edge → same ID across rebuilds), RFC 4122 compliant, and decoupled from the human-readable `(source, target, type)` triple so renaming convention later does not change IDs already exposed.
- Alternatives considered:
  - `kind`/`label`/`attrs` field names (rejected — divergent from user-requested schema).
  - Random UUIDv4 for edges (rejected — breaks ETag stability and golden tests; same edge would get a different ID every build).
  - Plain `{nodes, edges}` only (rejected — locks out Grafana Node Graph compat without an adapter layer).
  - GraphQL (rejected — adds dependency surface for v1 with no clear caller).

### D10. Logging via `log/slog`, JSON handler

`slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: ...}))` set as default logger; level configurable. Every HTTP request emits one structured log line with method, path, status, duration, request ID, and applied `cluster` filter values.

One additional log line per build: `slog.Info("graph built", "duration_ms", ..., "clusters", ..., "nodes", ..., "edges", ..., "cross_cluster_edges", ..., "queries", ..., "failures", ..., "start", ..., "end", ...)`.

- Why: required by the user; standard library only.
- Alternative: zap / zerolog (rejected — extra dependency, not requested).

### D11. Implementation tactics

These are mandatory for the v1 implementation:

- **Sealed graph node types**: Go interface `GraphNode` with concrete `PodNode`, `NodeNode`, `PVCNode`. Each implementation surfaces the canonical fields (`ID()`, `Name()`, `Type()`, `Labels()`) consumed by the serialisers in D9. The `cluster` value lives inside `Labels()["cluster"]` rather than as a separate first-class field on the wire.
- **Pure join layer**: `Build(topology Topology, edges []ServiceGraphEdge) *Graph` is a pure function over typed Go structs and produces the full, unfiltered multi-cluster graph for the time window. All HTTP- and Prometheus-free unit tests target this function. Cross-cluster edges are produced when the resolved source-pod cluster differs from the resolved target-pod cluster (server-side cluster comes from the topology pod-UID index lookup, not from a metric label).
- **Pure projection layer**: `Project(g *Graph, scope Scope) GraphView` applies cluster / namespace / node / edge_type filters and traversal over an immutable `*Graph` and returns a read-only view. No allocations of new node/edge structs, just slices of pointers.
- **Query registry**: PromQL strings as named constants in one file, parameterised on `<window>` and `<end>`. Paired with a parser that maps Prometheus `model.Vector` results into typed Go structs.
- **One PromQL instant query per metric family**, evaluated at the bucketed `end` with `last_over_time` / `rate` over the window. Queries do **not** include filter-derived selectors. Parse Vector client-side.
- **Parallel upstream fan-out** via `errgroup.WithContext`. Wall-clock latency = O(slowest query), not O(sum of queries).
- **Per-build context timeout**: graph endpoints (`/v1/graph`, `/v1/graph/nodegraph`) wrap the build in `context.WithTimeout(ctx, --build-timeout)` (default `15s`). On `context.DeadlineExceeded`, the build is aborted, the failure counter increments, and the request returns `504 Gateway Timeout` with `reason: "timeout"`.
- **Per-request timeout for non-graph endpoints**: `/v1/clusters` (live discovery query) and `/readyz` (`up{}` probe) wrap their upstream call in `context.WithTimeout(ctx, --api-timeout)` (default `5s`). On `context.DeadlineExceeded`, the request returns `504 Gateway Timeout` with `reason: "timeout"`. Endpoints with no upstream call (`/v1/edge-types`, `/livez`, `/metrics`, `/openapi.*`, `/docs*`) are not subject to this timeout.
- **No in-process concurrency cap.** Concurrency limiting is delegated to horizontal scaling (HPA) and Pod resource limits. The previous `golang.org/x/sync/semaphore`-based per-instance cap and the `503 capacity` error reason were removed: HPA reacts to CPU / latency signals at instance granularity and is the operator's primary lever; an in-process semaphore added a config knob whose tuning required the same load data HPA already uses, while making per-instance load shedding less observable than queue-time-based metrics.
- **Adjacency maps**: forward and reverse `map[NodeID][]*Edge` built once inside `Build()`; reused for traversal pruning during `Project()`. Built on the immutable `*Graph` so concurrent projections within the same request share them safely.

### D12. Self-metrics and operability

The server exposes its own `/metrics` endpoint (Prometheus exposition) with at least:

- `kube_state_graph_build_duration_seconds` (histogram — wall-clock build time per request).
- `kube_state_graph_project_duration_seconds` (histogram — filter + traversal pruning).
- `kube_state_graph_serialise_duration_seconds{format}` (histogram — JSON encode + ETag computation).
- `kube_state_graph_build_rejected_total{reason="timeout"}` (counter).
- `kube_state_graph_graph_node_count{cluster,kind}` (gauge — last build only, observational; bounded by configured cluster count).
- `kube_state_graph_graph_edge_count{type,cross_cluster}` (gauge — `cross_cluster` ∈ `{"true","false"}`).
- `kube_state_graph_clusters_observed` (gauge — unique `cluster` values seen in the last build).
- `kube_state_graph_upstream_query_duration_seconds{query}` (histogram).
- `kube_state_graph_upstream_query_failures_total{query}` (counter).
- `kube_state_graph_http_requests_total{path,status}` (counter).

Health endpoints:

- `GET /livez` — always 200 while the process is up.
- `GET /readyz` — 200 iff a cheap upstream probe (`up{}` instant query, wrapped in `context.WithTimeout(ctx, --api-timeout)`) succeeds. `503 Service Unavailable` if the probe fails (semantically: not ready to serve traffic — the standard Kubernetes liveness/readiness convention).

Operator endpoints: none in v1. Diagnostics rely on `kube_state_graph_*` metrics and structured request logs.

### D13. Testing layers

The test stack has six layers, all CI-runnable except the last; each MUST exist before this change is archived:

| Layer | CI? | Scope | Tool |
|------|-----|------|------|
| Unit | yes | Pure join / parse / project functions on hand-crafted multi-cluster `model.Vector` and `model.Matrix` inputs (intra-cluster, cross-cluster, and mixed) | `go test` |
| Component | yes | Build pipeline end-to-end against an `httptest.Server` mocking the Prometheus query API; covers per-build timeout, parameter validation, and serialiser output | `go test` |
| Golden | yes | Canned scenarios (single-cluster, two-cluster with cross-cluster edge, three-cluster with traversal pruning) → `/v1/graph`, `/v1/graph/nodegraph`, `/v1/clusters`, `/v1/edge-types` JSON compared to checked-in `.golden.json` | `go test` |
| Property | yes | Random topology + edge inputs across N synthetic clusters + random filters → invariants (no orphan edges, no duplicate IDs, every endpoint resolves, filtered ⊆ unfiltered, traversal stays within `depth`, cross-cluster edges have distinct cluster endpoints) | `testing/quick` or `gopter` |
| **Container integration** (capability `container-integration`) | yes | Per-package VictoriaMetrics container started via testcontainers-go; series injected via VM's `/api/v1/import/prometheus`; in-process API server pointed at the container; assertions over real PromQL evaluation and real ETag flow | `go test` + Docker |
| **Manual visual rig** (capability `verification-harness`) | **no** | Single Kind cluster with VictoriaMetrics + fake-fixtures producer + API server + Grafana Pod with the checked-in Node Graph dashboard, run on demand by an operator. Used for visual sanity verification of the rendered graph; not exercised by CI | `bash` bootstrap + browser |

The first five layers run on every PR via `go test ./...`. The Kind manual rig is exercised by operators on demand only — see D20 for testcontainer rationale and D21 for static analysis / vulnerability scanning policy.

- Why: integration alone leaves logic regressions undetectable in PR feedback; mock-only component tests miss real PromQL semantics; Kind alone is too slow and fragile for per-PR feedback. The split puts every behavioural assertion in the CI path against real PromQL, while the Grafana rig keeps human-in-the-loop verification first-class without coupling it to merge gates.

### D14. Versioning

- All HTTP routes are prefixed `/v1/`. v2 can coexist on the same binary if the JSON shape ever breaks.
- The body carries `apiVersion: "v1"` so off-the-wire consumers can detect breaks.
- New edge types and new `attrs` fields are additive only; removed fields are a v2 break.
- `connection_type` values from the producer contract are mapped to a stable internal enum so a producer-side rename does not propagate into the API contract.
- `cluster` label values pass through as opaque strings; renaming a cluster upstream is a caller-visible change, not an API break.

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
- Caching: response carries `Cache-Control: public, max-age=3600` and an `ETag` derived from the registry's compile-time hash.
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

`cluster` is repeatable; absent ⇒ all clusters in the freshly built graph. Path-based per-cluster URLs were considered and rejected: cross-cluster edges naturally span more than one cluster, so a single-cluster path implies a scope smaller than the data — leading either to lossy responses (drop cross-cluster edges) or surprising responses (include endpoints outside the path). Query-param multi-select avoids this entirely.

**Discovery.** `GET /v1/clusters` returns the list of clusters that have data in centralised VictoriaMetrics, derived live from `group by (cluster) (last_over_time(kube_node_info[1h]))`. The lookback is fixed at `1h` (sufficient to absorb transient KSM scrape gaps; not configurable). Each request hits VictoriaMetrics directly — there is no in-process discovery cache in v1. The response carries an `ETag` so callers may revalidate cheaply via `If-None-Match`.

**Cross-cluster edges.** `pod-calls-pod` edges where the resolved source and target pods live in different clusters are emitted as ordinary edges with both endpoint nodes present in the freshly built graph (since each build holds the global multi-cluster graph). When a request scopes to a subset of clusters, cross-cluster edges that touch the selected set are kept along with both endpoint nodes — the remote node's `labels.cluster` makes the cross-cluster context obvious to renderers. The edge carries `labels.cluster` set to the trace-source cluster (i.e. the client-side pod's cluster); consumers detect cross-cluster status by comparing the source-node and target-node `labels.cluster` (a boolean shortcut field is deferred to the future typed struct described in D9).

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

### D19. Bounded upstream cost

Bounded query cost (per-query duration, samples, points) is delegated entirely to upstream VictoriaMetrics search limits — KSG does not duplicate them. Operators running large fleets SHALL configure `-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`, and `-search.maxSamplesPerQuery` on VM and rely on `502 Bad Gateway` (with `reason: "upstream"`, mapped from VM 5xx) for overflow signalling. Per-cluster scope narrowing is a caller-side concern via the `?cluster=` query parameter on `/v1/graph`; the server itself loads every cluster present in upstream VM on each build.

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
        └─ exercise /v1/* endpoints, assert HTTP shape / headers / ETag round-trip behaviour
```

Decisions:

- **One container per package**, not per test: bootstrapping VM costs ~5–10 s, far more than each test. Tests inside a package use unique series-label discriminators (e.g., a `test=<TestName>` label) so they never collide.
- **Direct injection via `/api/v1/import/prometheus`**, not a scrape stub: keeps the test process self-contained (no second container, no scrape interval to tune), and the API server only sees series in VM regardless of how they got there. The Prometheus exposition format is hand-written by the test, supports per-sample timestamps, and is the same format the manual-rig fixtures producer emits.
- **In-process API server** (`api.New(...).Handler()` against `httptest.NewServer`): no third container; tests can introspect server state, share types, and avoid Docker round-trips. Containerised server behaviour is covered by the manual rig instead.
- **Absolute timestamps in fixtures**: tests use fixed timestamps (e.g., `time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)`) and pass the same window to the API. This makes time-window alignment fully deterministic — a class of bugs the httptest mock layer cannot expose.
- **VM image is pinned** by tag in test code; no `:latest`. Image is pre-pulled in CI to remove first-run noise.
- **Docker socket required**: the integration-test job runs on `ubuntu-latest` GitHub Actions runners (Docker socket native). macOS / Windows runners are out of scope for this layer.
- **ETag round-trip determinism**: the integration suite asserts that two consecutive `/v1/graph` requests for identical inputs return identical `ETag` values, since v1 has no result cache and any non-determinism in the build/serialise path would surface here. See `TestRepeatedRequestsReturnSameETag`.

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

**Route ↔ spec contract test** (Go-side): the test parses `docs/swagger.json` via `kin-openapi`, walks Gin's `engine.Routes()` after `Server.Handler()`, and asserts bidirectional `(method, path)` set-equality modulo a small allowlist for infrastructure paths (`/livez`, `/readyz`, `/metrics`, `/openapi.yaml`, `/openapi.json`, `/docs/*`). Any divergence — handler added without an annotation, annotation pointing at a removed route — fails the test.

- Why swag v2: lowest churn for an existing Gin codebase. Annotations live next to the handlers that implement the documented behaviour. Generated artefacts double as input to the drift gate and to the contract test.
- Why Scalar over Swagger UI: better default UX, smaller payload, native dark-mode, modern aesthetic. Drop-in replacement — both consume the same OpenAPI 3.0 spec.
- Why vendoring the UI bundle: deployment topology assumes restricted-network environments (Kubernetes operators, internal tools). A `/docs` route that requires reaching `cdn.jsdelivr.net` is silently broken in those environments. Vendoring guarantees the route works wherever the binary runs.
- Alternatives considered:
  - Hand-maintained `docs/openapi.yaml` (rejected — drift risk; swag v2 reduces effort while preserving control via annotations).
  - Huma (rejected — full framework refactor, see D20-style trade-off).
  - CDN-loaded UI (rejected — air-gap-incompatible).
  - Swagger UI bundled instead of Scalar (rejected — heavier bundle, dated UX; spec consumers still get the same JSON/YAML).

### D24. API-key authentication (header `X-API-Key`)

**Header.** Callers present a single key in `X-API-Key: <key>`. `Authorization: Bearer` was considered and rejected: `X-API-Key` is unambiguous (no scheme parsing), simpler for ops to set on Grafana datasources and `curl`, and avoids implying OAuth-style scope.

**Key sources.** Two flags, file takes precedence:

- `--api-keys-file <path>` / `KSG_API_KEYS_FILE` — one key per line, blank lines and `#` comments tolerated. Designed for Kubernetes `Secret` volume mounts. Re-read every `--api-keys-reload-interval` (default `30s`, `0` disables) so a `kubectl apply` on the Secret rotates keys without a Pod restart.
- `--api-keys` / `KSG_API_KEYS` — comma-separated literal. Dev / one-shot use only.

When neither is set the keyset is **empty** and the middleware is a no-op (auth disabled). The server logs a warning at boot in that case so the operator notices an unintended dev posture in production.

**Protected vs open routes.**

| Open (no key) | Protected (`X-API-Key` required) |
|---|---|
| `/livez`, `/readyz` (kubelet probes) | `/v1/graph`, `/v1/graph/nodegraph`, `/v1/clusters`, `/v1/edge-types` |
| `/metrics` (Prometheus scrape; gate via NetworkPolicy or a separate listen address in production) | |
| `/openapi.yaml`, `/openapi.json`, `/docs`, `/docs/assets/*` (UI must load) | |

**Validation.** `crypto/subtle.ConstantTimeCompare` per stored key, with a same-length filler comparison for stored keys whose length differs from the presented value. The full key set is iterated on every call so neither match latency nor early exit leaks the key count or the matching position.

**Reload semantics.** File reload is implemented via an `atomic.Pointer` swap on the underlying slice. In-flight requests use whichever pointer they captured; no locking. Combined latency for a Kubernetes `Secret` rotation is `kubelet sync (~60s)` + `--api-keys-reload-interval (30s default)` ≈ ~90s worst case.

**Failure mode.** Missing or invalid key → `401 Unauthorized` with `{"error":{"reason":"unauthorized","message":"…"}}`. The middleware also increments `kube_state_graph_auth_rejected_total{reason="missing|invalid"}`.

**Docs.** OpenAPI 3.0 declares `securitySchemes.ApiKeyAuth` (`in: header`, `name: X-API-Key`); every protected handler carries `@Security ApiKeyAuth` + `@Failure 401`. The Scalar UI surfaces an "Authentication" control so callers can paste a key and try requests live.

- Why static keys (not JWT / OIDC): the operator's expected deployment posture is "behind a reverse proxy with caller-side auth" plus a coarse server-side gate. Static keys cover the gate without dragging in an OIDC stack. Per-caller scoping is a follow-up if real deployments need it.
- Why no `/admin/keys` API: keys live in the K8s `Secret`; the rotation procedure is a `kubectl apply`, not an HTTP call. No code path can leak keys via the API.
- Logging: never log the presented key value. Logs include `auth=ok|disabled|denied` only.
- Alternatives considered:
  - `Authorization: Bearer` (rejected — X-API-Key chosen for simplicity, see Header note).
  - mTLS (deferred — operationally heavier; reverse proxy is the recommended TLS layer).
  - OAuth2 / OIDC (deferred — too heavy for v1's gate-only posture).

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

### D25. OpenTelemetry tracing and logging

**Why now**: D10 (`log/slog`, JSON handler) covers operator logs but ships no distributed-tracing surface. Once KSG sits behind a Grafana Node Graph dashboard, an Alloy / OTel Collector, and a centralised VictoriaMetrics, operators need per-request spans (which cluster, which PromQL leg was slow?) and trace-correlated logs (a single `trace_id` joining HTTP access, build pipeline, PromQL fan-out, projection, serialisation). The same OTel pipeline that already collects `traces_service_graph_*` from the workloads is the natural sink for KSG's own self-traces and self-logs.

**Stack**:

- `go.opentelemetry.io/otel` SDK + `sdktrace` + `sdklog`.
- `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` + `otlptracehttp` (HTTP/protobuf).
- `go.opentelemetry.io/otel/exporters/otlp/otlplogs/otlploggrpc` + `otlploghttp`.
- `go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin` for inbound HTTP spans.
- `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` for the outbound PromQL client transport.
- `go.opentelemetry.io/contrib/bridges/otelslog` for the slog → OTLP-logs bridge.
- `go.opentelemetry.io/otel/semconv/v1.27.0` for HTTP / RPC / DB attribute keys.

**Configuration surface**: OTel-standard environment variables only. No new CLI flags. No new `KSG_*` variables. Reading list:

- `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_TIMEOUT`, `OTEL_EXPORTER_OTLP_INSECURE` and their per-signal `_TRACES_` / `_LOGS_` variants.
- `OTEL_SERVICE_NAME` (default `kube-state-graph`), `OTEL_RESOURCE_ATTRIBUTES`.
- `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG`.

The SDK's stock env-var loaders are used (`otlptracegrpc.WithEnv`-style configs) rather than re-implementing parsing. When `OTEL_EXPORTER_OTLP_ENDPOINT` and both per-signal endpoint variables are unset the binary installs `noop.NewTracerProvider()` and a no-op slog handler bridge. This is the **default**; v1 deployments without an OTel collector incur zero export overhead and zero new background goroutines.

**Init sequence** (in `cmd/kube-state-graph/main.go`):

1. Parse flags.
2. Build resource: `resource.New(ctx, resource.WithFromEnv(), resource.WithProcess(), resource.WithHost(), resource.WithTelemetrySDK(), resource.WithAttributes(serviceName, serviceVersion, serviceInstanceID))`.
3. If endpoint configured → build OTLP trace exporter, batch span processor, sampler from env, install global `TracerProvider`. Else → install `noop.NewTracerProvider()`.
4. Same for logs (OTLP log exporter + batch log processor + global `LoggerProvider`, or no-op).
5. Set global propagator: `propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})`.
6. Build `*slog.Logger` with `slog.New(slogmulti.Fanout(stderrHandler, otelslog.NewHandler("kube-state-graph")))` (or equivalent multi-handler). Without OTLP enabled, the otelslog handler short-circuits via the global no-op `LoggerProvider`.
7. Defer `tracerProvider.Shutdown(shutdownCtx)` and `loggerProvider.Shutdown(shutdownCtx)` after the existing HTTP-server graceful shutdown so in-flight exports flush within the existing grace deadline.

**Span topology**:

```
GET /v1/graph                               (otelgin server span)
└─ kube-state-graph.build                   (Builder.Build)
   ├─ prometheus.query (kube_pod_info)      (errgroup leg 1)
   ├─ prometheus.query (kube_node_info)     (errgroup leg 2)
   ├─ prometheus.query (kube_node_status_addresses)
   ├─ prometheus.query (kube_pod_spec_volumes_persistentvolumeclaims_info)
   ├─ prometheus.query (kube_node_labels)
   └─ prometheus.query (traces_service_graph_request_total)
└─ kube-state-graph.project                 (filter / cluster scope / traversal)
└─ kube-state-graph.serialise               (Cytoscape or NodeGraph)
```

`prometheus.query` spans are siblings under `kube-state-graph.build` (driven by `errgroup`); they all carry `db.system=prometheus`, `db.statement=<rendered PromQL>`, `kube_state_graph.query_name=<one of the constants>`, and `server.address` / `server.port` derived from `--prom-url`. The PromQL HTTP client is wrapped with `otelhttp.NewTransport(...)` so the `traceparent` header is injected automatically and an additional client-side HTTP span is recorded per upstream call.

**Attribute set**:

| Span | Required attributes |
|------|---------------------|
| Server (otelgin) | `http.request.method`, `http.route`, `url.scheme`, `url.path`, `server.address`, `server.port`, `client.address`, `user_agent.original`, `http.response.status_code`, `kube_state_graph.etag` |
| `kube-state-graph.build` | `kube_state_graph.window_seconds`, `kube_state_graph.end_unix`, on success `kube_state_graph.cluster_count`, `graph.node.count`, `graph.edge.count` |
| `prometheus.query` | `db.system=prometheus`, `db.statement`, `kube_state_graph.query_name`, `server.address`, `server.port` |
| `kube-state-graph.project` | `graph.node.count`, `graph.edge.count` (post-filter), `kube_state_graph.filter.cluster`, `kube_state_graph.filter.namespace`, `kube_state_graph.filter.edge_type` |
| `kube-state-graph.serialise` | `kube_state_graph.serialiser` (`cytoscape` or `nodegraph`), `graph.node.count`, `graph.edge.count` |

`db.statement` carries the raw PromQL — operators with strict policy on logging query strings can opt out by setting `OTEL_TRACES_SAMPLER=always_off` (kills tracing globally) or by stripping the attribute at the Collector via a processor. Document the trade-off; do not redact in-binary because the readable PromQL is the highest-value debugging signal in a slow-build trace.

**Error recording**: any `error` returned to the build pipeline calls `span.RecordError(err)` and `span.SetStatus(codes.Error, reason.String())`, where `reason` is the existing `build.Reason`. The error-mapping helper in `internal/api/errors.go` (`mapBuildError`) is the single place this is wired so HTTP status, response `reason`, log line, and span status stay in lockstep.

**slog bridge**: `otelslog.NewHandler("kube-state-graph")` wraps the existing JSON / text handler in a multi-handler. A logger obtained from `slog.New(...)` is stashed on `*gin.Context` via the otelgin middleware so handlers call `slog.LogAttrs(ctx, ...)` (or the package-level `slog.InfoContext(ctx, ...)`) and receive `trace_id` / `span_id` automatically — both in the local stderr line and in the OTLP log record. The local handler is configured with `HandlerOptions{ ReplaceAttr: ... }` so the `trace.SpanContextFromContext` IDs appear in stderr output even when the otelslog bridge is no-op (i.e. tracing disabled but a span context exists in tests).

**Non-traced routes**: `otelgin` is installed on the `/v1/*` and `/debug/*` route groups only. `/livez`, `/readyz`, `/metrics`, `/openapi.yaml`, `/openapi.json`, `/docs`, `/docs/assets/*` are mounted on a separate router group without the middleware. Rationale: kubelet probes hit `/livez` once a second per Pod; a single 50-replica deployment would emit 50 spans/s of pure noise. `/metrics` similarly produces one span per scrape per Prometheus replica. Documentation routes are served without auth and are not interesting in a trace.

**Sampling**: default sampler is `parentbased_alwayson` (the OTel SDK default). Operators control rate via `OTEL_TRACES_SAMPLER=parentbased_traceidratio` + `OTEL_TRACES_SAMPLER_ARG=0.05` etc. Because v1 has no in-process result cache and `/v1/graph` is the cost-dominant endpoint, head-based ratio sampling is sufficient; tail sampling is delegated to the Collector if needed.

**Secrets handling**: API-key validation in `auth.KeySet.Validate` MUST NOT log or attribute the presented key. Specifically: the otelgin middleware is configured with `otelgin.WithFilter(...)` to suppress `Authorization` and `X-API-Key` headers from the auto-attribute set, and the auth middleware's slog calls do not include the key value. This rule is enforced by an integration test that fails if any exported span attribute or log record contains the literal sentinel test key.

**Shutdown semantics**: the existing graceful shutdown sequence is:

```
1. SIGTERM received
2. http.Server.Shutdown(ctx with grace deadline)  — drains in-flight requests
3. tracerProvider.Shutdown(same ctx)
4. loggerProvider.Shutdown(same ctx)
5. exit
```

If `tracerProvider.Shutdown` or `loggerProvider.Shutdown` returns context-deadline-exceeded, the local stderr handler logs `otlp shutdown timed out` and the process exits with a non-zero status. The exporter SHALL NOT extend the grace period — operators rely on K8s `terminationGracePeriodSeconds` matching `--shutdown-grace-period`.

- Why no bespoke `--otlp-*` flags: every operator who already runs an OTel-instrumented service is configured by the standard env vars; introducing a parallel CLI surface forks the operator workflow for no benefit.
- Why no in-process result cache implications: D6 still holds — the build always runs. Tracing only adds visibility; it does not change ETag determinism (resource attributes are not in the response body).
- Why log to *both* stderr and OTLP: the binary must remain useful in `kubectl logs` even when the OTel collector is down. Fan-out keeps the stderr stream intact.
- Why bound shutdown by the existing grace period rather than a separate `--otlp-shutdown-timeout`: K8s lifecycle is governed by a single `terminationGracePeriodSeconds`; introducing a second knob forces operators to keep two values in sync.
- Alternatives considered:
  - **Replace `log/slog` with a dedicated OTel logger** (rejected — `slog` is stdlib, the project already standardised on it in D10, and the bridge is one line of init).
  - **Add an exporter selection flag** (rejected — `OTEL_EXPORTER_OTLP_PROTOCOL` already covers gRPC vs HTTP/protobuf).
  - **Trace `/livez` / `/readyz` and rely on Collector filtering** (rejected — cheap to skip at the source; saves Collector ingestion budget; matches industry guidance).
  - **Emit a typed numeric span attribute for ETag instead of a string** (rejected — semconv prefers strings for opaque identifiers; ETag is sha256-hex, not numeric).
  - **Run a parallel async pipeline that captures the build trace into a debug endpoint** (rejected — duplicates OTel functionality; ops teams already have a Collector + Tempo / Jaeger).

## Risks / Trade-offs

- [Per-request build cost] → Every `/v1/graph` request runs a fresh upstream PromQL fan-out (target ≤ 3 s for ≤ 5 k pods aggregated across clusters in scope). With no in-process result cache, upstream load scales linearly with HTTP traffic; `--build-timeout` bounds tail latency (`504 timeout`); concurrency control is delegated to HPA + Pod resource limits (no in-process semaphore). A future cache mechanism is expected to absorb this cost; until then, ETag-based revalidation is the only amortisation lever.
- [Pod UID churn on restart pollutes long lookback windows] → For windows where `last_over_time(kube_pod_info)` returns multiple UIDs for the same `(cluster, namespace, name)` tuple within the window, keep ONLY the latest UID and discard the prior. There is no reliable way to link a deleted pod's UID to its replacement once kubelet stops reporting the deleted UID (the `kube-state-metrics` series simply stops; the controller assigns a fresh UUID for the new pod with no back-reference). The earlier idea of emitting a `pod-replaced-by` synthetic edge was rejected for this reason — it would have implied an identity mapping that the source data does not support. Document in the spec.
- [Service-graph metrics absent or sparse] → Topology-only graph is still valid; missing service-graph series produce zero `pod-calls-pod` edges instead of a build failure.
- [PromQL fan-out large with many clusters] → Per-query cost (duration, samples, points) is bounded by upstream VictoriaMetrics search limits; KSG surfaces VM 5xx as `502 Bad Gateway` with `reason: "upstream"`.
- [Inconsistent `cluster` external label across scrape pipelines] → Series missing the `cluster` label are bucketed under `cluster="unknown"` and surfaced via `kube_state_graph_clusters_observed`; document that operators must set the label uniformly.
- [Cross-cluster edge with one endpoint missing topology data] → If the producer emits a `traces_service_graph_request_total` series whose `client_k8s_pod_uid` or `server_k8s_pod_uid` does not appear in any cluster's `kube_pod_info` for the window, the missing endpoint is rendered as a synthetic ghost pod node (`attrs.ghost=true`) carrying only its `cluster` and `pod_uid`, instead of dropping the edge.
- [`kube-state-metrics` retention in VictoriaMetrics shorter than requested window] → `last_over_time` returns empty; respond `400 Bad Request` with `reason: "outside retention"` when zero topology rows are returned for a window covered by upstream `up{}` data.
- [Fake fixtures producer in the harness diverges from real producers] → Pin the metric names, label set, and cluster-label discipline the harness uses to D8, so swapping in real producers is a configuration change rather than a code change.
- [No auth on the API] → Document that the service is intended to sit behind a reverse proxy.
- [No result cache → upstream load scales with traffic] → Accepted for v1 in pre-distributed-deployment dev. Future cache mechanism (Redis L2, materialiser tier, or graph DB) is the planned mitigation; the design space is intentionally left open so the chosen shape matches the eventual deployment topology.
- [Multi-cluster cardinality on self-metrics] → `cluster` label appears only on observational gauges (`graph_node_count`, `graph_edge_count`); document expected `cluster` cardinality range (≤ 20 in v1) and recommend dropping the label at the scrape layer if it grows beyond budget.
- [OTLP collector outage stalls slog bridge or trace export] → Both exporters use bounded `BatchSpanProcessor` / `BatchLogProcessor` queues; on persistent collector failure, the SDK drops the oldest batches and surfaces the failure via the SDK's internal error handler (logged through stderr, not through the bridge to avoid feedback loops). Local stderr logs remain unaffected.
- [Trace span explosion on debug endpoints] → `/debug/*` routes are traced; document that operators should avoid scripting curl loops over them in production. Mitigation is at the Collector via tail sampling.
- [`db.statement` attribute leaks tenant info via PromQL label matchers] → Document; operators with stricter policy disable tracing or strip the attribute at the Collector.

## Migration Plan

Greenfield repository — no migration. Rollback is `git revert` of the merge commit. The JSON contract is versioned via a top-level `apiVersion: "v1"` field so consumers can detect breaking changes.

## Open Questions

- Final list of edge types beyond the three in D4 (e.g., `pod-shares-node`, `pod-shares-namespace`) — resolve during spec drafting; whichever ship in v1 must appear in both `Build()` and the static `/v1/edge-types` registry. v1 ships exactly the three: `pod-runs-on-node`, `pod-mounts-pvc`, `pod-calls-pod`.
- Alignment-grid policy across DST or leap seconds — likely "always UTC, no DST adjustment", confirm during spec.
- Shape of the future cache mechanism for distributed deployment (Redis L2 vs background materialiser vs graph DB). Tracked as a separate change once the deployment topology firms up.
- Whether `/v1/edge-types` should ever support time-window filtering — defer to v1.1.
- Whether `/v1/clusters` should also report per-cluster pod / node counts in its response, or keep it minimal (names + first-seen / last-seen) — defer to spec.
- ~~Fake-fixtures program shape: continuous Deployment with steady-state metrics vs YAML-driven snapshot replayer~~ — resolved: no fixtures program. Local rig uses real `kube-state-metrics`; integration tests (`internal/integration/`) ingest series directly via `POST /api/v1/import/prometheus` to a `testcontainers-go` VictoriaMetrics container.
- Exact Grafana Node Graph dashboard JSON to ship in `deploy/grafana/` for visual verification, including a layout that highlights cross-cluster edges — defer to harness spec.
- Whether `?format=` query parameter on `/v1/graph` is preferable to a separate `/v1/graph/nodegraph` route — defer to spec; current preference is the separate route.
- Whether `KSG_EXTERNAL_NAME_PATTERN` should evolve to a regex (`KSG_EXTERNAL_NAME_REGEX`) or accept multiple comma-separated patterns — defer to v1.x based on real deployment feedback.
- Whether external nodes should expose any additional `labels` (e.g., scheme parsed out of URL-shaped values) — defer; v1 keeps `labels.pattern` only.
