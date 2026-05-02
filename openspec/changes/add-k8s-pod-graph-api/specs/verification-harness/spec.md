## ADDED Requirements

### Requirement: Single Kind cluster bootstrap

The harness SHALL provision exactly one local Kubernetes cluster using Kind. The cluster SHALL be created from a checked-in `deploy/kind/kind-config.yaml` configuration. The harness SHALL NOT spin up multiple Kind clusters.

#### Scenario: kind-config.yaml present

- **WHEN** an operator inspects the repository at `deploy/kind/kind-config.yaml`
- **THEN** a Kind configuration file exists declaring a single cluster

#### Scenario: bootstrap creates one cluster

- **WHEN** an operator runs the harness bootstrap script
- **THEN** the script creates exactly one Kind cluster and the resulting `kind get clusters` output contains exactly one name

### Requirement: In-cluster VictoriaMetrics installation

The bootstrap script SHALL install a VictoriaMetrics single-node deployment inside the Kind cluster, exposed at a stable in-cluster Service name (e.g., `victoria-metrics:8428`). The installation SHALL NOT require multi-tenant vmcluster mode.

#### Scenario: VictoriaMetrics reachable in-cluster

- **WHEN** the bootstrap script has finished and a Pod inside the cluster issues `GET http://victoria-metrics:8428/health`
- **THEN** the response is 200

#### Scenario: vmsingle, not vmcluster

- **WHEN** an operator inspects the manifests applied by bootstrap
- **THEN** the manifests deploy a vmsingle-class workload and do NOT include any of `vmstorage`, `vmselect`, or `vminsert`

### Requirement: Fake fixtures producer

The harness SHALL include a Go program at `tests/harness/vm-fixtures/` that exposes a `/metrics` Prometheus exposition endpoint emitting hand-crafted multi-cluster fixtures. VictoriaMetrics SHALL scrape this endpoint as its only source of `kube_*` and `traces_service_graph_*` series. Real `kube-state-metrics`, real OTLP collectors, and real OTel SDKs SHALL NOT be installed in the harness.

#### Scenario: Fixtures program exposes /metrics

- **WHEN** the fixtures Pod is running and a client inside the cluster issues `GET http://vm-fixtures:8080/metrics`
- **THEN** the response is 200 in Prometheus exposition format

#### Scenario: No real kube-state-metrics

- **WHEN** an operator lists Pods in the harness namespace
- **THEN** no Pod's container image references `kube-state-metrics`

### Requirement: Multi-cluster fixture content

The fixtures producer SHALL emit, at minimum, the following synthetic series for at least two synthetic clusters (e.g., `cluster-alpha` and `cluster-beta`):

- `kube_pod_info{cluster, namespace, pod, uid, node}` — at least 2 pods per cluster.
- `kube_node_info{cluster, node}` — at least 1 node per cluster.
- `kube_node_status_addresses{cluster, node, type="ExternalIP", address}` — for every emitted node.
- `kube_pod_spec_volumes_persistentvolumeclaims_info{cluster, namespace, pod, volume, claim_name}` — at least 1 binding in one cluster.
- `kube_node_labels{cluster, node, label_*}` — at least 1 label per node.
- `traces_service_graph_request_total{client, server, client_cluster, server_cluster, client_k8s_pod_uid, server_k8s_pod_uid, client_k8s_namespace_name, server_k8s_namespace_name, connection_type}` — including (a) at least one series with `client_cluster != server_cluster` and (b) at least one series whose `client` value contains `://` (e.g., `client="http://api.example.com"`) so the external-name-pattern rule can be exercised.

#### Scenario: At least one cross-cluster edge series

- **WHEN** an operator scrapes the fixtures endpoint
- **THEN** at least one `traces_service_graph_request_total` series has different values for `client_cluster` and `server_cluster`

#### Scenario: Topology references the cross-cluster edge endpoints

- **WHEN** an operator inspects the fixture set
- **THEN** every `(client_cluster, client_k8s_pod_uid)` and `(server_cluster, server_k8s_pod_uid)` referenced by a `traces_service_graph_request_total` series also appears as a `kube_pod_info{cluster=..., uid=...}` series

### Requirement: Fixture configuration

The fixtures program SHALL load its series definitions from a YAML file checked into the repository (`tests/harness/vm-fixtures/fixtures.yaml`) so test scenarios are deterministic. The fixtures program SHALL re-read the file on a SIGHUP signal and SHALL emit a metric `vm_fixtures_reloaded_total` whose value increments on each successful reload.

#### Scenario: SIGHUP reload

- **WHEN** an operator updates the YAML and sends SIGHUP to the fixtures container
- **THEN** subsequent `/metrics` scrapes reflect the new fixtures and `vm_fixtures_reloaded_total` has incremented by 1

### Requirement: API server installed in the harness

The harness SHALL install the `kube-state-graph` API server inside the Kind cluster, configured to point at the in-cluster VictoriaMetrics endpoint, and exposed via a NodePort or `kubectl port-forward` so the smoke script can reach it from the host. The harness SHALL set `KSG_EXTERNAL_NAME_PATTERN="://"` on the API server Deployment so the smoke script can validate the external-name-pattern rule.

#### Scenario: API server reachable

- **WHEN** the bootstrap has completed and the smoke script port-forwards the API service
- **THEN** `GET /v1/livez` against the forwarded port returns 200

#### Scenario: External name pattern configured

- **WHEN** an operator inspects the API server Deployment in the harness
- **THEN** the env section contains `KSG_EXTERNAL_NAME_PATTERN` set to `"://"`

### Requirement: Smoke script assertions

A `tests/smoke/` script SHALL run after bootstrap and verify the following assertions against the running API server. Any failure SHALL exit non-zero.

- `GET /v1/livez` returns 200.
- `GET /v1/readyz` returns 200 within a configurable readiness budget (default 60 s).
- `GET /v1/clusters` returns at least the synthetic cluster names emitted by the fixtures (e.g., `cluster-alpha`, `cluster-beta`).
- `GET /v1/edge-types` returns a body listing `pod-runs-on-node`, `pod-mounts-pvc-on-node`, and `pod-calls-pod`.
- `GET /v1/graph?start=<now-5m>&end=<now>` returns 200 with a non-empty `elements.nodes` and at least one `pod-runs-on-node` edge per cluster.
- `GET /v1/graph?start=<now-5m>&end=<now>&edge_type=pod-calls-pod` returns at least one edge whose `data.labels.client_cluster` is not equal to `data.labels.server_cluster`.
- `GET /v1/graph?start=<now-5m>&end=<now>&cluster=cluster-alpha` returns nodes whose `data.labels.cluster` is `cluster-alpha`, plus any cross-cluster edge endpoints in `cluster-beta`.
- Every node in any `/v1/graph` response carries `data.id`, `data.name`, `data.type`, and `data.labels` (a JSON object whose values are all strings). Every edge carries `data.id` (RFC 4122 UUID), `data.type`, `data.source`, `data.target`, and `data.labels` (a JSON object whose values are all strings).
- `GET /v1/graph?start=<now-5m>&end=<now>&edge_type=pod-calls-pod` returns at least one node whose `data.type` is `"external"` and whose `data.name` contains the configured pattern substring (e.g., `://`).
- `GET /v1/metrics` returns 200 in Prometheus exposition format.

#### Scenario: Smoke script success path

- **WHEN** the harness is bootstrapped and the smoke script runs
- **THEN** every assertion above passes and the script exits 0

#### Scenario: Cross-cluster edge present

- **WHEN** the smoke script issues `GET /v1/graph?...&edge_type=pod-calls-pod`
- **THEN** the response contains at least one edge whose `data.labels.client_cluster` differs from `data.labels.server_cluster`

#### Scenario: Canonical schema enforced

- **WHEN** the smoke script inspects any `/v1/graph` response
- **THEN** every node has `data.id`, `data.name`, `data.type`, `data.labels`, and every edge has `data.id` (UUID), `data.type`, `data.source`, `data.target`, `data.labels`; every value inside any `data.labels` map is a JSON string

#### Scenario: External node produced by KSG_EXTERNAL_NAME_PATTERN

- **WHEN** the smoke script issues `GET /v1/graph?...&edge_type=pod-calls-pod` against the harness (which sets `KSG_EXTERNAL_NAME_PATTERN="://"`)
- **THEN** the response contains at least one node whose `data.type` is `"external"` and whose `data.name` contains `"://"`

### Requirement: Optional Grafana dashboard

The harness SHALL ship a Grafana dashboard JSON at `deploy/grafana/kube-state-graph-nodegraph.json` that uses the JSON / Infinity datasource against `GET /v1/graph/nodegraph` so an operator can visually verify the multi-cluster graph. Installing Grafana itself SHALL be optional; the smoke script SHALL NOT require Grafana to pass.

#### Scenario: Dashboard JSON checked in

- **WHEN** an operator inspects `deploy/grafana/`
- **THEN** a Grafana dashboard JSON file exists and references `/v1/graph/nodegraph` as a datasource URL template

### Requirement: Reproducible teardown

The harness SHALL provide a teardown command that deletes the Kind cluster and removes any host-side artefacts created during bootstrap. Re-running bootstrap after teardown SHALL produce a cluster equivalent to the previous bootstrap.

#### Scenario: Teardown deletes Kind cluster

- **WHEN** an operator runs the teardown script
- **THEN** `kind get clusters` no longer lists the harness cluster and no Docker containers from the harness remain

### Requirement: CI invocation

The repository SHALL include a CI job definition that runs the smoke script against the harness on PRs that modify any of `cmd/`, `internal/build/`, `internal/cache/`, `deploy/kind/`, or `tests/harness/`. The same job SHALL run nightly regardless of changed paths.

#### Scenario: CI workflow exists

- **WHEN** an operator inspects the repository's CI configuration
- **THEN** a workflow exists that invokes the harness bootstrap and the smoke script
