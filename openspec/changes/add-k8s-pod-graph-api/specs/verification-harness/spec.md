## ADDED Requirements

### Requirement: Manual-only visual verification rig

The verification harness SHALL be operated manually by a human; it SHALL NOT be exercised by CI. Its purpose is end-to-end visual verification of the multi-cluster graph in Grafana, not regression testing. CI integration coverage is provided by the `container-integration` capability instead.

#### Scenario: No CI workflow runs the harness

- **WHEN** an operator inspects the repository's CI workflow files
- **THEN** no CI job invokes the harness bootstrap script or its smoke script

#### Scenario: README documents manual operation

- **WHEN** a developer reads the harness `README.md` (or top-level documentation)
- **THEN** the documented entry points are `make local-up`, `make local-smoke`, and `make local-down` (or equivalent), each clearly labelled as manual

### Requirement: Single Kind cluster bootstrap

The harness SHALL provision exactly one local Kubernetes cluster using Kind. The cluster SHALL be created from a checked-in Kind configuration file (e.g., `deploy/kind/kind-config.yaml` or `local/grafana/kind-config.yaml`). The harness SHALL NOT spin up multiple Kind clusters.

#### Scenario: Kind config file present

- **WHEN** an operator inspects the harness directory
- **THEN** a Kind configuration file exists declaring exactly one cluster

#### Scenario: bootstrap creates one cluster

- **WHEN** an operator runs the harness bootstrap script
- **THEN** the script creates exactly one Kind cluster and the resulting `kind get clusters` output contains exactly one name

### Requirement: In-cluster VictoriaMetrics installation

The bootstrap script SHALL install a VictoriaMetrics single-node deployment inside the Kind cluster, exposed at a stable in-cluster Service name (e.g., `victoria-metrics:8428`). The installation SHALL NOT use multi-tenant vmcluster mode.

#### Scenario: VictoriaMetrics reachable in-cluster

- **WHEN** the bootstrap script has finished and a Pod inside the cluster issues `GET http://victoria-metrics:8428/health`
- **THEN** the response is 200

#### Scenario: vmsingle, not vmcluster

- **WHEN** an operator inspects the manifests applied by bootstrap
- **THEN** the manifests deploy a vmsingle-class workload and do NOT include any of `vmstorage`, `vmselect`, or `vminsert`

### Requirement: Topology source = real kube-state-metrics

The harness SHALL install a real `kube-state-metrics` Deployment inside the Kind cluster and configure VictoriaMetrics to scrape it. The harness SHALL NOT include a synthetic fixtures program; the local rig's purpose is end-to-end visual verification against real Kubernetes topology series, not fabricated ones. A relabel rule on the scrape config SHALL inject a `cluster=kind-local` external label so the API server treats the rig as a single-cluster source. The harness `kube-state-metrics` MUST also export services and endpointslices (KSM `--resources` includes `services,endpointslices`; RBAC grants `list`/`watch` for both) so connection-string service/headless resolution can be exercised.

The harness SHALL also produce real `traces_service_graph_request_total` series for in-cluster traffic via a Grafana Beyla DaemonSet (eBPF auto-instrumentation) shipping OTLP spans to a Grafana Alloy Deployment whose `otelcol.connector.servicegraph` (configured with `dimensions=["k8s.pod.uid"]`) emits the metric with `client_k8s_pod_uid` and `server_k8s_pod_uid`, then remote-writes to VictoriaMetrics. The harness SHALL NOT ship a synthetic traffic generator; the existing in-cluster Go traffic (`kube-state-graph` scraping VictoriaMetrics, VictoriaMetrics scraping `kube-state-metrics`, Grafana querying `kube-state-graph`, etc.) is sufficient to populate paired client+server spans. Cross-cluster scenarios remain out of scope for the local rig and are exercised by `internal/integration/` tests against a `testcontainers-go` VictoriaMetrics container (see the `container-integration` capability), which directly ingest hand-crafted multi-cluster fixtures via `POST /api/v1/import/prometheus`.

#### Scenario: kube-state-metrics scraped by VictoriaMetrics

- **WHEN** the bootstrap has completed and a Pod inside the cluster issues `GET http://victoria-metrics:8428/api/v1/query?query=kube_pod_info`
- **THEN** the response contains at least one series whose `cluster` label is `kind-local`

#### Scenario: No synthetic fixtures program

- **WHEN** an operator inspects the harness manifests and source tree
- **THEN** there is no `cmd/vm-fixtures` binary, no `tests/harness/vm-fixtures/` directory, and no manifest exposing a `/metrics` fixtures endpoint to VictoriaMetrics

### Requirement: API server installed in the harness

The harness SHALL install the `kube-state-graph` API server inside the Kind cluster, configured to point at the in-cluster VictoriaMetrics endpoint, and exposed via a NodePort or `kubectl port-forward` so the operator can reach it from the host.

#### Scenario: API server reachable

- **WHEN** the bootstrap has completed and the operator port-forwards (or hits the NodePort for) the API service
- **THEN** `GET /v1/livez` against the forwarded port returns 200

#### Scenario: No others name pattern env

- **WHEN** an operator inspects the API server Deployment in the harness
- **THEN** the env section does NOT contain `KSG_OTHERS_NAME_PATTERN` (connection-string `"://"` detection is built-in)

### Requirement: Grafana Pod with provisioned dashboard

The harness SHALL install a Grafana Pod inside the Kind cluster, expose it via NodePort or port-forward on the host (default `:3000`), and pre-provision both:

- A datasource pointing at the in-cluster `kube-state-graph` Service (suitable for the JSON / Infinity datasource), and
- The Node Graph dashboard checked in at `deploy/grafana/kube-state-graph-nodegraph.json`.

The operator SHALL be able to load Grafana, sign in with the bootstrap credentials, and see the rendered multi-cluster graph immediately, without manually configuring datasources or importing dashboards.

#### Scenario: Grafana reachable

- **WHEN** the bootstrap has completed and the operator opens the Grafana URL
- **THEN** the Grafana login page is served and bootstrap credentials are documented in the harness README

#### Scenario: Dashboard pre-provisioned

- **WHEN** the operator opens Grafana after bootstrap
- **THEN** the kube-state-graph Node Graph dashboard appears under Dashboards without manual import and renders nodes / edges from the running API server

#### Scenario: Datasource pre-provisioned

- **WHEN** the operator opens Grafana → Connections → Data sources
- **THEN** a datasource is already configured pointing at `http://kube-state-graph.kube-state-graph.svc:8080`

### Requirement: Manual smoke script

The harness SHALL ship a smoke script (e.g., `local/kind/smoke.sh`) that an operator MAY run after bootstrap to sanity-check the API surface end-to-end without opening Grafana. The script SHALL exit non-zero on any failure. CI SHALL NOT run this script.

The local rig is single-cluster (Kind injects `cluster=kind-local` via the VictoriaMetrics scrape config) and now produces in-cluster `traces_service_graph_request_total` via the Beyla→Alloy pipeline, so the script SHALL also assert the `pod-calls-pod` edge path. Multi-cluster behaviour, cross-cluster edges, and external-name substitution remain out of scope for the local rig and are exercised by `internal/integration/` tests against `testcontainers-go` VictoriaMetrics — not by this script.

The script SHALL verify, at minimum:

- `GET /v1/livez` returns 200.
- `GET /v1/readyz` returns 200 within a configurable readiness budget (default 60 s).
- `GET /v1/clusters` returns the rig's single cluster name (`kind-local`).
- `GET /v1/edge-types` returns a body listing `pod-runs-on-node`, `pod-mounts-pvc`, and `pod-calls-pod`.
- `GET /v1/graph?start=<now-5m>&end=<now>` returns 200 with a non-empty `elements.nodes` and at least one `pod-runs-on-node` edge.
- `GET /v1/graph?start=<now-5m>&end=<now>&cluster=kind-local` returns nodes whose `data.labels.cluster` is `kind-local`.
- VictoriaMetrics exposes at least one `traces_service_graph_request_total{cluster="kind-local",client_k8s_pod_uid!="",server_k8s_pod_uid!=""}` sample within a configurable budget (default 180 s) after bootstrap.
- `GET /v1/graph?cluster=kind-local&edge_type=pod-calls-pod&start=<now-5m>&end=<now>` returns at least one `pod-calls-pod` edge.
- Every node in any `/v1/graph` response carries `data.id`, `data.name`, `data.type`, and `data.labels` (a JSON object whose values are all strings). Every edge carries `data.id` (RFC 4122 UUID), `data.type`, `data.source`, `data.target`, and `data.labels` (a JSON object whose values are all strings).
- `GET /metrics` returns 200 in Prometheus exposition format and contains at least one `kube_state_graph_*` series.

#### Scenario: Smoke script success path

- **WHEN** the harness is bootstrapped and the operator runs the smoke script
- **THEN** every assertion above passes and the script exits 0

#### Scenario: Canonical schema enforced

- **WHEN** the operator runs the smoke script and it inspects any `/v1/graph` response
- **THEN** every node has `data.id`, `data.name`, `data.type`, `data.labels`, and every edge has `data.id` (UUID), `data.type`, `data.source`, `data.target`, `data.labels`; every value inside any `data.labels` map is a JSON string

### Requirement: Reproducible teardown

The harness SHALL provide a teardown command that deletes the Kind cluster and removes any host-side artefacts created during bootstrap. Re-running bootstrap after teardown SHALL produce a cluster equivalent to the previous bootstrap.

#### Scenario: Teardown deletes Kind cluster

- **WHEN** an operator runs the teardown script
- **THEN** `kind get clusters` no longer lists the harness cluster and no Docker containers from the harness remain
