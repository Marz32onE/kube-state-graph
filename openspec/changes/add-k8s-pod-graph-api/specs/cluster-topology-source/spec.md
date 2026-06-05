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
- `kube_service_info{cluster, namespace, service, cluster_ip, ...}` (OPTIONAL — feeds the service/endpoint indexes)
- `kube_endpointslice_endpoints{cluster, namespace, endpointslice, address, targetref_kind, targetref_name, targetref_namespace, ...}` (OPTIONAL — feeds the service/endpoint indexes)
- `kube_endpointslice_labels{cluster, namespace, endpointslice, label_kubernetes_io_service_name, ...}` (OPTIONAL — joins each slice back to its owning service)
- `kube_pod_owner{cluster, namespace, pod, owner_kind, owner_name, owner_is_controller, ...}` (OPTIONAL — feeds the pod controller-owner labels)
- `kube_replicaset_owner{cluster, namespace, replicaset, owner_kind, owner_name, ...}` (OPTIONAL — resolves a ReplicaSet pod owner up to its owning Deployment)

The three service/endpointslice families are OPTIONAL: when absent (kube-state-metrics not exporting services or endpointslices), the reader SHALL still build a valid topology, the service/endpoint indexes are simply empty, and connection-string resolution in the pod-service-graph reader degrades gracefully — `"://"` service endpoints that cannot be resolved against an empty index become `external/<label>` nodes.

#### Scenario: All families queried

- **WHEN** a graph build runs against an upstream containing all families above
- **THEN** the reader emits exactly one PromQL query per family for the build, each evaluated at the bucketed `end` over the bucketed window

#### Scenario: Missing optional family

- **WHEN** the upstream contains `kube_pod_info` and `kube_node_info` but no `kube_node_labels` series for the window
- **THEN** the reader produces a valid topology with empty `labels` maps on node entities and does not fail the build

#### Scenario: Service and endpointslice metrics absent

- **WHEN** the upstream contains `kube_pod_info` and `kube_node_info` but no `kube_service_info`, `kube_endpointslice_endpoints`, or `kube_endpointslice_labels` series for the window
- **THEN** the reader produces a valid topology with empty service/endpoint indexes, the build does not fail, and any `"://"` connection-string endpoint that would otherwise resolve to an in-cluster service falls back to an `external/<label>` node with empty `labels`

### Requirement: Service and endpoint indexes

When the optional `kube_service_info`, `kube_endpointslice_endpoints`, and `kube_endpointslice_labels` families are present, the topology reader SHALL build two lookup INDEXES that the pod-service-graph reader consults to resolve `"://"` connection-string endpoints. The reader SHALL build INDEXES ONLY — it SHALL NOT emit `service` nodes or `service-selects-pod` edges into the graph wholesale. Those are materialised ON DEMAND by the pod-service-graph reader, for referenced services only, to avoid graph bloat.

The two indexes are:

- **ServicesByNameNS**: keyed by `(cluster, namespace, service)`, mapping to the service facts from `kube_service_info` — including `cluster_ip` (used to set the service node's `ipaddress`, omitted when `cluster_ip="None"` for headless services).
- **EndpointsByService**: keyed by `(cluster, namespace, service)`, mapping to the list of backing pods (the source of the Service → backing-pod fan-out). Each slice is joined back to its owning service via the `label_kubernetes_io_service_name` label on `kube_endpointslice_labels`, joined to `kube_endpointslice_endpoints` by `(cluster, namespace, endpointslice)`. Each endpoint is then resolved to a topology pod by joining `(namespace, targetref_name)` against `kube_pod_info` (matching the pod by name within the namespace to recover its UID). The per-endpoint `hostname` label is NOT consumed — there is no per-pod headless resolution.

#### Scenario: Service index resolves backing pods

- **WHEN** the upstream provides `kube_service_info{cluster="cluster-alpha", namespace="db", service="mongo", cluster_ip="10.96.0.5"}`, a `kube_endpointslice_labels{cluster="cluster-alpha", namespace="db", endpointslice="mongo-abc", label_kubernetes_io_service_name="mongo"}` series, and `kube_endpointslice_endpoints{cluster="cluster-alpha", namespace="db", endpointslice="mongo-abc", targetref_kind="Pod", targetref_name="mongo-0", targetref_namespace="db"}` whose `(namespace, targetref_name)` matches a `kube_pod_info` pod
- **THEN** `ServicesByNameNS[(cluster-alpha, db, mongo)]` carries `cluster_ip="10.96.0.5"` and `EndpointsByService[(cluster-alpha, db, mongo)]` lists the resolved backing pod, while no `service` node or `service-selects-pod` edge is emitted into the graph by the topology reader

### Requirement: Configurable upstream metric-name prefix

The topology reader SHALL prepend a single configurable prefix to every `kube_*` series name it queries, so deployments using a fork of kube-state-metrics or a custom exporter that re-publishes the same series under an organisational prefix (e.g. `o11y_kube_pod_info`) can be supported without forking the API server. The prefix SHALL be sourced from the `KSG_METRIC_PREFIX` environment variable or the `--metric-prefix` flag (flag wins over env when both are set). The default value SHALL be the empty string, preserving stock kube-state-metrics behaviour. The prefix SHALL be additive — appended verbatim before the existing series name; the existing `kube_*` suffix and the upstream label-name contract (`cluster`, `namespace`, `pod`, `uid`, `node`, `persistentvolumeclaim`, `label_*`, etc.) are unchanged. The prefix SHALL be validated against the Prometheus metric-name charset `^[a-zA-Z_:][a-zA-Z0-9_:]*$` when non-empty; an invalid value SHALL fail server startup. The trailing underscore (if any) is the operator's responsibility — the server does not inject one.

The same prefix SHALL apply to every kube-state-metrics-shaped series the reader consumes: `kube_pod_info`, `kube_node_info`, `kube_node_status_addresses`, `kube_pod_spec_volumes_persistentvolumeclaims_info`, `kube_node_labels`, `kube_service_info`, `kube_endpointslice_endpoints`, `kube_endpointslice_labels`, `kube_pod_owner`, `kube_replicaset_owner`, and the `kube_node_info`-backed cluster discovery query. The upstream label-name contract those series carry is unchanged (`cluster`, `namespace`, `pod`, `uid`, `node`, `persistentvolumeclaim`, `label_*`, `service`, `cluster_ip`, `endpointslice`, `address`, `hostname`, `targetref_kind`, `targetref_name`, `targetref_namespace`, `label_kubernetes_io_service_name`, etc.). The prefix SHALL NOT be applied to `traces_service_graph_request_total` (which is produced by a different exporter family) nor to the Prometheus-native `up{}` readiness probe.

#### Scenario: Default empty prefix preserves stock series names

- **WHEN** the server starts without `KSG_METRIC_PREFIX` or `--metric-prefix`
- **THEN** every topology query string contains the bare `kube_*` series name (e.g. `last_over_time(kube_pod_info[<window>])`) and no prefix is added

#### Scenario: Custom prefix from environment

- **WHEN** the server starts with `KSG_METRIC_PREFIX=o11y_`
- **THEN** the issued topology PromQL contains `last_over_time(o11y_kube_pod_info[<window>])`, `last_over_time(o11y_kube_node_info[<window>])`, `last_over_time(o11y_kube_node_status_addresses{type="ExternalIP"}[<window>])`, `last_over_time(o11y_kube_pod_spec_volumes_persistentvolumeclaims_info[<window>])`, `last_over_time(o11y_kube_node_labels[<window>])`, `last_over_time(o11y_kube_service_info[<window>])`, `last_over_time(o11y_kube_endpointslice_endpoints[<window>])`, and `last_over_time(o11y_kube_endpointslice_labels[<window>])`, AND the cluster-discovery query becomes `group by (cluster) (last_over_time(o11y_kube_node_info[<lookback>]))`

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

Every emitted topology entity SHALL carry the canonical fields consumed by the graph API: `id`, `name`, `type`, `labels`, and `ipaddress` (for pods and K8s nodes). The reader SHALL set these as follows:

- For pods: `name` = the `pod` label of `kube_pod_info`; `type` = `"pod"`; `labels` includes `cluster`, `namespace`, `node` (cluster-scoped node ID), and any K8s pod labels available from `kube_pod_labels` for that pod (added under their original keys). `ipaddress` = `[pod_ip]` from `kube_pod_info.pod_ip` when surfaced; otherwise empty / omitted. The `host_ip` series label is intentionally not surfaced on the pod entity — the node's IP is exposed only via the K8s node entity. When kube-state-metrics emits multiple `kube_pod_info` series for the same pod-UID with evolving label sets (e.g. earlier scrapes that lack `node` or `pod_ip`), the reader SHALL merge labels across same-UID samples and pick the newest non-empty `pod_ip` so the emitted entity reflects the most informative observation. When `kube_pod_owner` is available, `labels` additionally includes `owner_kind` and `owner_name` for the pod's controller owner (with the ReplicaSet skipped to the owning Deployment) — see the "Pod controller-owner labels with ReplicaSet skip" requirement; both keys are omitted when the pod has no controller owner.
- For K8s nodes: `name` = the `node` label of `kube_node_info`; `type` = `"node"`; `labels` includes `cluster` and any node labels from `kube_node_labels` for that node (the `label_*=` series translates to entries under their original key with the `label_` prefix removed). `ipaddress` = `[external_ip]` from `kube_node_status_addresses{type="ExternalIP"}` when surfaced; otherwise empty / omitted. IPs SHALL NOT be carried inside `labels`.
- For PVCs: `name` = the `claim_name` label of `kube_pod_spec_volumes_persistentvolumeclaims_info`; `type` = `"pvc"`; `labels` includes `cluster`, `namespace`, and `volume`. `ipaddress` is not emitted.

#### Scenario: Pod entity canonical fields

- **WHEN** `kube_pod_info{cluster="cluster-alpha", namespace="shop", pod="checkout-1", uid="abc", node="worker-0"}` is present
- **THEN** the emitted pod entity has `id="cluster-alpha/abc"`, `name="checkout-1"`, `type="pod"`, `labels.cluster="cluster-alpha"`, `labels.namespace="shop"`, and `labels.node="cluster-alpha/worker-0"`

#### Scenario: Pod IP surfaced on the ipaddress attribute

- **WHEN** `kube_pod_info{cluster="cluster-alpha", namespace="shop", pod="checkout-1", uid="abc", node="worker-0", pod_ip="10.244.0.42", host_ip="10.0.0.7"}` is present
- **THEN** the emitted pod entity has `ipaddress=["10.244.0.42"]`; neither `labels.pod_ip` nor `labels.host_ip` is present, and `host_ip` is dropped because the node's IP lives on the K8s node entity

#### Scenario: Pod ipaddress merged across same-UID samples

- **WHEN** kube-state-metrics emits two `kube_pod_info` series with the same `uid` — one without `pod_ip`/`node` (early scrape during scheduling) and a later one with both populated
- **THEN** the emitted pod entity carries the populated `node` label and `ipaddress=[<pod_ip>]` regardless of the order returned by the upstream

#### Scenario: K8s node ExternalIP surfaced on the ipaddress attribute

- **WHEN** `kube_node_status_addresses{cluster="cluster-alpha", node="worker-0", type="ExternalIP", address="203.0.113.10"}` is present
- **THEN** the emitted K8s node entity has `ipaddress=["203.0.113.10"]` and `labels.external_ip` is not present

#### Scenario: K8s node labels flattened

- **WHEN** the upstream provides `kube_node_labels{cluster="cluster-alpha", node="worker-0", label_topology_kubernetes_io_zone="us-east-1a", label_kubernetes_io_arch="amd64"}`
- **THEN** the emitted node entity's `labels` map contains `topology.kubernetes.io/zone="us-east-1a"` and `kubernetes.io/arch="amd64"` under their original keys

### Requirement: Pod controller-owner labels with ReplicaSet skip

The topology reader SHALL resolve each pod's **controller owner** from `kube_pod_owner` and stamp it onto the pod entity as two strict-string labels, `owner_kind` and `owner_name`. The reader SHALL select the owner whose `owner_is_controller="true"`; when multiple controller owners are reported for a single `(cluster, namespace, pod)` the reader SHALL pick deterministically (lexical order of `(owner_kind, owner_name)`) so the emitted entity is stable across rebuilds.

When the selected controller owner has `owner_kind="ReplicaSet"`, the reader SHALL transparently **skip the ReplicaSet** and resolve one level up via `kube_replicaset_owner` keyed by `(cluster, namespace, replicaset=owner_name)`:

- If a `kube_replicaset_owner` series with `owner_kind="Deployment"` exists for that ReplicaSet, the emitted `owner_kind="Deployment"` and `owner_name` is the Deployment name.
- If no `kube_replicaset_owner` series exists for that ReplicaSet (a bare ReplicaSet with no owning Deployment), the emitted owner SHALL remain `owner_kind="ReplicaSet"`, `owner_name=<replicaset>`.

Owners of any other kind (`DaemonSet`, `StatefulSet`, `Job`, `Node` for static pods reported as a controller, etc.) SHALL be surfaced verbatim with no further resolution. When a pod has no controller owner at all (`kube_pod_owner` absent for the pod, or no series with `owner_is_controller="true"`), the reader SHALL omit BOTH `owner_kind` and `owner_name` from the pod's `labels` — it SHALL NOT emit empty-string values. `kube_pod_owner` and `kube_replicaset_owner` are OPTIONAL: when absent the reader SHALL build a valid topology with no owner labels and SHALL NOT fail the build. This requirement introduces NO new node or edge type — the owner is purely topological metadata on the existing `type="pod"` node, consistent with the strict `map[string]string` `labels` contract (no numbers, no bools).

#### Scenario: Pod owned by a Deployment via ReplicaSet

- **WHEN** `kube_pod_owner{cluster="cluster-alpha", namespace="shop", pod="checkout-1", owner_kind="ReplicaSet", owner_name="checkout-7f9c", owner_is_controller="true"}` and `kube_replicaset_owner{cluster="cluster-alpha", namespace="shop", replicaset="checkout-7f9c", owner_kind="Deployment", owner_name="checkout"}` are present
- **THEN** the emitted pod entity has `labels.owner_kind="Deployment"` and `labels.owner_name="checkout"` (the intermediate ReplicaSet does not appear)

#### Scenario: Bare ReplicaSet with no owning Deployment

- **WHEN** `kube_pod_owner{..., pod="adhoc-1", owner_kind="ReplicaSet", owner_name="adhoc-rs", owner_is_controller="true"}` is present but no `kube_replicaset_owner` series exists for `adhoc-rs`
- **THEN** the emitted pod entity has `labels.owner_kind="ReplicaSet"` and `labels.owner_name="adhoc-rs"`

#### Scenario: Pod owned directly by a non-ReplicaSet controller

- **WHEN** `kube_pod_owner{..., pod="logs-x9", owner_kind="DaemonSet", owner_name="fluentd", owner_is_controller="true"}` is present
- **THEN** the emitted pod entity has `labels.owner_kind="DaemonSet"` and `labels.owner_name="fluentd"` with no `kube_replicaset_owner` lookup

#### Scenario: Pod with no controller owner

- **WHEN** no `kube_pod_owner` series with `owner_is_controller="true"` exists for a pod (e.g. a static or bare pod)
- **THEN** the emitted pod entity carries NEITHER `labels.owner_kind` NOR `labels.owner_name` (the keys are absent, not empty strings)

#### Scenario: Owner metrics absent entirely

- **WHEN** the upstream contains `kube_pod_info` but no `kube_pod_owner` or `kube_replicaset_owner` series for the window
- **THEN** the reader produces a valid topology with no `owner_kind` / `owner_name` labels on any pod and does not fail the build

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
