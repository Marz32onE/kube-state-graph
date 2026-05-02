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
- Run **CI integration tests via testcontainers-go**: a real VictoriaMetrics container started from inside `go test`, with synthetic series injected directly via VM's `/api/v1/import/prometheus` endpoint, and the API server run in-process against the container's URL. No Kubernetes, no scrape pipeline — fast, deterministic, runs on every PR.
- Ship a separate **manual visual-verification rig** (Kind cluster + VictoriaMetrics + fake-fixtures producer + Grafana Pod with the checked-in Node Graph dashboard) so an operator can boot the rig locally and eyeball the graph in Grafana. This rig is **not exercised by CI** — it exists for human verification only.
- Enforce a curated **static-analysis suite** in CI covering complexity, security, error handling, performance, and modern Go idioms, plus **`govulncheck`** for dependency-vulnerability scanning on every PR.
- Auto-generate an **OpenAPI 3.0 specification** from annotated handlers via `swaggo/swag` v2, served at `/openapi.yaml` and `/openapi.json`. Serve the **Scalar API Reference** UI at `/docs` with all assets **vendored** in the binary so the documentation works in air-gapped or otherwise isolated networks (no CDN dependency at view time). A drift gate in CI fails any PR where the generated spec is out of sync with the annotations.
- Adopt **`stretchr/testify`** as the single test-assertion library across the whole repository, with `testify/suite` driving the testcontainers-based integration tests' shared container lifecycle and `testifylint` policing usage in the static-analysis suite.

## Capabilities

### New Capabilities

- `graph-api`: HTTP API surface (Gin) that returns the combined cross-cluster pod / node / PVC graph as Cytoscape.js JSON, with a Grafana Node Graph compatibility route, time-range parameters, filtering, partial-graph traversal, edge-type discovery, and cluster discovery.
- `cluster-topology-source`: Reader that issues PromQL queries against centralised VictoriaMetrics for `kube_pod_info`, `kube_node_info`, `kube_node_status_addresses`, `kube_pod_spec_volumes_persistentvolumeclaims_info`, `kube_node_labels`, etc., honouring the per-source `cluster` external label and assembling per-cluster pod / node / PVC entities keyed by `(cluster, pod-uid)` and `(cluster, node-name)`.
- `pod-service-graph`: Pod-UID-scoped service graph reader: PromQL queries against centralised VictoriaMetrics for `traces_service_graph_*` series labelled with `client_cluster`, `server_cluster`, `client_k8s_pod_uid`, `server_k8s_pod_uid`, joined with topology to produce typed edges between pod and node graph nodes — including cross-cluster edges where `client_cluster != server_cluster`.
- `verification-harness`: Single-Kind-cluster **manual-only** rig with VictoriaMetrics installed in-cluster, a fake-fixtures producer that emits multi-cluster `kube_*` and `traces_service_graph_*` series, the `kube-state-graph` API server, and a Grafana Pod pre-provisioned with a Node Graph dashboard. The rig is run on demand by a human for visual verification of the rendered graph; it is not part of the CI test path.
- `container-integration`: CI-driven integration tests using testcontainers-go to start a single VictoriaMetrics container per test package, inject synthetic series via VM's HTTP import API, run the API server in-process, and assert the wire shape and behaviour of every `/v1/*` endpoint against a real PromQL backend.
- `static-analysis-suite`: Repository-level static-analysis policy: a curated `golangci-lint` configuration covering complexity, security, error-handling, performance, modern-Go-idiom checks, and `testifylint`; a `swag init` drift gate; plus `govulncheck` runs on every PR to detect dependency vulnerabilities. All run in CI and gate merges.
- `api-docs`: Auto-generated OpenAPI 3.0 specification (via `swaggo/swag` v2) served at `/openapi.yaml` and `/openapi.json`, plus a Scalar API Reference UI served at `/docs` with all JS / CSS assets vendored into the Go binary so documentation works offline / air-gapped. A drift contract test asserts every Gin route is documented and every documented path exists.

### Modified Capabilities

(None — no existing specs in this repository.)

## Impact

- New Go module with Gin, Prometheus Go client, `github.com/dgraph-io/ristretto/v2`, `golang.org/x/sync/{singleflight,errgroup,semaphore}`, and `log/slog` as primary dependencies. VictoriaMetrics is consumed over HTTP via the Prometheus query API and is not vendored. No `client-go`, no informers, no Kubernetes API access.
- New HTTP API surface (`/v1/graph`, `/v1/graph/nodegraph`, `/v1/clusters`, `/v1/edge-types`, `/v1/livez`, `/v1/readyz`, `/metrics`, optional `/admin/cache`, `/debug/last-queries`) that downstream UIs and scripts will depend on.
- New verification artefacts under the repo:
  - `internal/integration/` — CI-driven testcontainers tests.
  - Manual rig assets (Kind config, VictoriaMetrics manifests, fake-fixtures producer, Grafana Pod manifest + dashboard, local smoke script) — run on demand, not by CI.
- New static-analysis artefacts (`.golangci.yml` enabling the curated linter set, CI workflow steps for `golangci-lint` and `govulncheck`).
- Each upstream cluster operator becomes responsible for ensuring its scrape pipeline applies the `cluster` external label uniformly across `kube-state-metrics` and service-graph metrics. This is documented but not enforced in code.
- No existing code paths or specs are modified.
