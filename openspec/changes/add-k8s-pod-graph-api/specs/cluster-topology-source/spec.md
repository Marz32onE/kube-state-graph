## ADDED Requirements

### Requirement: Centralised VictoriaMetrics as the only topology source

The topology reader SHALL fetch all pod, node, and PVC topology by issuing PromQL queries against a single configurable Prometheus-compatible endpoint (`--prom-url`), pointing at centralised VictoriaMetrics. The reader SHALL NOT call the Kubernetes API server, SHALL NOT scrape `kube-state-metrics` directly, and SHALL NOT use Kubernetes informers.

#### Scenario: Single configured upstream

- **WHEN** the server starts with `--prom-url=http://vm.example:8428`
- **THEN** every topology query is sent to `http://vm.example:8428` and no other HTTP destinations

#### Scenario: No Kubernetes API access

- **WHEN** the server runs in any environment
- **THEN** the binary makes no requests to any `/api/*` Kubernetes API path and requires no Kubernetes ServiceAccount or kubeconfig

### Requirement: Topology series consumed

The topology reader SHALL consume at minimum the following `kube-state-metrics` series, each carrying a `cluster` external label:

- `kube_pod_info{cluster, namespace, pod, uid, node, pod_ip, host_ip, ...}` (`pod_ip` and `host_ip` are surfaced when present)
- `kube_node_info{cluster, node, ...}`
- `kube_node_status_addresses{cluster, node, type="ExternalIP", address, ...}`
- `kube_pod_spec_volumes_persistentvolumeclaims_info{cluster, namespace, pod, volume, claim_name, ...}`
- `kube_node_labels{cluster, node, label_*, ...}`

#### Scenario: All families queried

- **WHEN** a graph build runs against an upstream containing all five families above
- **THEN** the reader emits exactly one PromQL query per family for the build, each evaluated at the bucketed `end` over the bucketed window

#### Scenario: Missing optional family

- **WHEN** the upstream contains `kube_pod_info` and `kube_node_info` but no `kube_node_labels` series for the window
- **THEN** the reader produces a valid topology with empty `labels` maps on node entities and does not fail the build

### Requirement: Configurable upstream metric-name prefix

The topology reader SHALL prepend a single configurable prefix to every `kube_*` series name it queries, so deployments using a fork of kube-state-metrics or a custom exporter that re-publishes the same series under an organisational prefix (e.g. `o11y_kube_pod_info`) can be supported without forking the API server. The prefix SHALL be sourced from the `KSG_METRIC_PREFIX` environment variable or the `--metric-prefix` flag (flag wins over env when both are set). The default value SHALL be the empty string, preserving stock kube-state-metrics behaviour. The prefix SHALL be additive — appended verbatim before the existing series name; the existing `kube_*` suffix and the upstream label-name contract (`cluster`, `namespace`, `pod`, `uid`, `node`, `persistentvolumeclaim`, `label_*`, etc.) are unchanged. The prefix SHALL be validated against the Prometheus metric-name charset `^[a-zA-Z_:][a-zA-Z0-9_:]*$` when non-empty; an invalid value SHALL fail server startup. The trailing underscore (if any) is the operator's responsibility — the server does not inject one.

The same prefix SHALL apply to every kube-state-metrics-shaped series the reader consumes: `kube_pod_info`, `kube_node_info`, `kube_node_status_addresses`, `kube_pod_spec_volumes_persistentvolumeclaims_info`, `kube_node_labels`, and the `kube_node_info`-backed cluster discovery query. The prefix SHALL NOT be applied to `traces_service_graph_request_total` (which is produced by a different exporter family) nor to the Prometheus-native `up{}` readiness probe.

#### Scenario: Default empty prefix preserves stock series names

- **WHEN** the server starts without `KSG_METRIC_PREFIX` or `--metric-prefix`
- **THEN** every topology query string contains the bare `kube_*` series name (e.g. `last_over_time(kube_pod_info[<window>])`) and no prefix is added

#### Scenario: Custom prefix from environment

- **WHEN** the server starts with `KSG_METRIC_PREFIX=o11y_`
- **THEN** the issued topology PromQL contains `last_over_time(o11y_kube_pod_info[<window>])`, `last_over_time(o11y_kube_node_info[<window>])`, `last_over_time(o11y_kube_node_status_addresses{type="ExternalIP"}[<window>])`, `last_over_time(o11y_kube_pod_spec_volumes_persistentvolumeclaims_info[<window>])`, and `last_over_time(o11y_kube_node_labels[<window>])`, AND the cluster-discovery query becomes `group by (cluster) (last_over_time(o11y_kube_node_info[<lookback>]))`

#### Scenario: Prefix does not affect service-graph or probe queries

- **WHEN** the server starts with `KSG_METRIC_PREFIX=o11y_`
- **THEN** the service-graph reader still queries `rate(traces_service_graph_request_total[<window>])` (no prefix) and the `/readyz` probe still issues `up` (no prefix)

#### Scenario: Flag overrides environment variable

- **WHEN** the server starts with `KSG_METRIC_PREFIX=acme_` in the environment and `--metric-prefix=beta_` on the command line
- **THEN** the resulting topology queries reference `beta_kube_pod_info` and not `acme_kube_pod_info`

#### Scenario: Invalid prefix charset rejected at startup

- **WHEN** the server starts with `KSG_METRIC_PREFIX="o11y-bad!"`
- **THEN** `config.Validate` returns an error containing `metric-prefix` and the process exits non-zero before binding the listener

### Requirement: Time-window evaluation

Each topology query SHALL be evaluated at the caller-supplied `end` timestamp using `last_over_time(<series>[<window>]) @ <end>` so the result reflects the most recent value of each series within the requested window. The reader SHALL NOT fall back to instant evaluation at `now`.

#### Scenario: last_over_time used for kube_pod_info

- **WHEN** the reader runs a query for `kube_pod_info`
- **THEN** the issued PromQL string contains `last_over_time(kube_pod_info[<window>]) @ <end>` where `<window>` equals `end - start` and `<end>` equals the caller-supplied `end`

#### Scenario: Windowed result mid-restart

- **WHEN** a pod was running at `start` and replaced before `end`
- **THEN** the reader emits both pod-info entries for the window (the prior and the current UID); see "Pod restart handling" requirement

### Requirement: Cluster-scoped IDs

The reader SHALL produce topology entities whose stable identifiers are cluster-scoped:

- Pod ID = `<cluster>/<pod-uid>` (composite of `cluster` and `uid` labels).
- K8s node ID = `<cluster>/<node>` (composite of `cluster` and `node` labels).
- PVC ID = `<cluster>/<namespace>/<claim_name>`.

#### Scenario: Two clusters with same node name

- **WHEN** `kube_node_info{cluster="cluster-alpha", node="worker-0"}` and `kube_node_info{cluster="cluster-beta", node="worker-0"}` both exist in the window
- **THEN** the reader emits two distinct K8s node entities with IDs `cluster-alpha/worker-0` and `cluster-beta/worker-0`

#### Scenario: Pod ID derives from uid label

- **WHEN** `kube_pod_info{cluster="cluster-alpha", uid="abc-123", ...}` is present
- **THEN** the reader emits a pod entity with ID `cluster-alpha/abc-123`

### Requirement: Canonical entity fields

Every emitted topology entity SHALL carry the four canonical fields consumed by the graph API: `id`, `name`, `type`, `labels`. The reader SHALL set these as follows:

- For pods: `name` = the `pod` label of `kube_pod_info`; `type` = `"pod"`; `labels` includes `cluster`, `namespace`, `node` (cluster-scoped node ID), `pod_ip` and `host_ip` when the upstream `kube_pod_info` series carries them, and any K8s pod labels available from `kube_pod_labels` for that pod (added under their original keys). When kube-state-metrics emits multiple `kube_pod_info` series for the same pod-UID with evolving label sets (e.g. earlier scrapes that lack `node`, `pod_ip`, or `host_ip`), the reader SHALL merge labels across same-UID samples so the emitted entity reflects the most informative observation.
- For K8s nodes: `name` = the `node` label of `kube_node_info`; `type` = `"node"`; `labels` includes `cluster`, `external_ip` when `kube_node_status_addresses{type="ExternalIP"}` provides one, and any node labels from `kube_node_labels` for that node (the `label_*=` series translates to entries under their original key with the `label_` prefix removed).
- For PVCs: `name` = the `claim_name` label of `kube_pod_spec_volumes_persistentvolumeclaims_info`; `type` = `"pvc"`; `labels` includes `cluster`, `namespace`, and `volume`.

#### Scenario: Pod entity canonical fields

- **WHEN** `kube_pod_info{cluster="cluster-alpha", namespace="shop", pod="checkout-1", uid="abc", node="worker-0"}` is present
- **THEN** the emitted pod entity has `id="cluster-alpha/abc"`, `name="checkout-1"`, `type="pod"`, `labels.cluster="cluster-alpha"`, `labels.namespace="shop"`, and `labels.node="cluster-alpha/worker-0"`

#### Scenario: Pod IP and host IP surfaced under labels

- **WHEN** `kube_pod_info{cluster="cluster-alpha", namespace="shop", pod="checkout-1", uid="abc", node="worker-0", pod_ip="10.244.0.42", host_ip="10.0.0.7"}` is present
- **THEN** the emitted pod entity has `labels.pod_ip="10.244.0.42"` and `labels.host_ip="10.0.0.7"`

#### Scenario: Pod IP labels merged across same-UID samples

- **WHEN** kube-state-metrics emits two `kube_pod_info` series with the same `uid` — one without `pod_ip`/`host_ip`/`node` (early scrape during scheduling) and a later one with all three populated
- **THEN** the emitted pod entity carries the populated `node`, `pod_ip`, and `host_ip` values regardless of the order returned by the upstream

#### Scenario: K8s node external_ip surfaced under labels

- **WHEN** `kube_node_status_addresses{cluster="cluster-alpha", node="worker-0", type="ExternalIP", address="203.0.113.10"}` is present
- **THEN** the emitted K8s node entity has `labels.external_ip="203.0.113.10"`

#### Scenario: K8s node labels flattened

- **WHEN** the upstream provides `kube_node_labels{cluster="cluster-alpha", node="worker-0", label_topology_kubernetes_io_zone="us-east-1a", label_kubernetes_io_arch="amd64"}`
- **THEN** the emitted node entity's `labels` map contains `topology.kubernetes.io/zone="us-east-1a"` and `kubernetes.io/arch="amd64"` under their original keys

### Requirement: Pod restart handling within window

When `last_over_time(kube_pod_info[...])` returns multiple `uid` values for the same `(cluster, namespace, pod)` tuple within the requested window (i.e. the pod was deleted and recreated mid-window), the reader SHALL retain ONLY the entity with the latest evaluation timestamp as the canonical pod and SHALL discard prior UIDs. There is no reliable identity link between the deleted pod and its replacement once kubelet stops reporting the deleted UID, so the API does not attempt to reconstruct one.

#### Scenario: Pod replaced mid-window collapses to latest UID

- **WHEN** the window includes a pod restart producing two distinct UIDs for the same `(cluster, namespace, pod)` tuple
- **THEN** the resulting topology contains exactly one pod entity, identified by the newest UID; the prior UID does not appear as a node and no synthetic edge is emitted

### Requirement: Cluster discovery query

The topology reader SHALL provide a discovery query, used by the cluster discovery endpoint, that returns the set of distinct `cluster` label values observed in `kube_node_info` over a configurable lookback (default 1 hour) via PromQL `group by (cluster) (last_over_time(kube_node_info[<lookback>]))`.

#### Scenario: Two clusters discovered

- **WHEN** centralised VictoriaMetrics holds `kube_node_info` series for `cluster=cluster-alpha` and `cluster=cluster-beta` within the discovery lookback
- **THEN** the discovery query returns exactly the set `{ "cluster-alpha", "cluster-beta" }`

### Requirement: Series missing the cluster label

A topology series that is missing the `cluster` label SHALL be bucketed under `cluster="unknown"`. The reader SHALL surface the count of such series via the `kube_state_graph_clusters_observed` gauge (the value `unknown` will appear in the gauge's label set when present).

#### Scenario: Legacy series without cluster label

- **WHEN** a `kube_pod_info` series has no `cluster` label
- **THEN** the resulting pod entity has `cluster: "unknown"` and contributes to the `unknown` value in the observed-clusters set

### Requirement: Per-call upstream timeout

Each topology query SHALL be issued with a per-call context timeout (default 10 seconds, configurable). On timeout or non-2xx response, the reader SHALL increment `kube_state_graph_upstream_query_failures_total{query=<name>}` and propagate the error so the build aborts.

#### Scenario: Single query times out

- **WHEN** centralised VictoriaMetrics fails to respond to the `kube_node_labels` query within the per-call timeout
- **THEN** the failure counter for `query="kube_node_labels"` increments by 1 and the build returns an error
