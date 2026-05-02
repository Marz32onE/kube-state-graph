## Why

Operators running fleets of Kubernetes clusters need a single JSON view of pod-to-pod, pod-to-node, and **cross-cluster** pod-to-pod relationships. Existing tools either stop at the service abstraction, are scoped to one cluster, or require ad-hoc PromQL against `kube-state-metrics` and a service graph metrics endpoint, which is too slow for an interactive UI and does not naturally render cross-cluster edges.

Modern service meshes and OTLP collectors already emit pod-UID-resolved trace metrics that include both ends of every call (`client_cluster`, `client_k8s_pod_uid`, `server_cluster`, `server_k8s_pod_uid`, …). When all clusters' metrics flow into a centralised VictoriaMetrics, this is enough information to render a unified, cross-cluster pod-level service graph — provided someone joins it with topology data and serves it efficiently.

## What Changes

- Introduce a Go (Gin) REST API server, `kube-state-graph`, that returns a unified nodes-and-edges JSON document covering pod, node, and PVC topology for **one or more Kubernetes clusters** and the pod-UID-resolved RPC edges that connect them — including edges that **cross cluster boundaries**.
- Read all inputs from a single centralised VictoriaMetrics endpoint via the Prometheus HTTP API. Each upstream series is expected to carry a `cluster` external label identifying its source cluster (for `kube-state-metrics` series) or `client_cluster` / `server_cluster` (for service-graph series).
- Build graphs **on demand** for caller-specified `[start, end]` time ranges, using PromQL `@` timestamp modifiers and range-aware functions (`last_over_time`, `rate`).
- Optimise for the multi-user dashboard pattern with a tiered cache stack: HTTP `ETag` / `Cache-Control`, request coalescing via `singleflight`, and an in-process Ristretto cache keyed by time bucket only (filter and cluster-selection are applied at response time over the cached graph, so concurrent users with different filters share the same cache entry).
- Expose the graph in **Cytoscape.js** JSON shape on `GET /v1/graph` and a **Grafana Node Graph** datasource shape on `GET /v1/graph/nodegraph` for free visual verification through a Grafana dashboard.
- Provide cluster discovery (`GET /v1/clusters`) and a static edge-type catalogue (`GET /v1/edge-types`) so callers can populate filter dropdowns without reading documentation.
- Use Go's standard library `log/slog` for structured logging across the server.
- Ship a Kind-based integration-test harness with a single Kind cluster, VictoriaMetrics installed inside it, and a **fake fixtures producer** that injects synthetic multi-cluster `kube-state-metrics`-shaped and service-graph-shaped series straight into VictoriaMetrics. Real `kube-state-metrics` and a real OTLP / Alloy collector are explicitly out of scope for the harness; the fake fixtures keep the test focused on the API server and deterministic across runs.

## Capabilities

### New Capabilities

- `graph-api`: HTTP API surface (Gin) that returns the combined cross-cluster pod / node / PVC graph as Cytoscape.js JSON, with a Grafana Node Graph compatibility route, time-range parameters, filtering, partial-graph traversal, edge-type discovery, and cluster discovery.
- `cluster-topology-source`: Reader that issues PromQL queries against centralised VictoriaMetrics for `kube_pod_info`, `kube_node_info`, `kube_node_status_addresses`, `kube_pod_spec_volumes_persistentvolumeclaims_info`, `kube_node_labels`, etc., honouring the per-source `cluster` external label and assembling per-cluster pod / node / PVC entities keyed by `(cluster, pod-uid)` and `(cluster, node-name)`.
- `pod-service-graph`: Pod-UID-scoped service graph reader: PromQL queries against centralised VictoriaMetrics for `traces_service_graph_*` series labelled with `client_cluster`, `server_cluster`, `client_k8s_pod_uid`, `server_k8s_pod_uid`, joined with topology to produce typed edges between pod and node graph nodes — including cross-cluster edges where `client_cluster != server_cluster`.
- `verification-harness`: Single-Kind-cluster harness with VictoriaMetrics installed in-cluster and a fake fixtures producer that emits multi-cluster `kube_*` and `traces_service_graph_*` series directly to VictoriaMetrics. Smoke script asserts the API server's responses end to end across single-cluster and cross-cluster scenarios.

### Modified Capabilities

(None — no existing specs in this repository.)

## Impact

- New Go module with Gin, Prometheus Go client, `github.com/dgraph-io/ristretto/v2`, `golang.org/x/sync/{singleflight,errgroup,semaphore}`, and `log/slog` as primary dependencies. VictoriaMetrics is consumed over HTTP via the Prometheus query API and is not vendored. No `client-go`, no informers, no Kubernetes API access.
- New HTTP API surface (`/v1/graph`, `/v1/graph/nodegraph`, `/v1/clusters`, `/v1/edge-types`, `/v1/livez`, `/v1/readyz`, `/metrics`, optional `/admin/cache`, `/debug/last-queries`) that downstream UIs and scripts will depend on.
- New verification artefacts under the repo (Kind config, in-cluster VictoriaMetrics manifest, fake fixtures producer, smoke scripts, optional Grafana dashboard) used by CI / local verification.
- Each upstream cluster operator becomes responsible for ensuring its scrape pipeline applies the `cluster` external label uniformly across `kube-state-metrics` and service-graph metrics. This is documented but not enforced in code.
- No existing code paths or specs are modified.
