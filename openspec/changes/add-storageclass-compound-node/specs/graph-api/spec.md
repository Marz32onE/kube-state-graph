## MODIFIED Requirements

### Requirement: Cytoscape.js response shape

`GET /v1/graph` SHALL return a JSON document in Cytoscape.js shape: `{ apiVersion, clusters, elements: { nodes, edges } }`. The body SHALL NOT contain time-varying or echo-of-input fields, so identical inputs against the same upstream state produce byte-identical bodies.

Each **node** SHALL be `{ data: { id, name, type, labels } }`:
- `id` SHALL be a cluster-scoped composite for pods / K8s nodes / PVCs / services (pods: `<cluster>/<pod-uid>`; nodes: `<cluster>/<node-name>`; PVCs: `<cluster>/<namespace>/<claim>`; services: `<cluster>/<namespace>/<service>`). For external nodes (unresolvable `"://"` connection-string endpoints or missing-UID human-label fallback), `id` SHALL be `external/<label-value>` (no cluster prefix).
- `name` SHALL be the human-readable pod / node / PVC / service name. For external nodes, `name` SHALL be the verbatim `client` or `server` label value from the source service-graph series.
- `type` SHALL be one of the strings `"pod"`, `"node"`, `"pvc"`, `"service"`, `"external"`. The Cytoscape serialiser additionally synthesises `"cluster"` and `"storageclass"` group nodes for compound nesting (see "Cytoscape compound node grouping").
- `data` MAY carry an optional `parent` field (`omitempty`) referencing the `id` of the node's Cytoscape compound container — see "Cytoscape compound node grouping".
- `labels` SHALL be a JSON object whose values are strings only (`map[string]string`). For pod / K8s node / PVC / service nodes it SHALL include at minimum a `cluster` entry; for pods, PVCs, and services it SHALL also include a `namespace` entry; for pods it SHALL include `node` (the cluster-scoped node ID), and SHALL include `pod_ip` and `host_ip` whenever the upstream `kube_pod_info` series carried them; for K8s nodes it SHALL include `external_ip` when the upstream provided one. **For external nodes**, `labels` SHALL be an empty object `{}` (no `cluster` key).

Each **edge** SHALL be `{ data: { id, type, source, target, labels } }`:
- `id` SHALL be a UUID, RFC 4122 compliant, encoded as a lowercase canonical string.
- `type` SHALL be one of the registered edge types from `/v1/edge-types`.
- `source` and `target` SHALL each match the `id` of a node present in the same response's `elements.nodes`.
- `labels` SHALL be a JSON object whose values are strings only (`map[string]string`). The exact key set per edge type is defined by the `pod-service-graph` and `cluster-topology-source` capabilities.

Implementations SHALL NOT encode booleans or numbers as strings inside `labels`. Non-string-typed data (numeric metrics, boolean flags) is deferred to a future typed struct field on `data` and is NOT part of the v1 contract.

#### Scenario: Pod node payload

- **WHEN** the response contains a pod node
- **THEN** its `data.type` equals `"pod"`, its `data.id` matches `<cluster>/<pod-uid>`, its `data.name` equals the pod's metadata name, and `data.labels.cluster` matches the cluster prefix in the ID

#### Scenario: Pod node payload includes pod_ip and host_ip when upstream emits them

- **WHEN** the response contains a pod node whose source `kube_pod_info` series carried `pod_ip` and `host_ip`
- **THEN** `data.labels.pod_ip` equals the upstream `pod_ip` value and `data.labels.host_ip` equals the upstream `host_ip` value

#### Scenario: K8s node payload

- **WHEN** the response contains a Kubernetes-node node
- **THEN** its `data.type` equals `"node"`, its `data.id` matches `<cluster>/<node-name>`, its `data.name` equals the node's metadata name, and `data.labels.external_ip` is present whenever the upstream metric provided one

#### Scenario: PVC node payload

- **WHEN** the response contains a PVC node
- **THEN** its `data.type` equals `"pvc"`, its `data.id` matches `<cluster>/<namespace>/<claim>`, its `data.name` equals the claim name, and `data.labels.namespace` equals the PVC namespace

#### Scenario: PVC node carries no storageclass attribute

- **WHEN** the response contains a PVC node whose StorageClass was resolved from `kube_persistentvolumeclaim_info`
- **THEN** the PVC node's `data` has no `storageclass` field and its `labels` has no `storageclass` key — the StorageClass surfaces only via the node's `data.parent` and the synthetic `type="storageclass"` group node

#### Scenario: Service node payload

- **WHEN** the response contains a service node (a connection-string endpoint that resolved to an in-cluster service via `kube_service_info`)
- **THEN** its `data.type` equals `"service"`, its `data.id` matches `<cluster>/<namespace>/<service>`, its `data.name` equals the service name, `data.labels.cluster` matches the cluster prefix in the ID, `data.labels.namespace` equals the service namespace, and `data.ipaddress` equals `[cluster_ip]` whenever the upstream `kube_service_info` `cluster_ip` value is not `"None"`

#### Scenario: External node payload (unresolvable connection-string endpoint)

- **WHEN** the response contains an external node produced by an unresolvable `"://"` connection-string endpoint (a `client` or `server` label containing `"://"` whose host did not resolve to an in-cluster service)
- **THEN** its `data.type` equals `"external"`, its `data.id` equals `external/<value>`, its `data.name` equals `<value>` (the verbatim service-graph `client` or `server` label), and `data.labels` equals `{}`

#### Scenario: External node payload (missing-UID fallback)

- **WHEN** the response contains an external node produced by the missing-UID human-label fallback (a service-graph series whose `client_k8s_pod_uid` or `server_k8s_pod_uid` was empty but the corresponding `client`/`server` label was populated and contained no `"://"`)
- **THEN** its `data.type` equals `"external"`, its `data.id` equals `external/<value>`, its `data.name` equals `<value>`, and `data.labels` equals `{}`

#### Scenario: Edge payload references existing nodes

- **WHEN** the response contains any edge
- **THEN** both `data.source` and `data.target` SHALL match the `data.id` of a node present in the same response's `elements.nodes`

#### Scenario: Edge id is a UUID

- **WHEN** the response contains any edge
- **THEN** `data.id` matches the regex `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

#### Scenario: Edge id is stable across rebuilds

- **WHEN** the same logical edge (same `type`, `source`, `target`) is produced by two consecutive builds for the same time bucket
- **THEN** `data.id` is byte-identical between the two builds

### Requirement: Cytoscape compound node grouping

`GET /v1/graph` SHALL express the cluster / node / pod and cluster / storageclass / pvc hierarchies as Cytoscape compound nodes via an optional `data.parent` field, and SHALL synthesise one group node per cluster plus one group node per `(cluster, StorageClass)` pair backing an emitted PVC. This is a presentation concern of the Cytoscape serialiser; it SHALL NOT affect the core graph, projection, or traversal.

The serialiser SHALL emit, for each distinct `labels.cluster` value present on an emitted node, one synthetic group node `{ data: { id: "cluster/<cluster>", name: "<cluster>", type: "cluster", labels: {} } }` with no `parent` and no `ipaddress`. These group nodes SHALL be emitted before the other nodes, ordered by cluster name, so the body stays byte-deterministic.

The serialiser SHALL also emit, for each distinct `(cluster, StorageClass)` pair carried by an emitted `type="pvc"` node that has a non-empty resolved StorageClass, one synthetic group node `{ data: { id: "<cluster>/storageclass/<storageclass>", name: "<storageclass>", type: "storageclass", labels: {}, parent: "cluster/<cluster>" } }` with no `ipaddress`. The StorageClass group node carries no `labels` (the empty object `{}`, matching the cluster group node); its cluster identity is expressed solely by its `id` and `parent`. These StorageClass group nodes SHALL be emitted after the cluster group nodes and before the non-group nodes, ordered by `(cluster, storageclass)`, so the body stays byte-deterministic. No StorageClass group node SHALL be synthesised for a `(cluster, StorageClass)` pair that no emitted PVC references, and none SHALL be synthesised for a PVC whose resolved StorageClass is empty.

Each non-group node's `data.parent` SHALL be assigned as:

- `type="pod"` → the pod's K8s node id (its `labels.node` value) when that node is present in the same response; else `cluster/<labels.cluster>` when the pod has a non-empty cluster; else omitted.
- `type="node"`, `type="service"` → `cluster/<labels.cluster>`.
- `type="pvc"` → its StorageClass group id `<cluster>/storageclass/<storageclass>` when the PVC has a non-empty resolved StorageClass (in which case that StorageClass group node is also emitted); else `cluster/<labels.cluster>`.
- `type="storageclass"` → `cluster/<labels.cluster>`.
- `type="external"` → omitted (no cluster identity).

The `parent` field SHALL use `omitempty` semantics (absent when there is no parent). Services and PVCs SHALL NOT be compound parents of pods (a Service spans nodes and a pod may back multiple Services; those relationships remain edges). A StorageClass group node SHALL contain only PVCs — it SHALL NOT be the parent of any pod, K8s node, or service.

There is no `pod-runs-on-node` edge. The pod→node relationship SHALL be expressed solely by the `cluster > node > pod` compound nesting, derived from each pod's `labels.node`. K8s `node` nodes therefore carry no edges and act purely as compound containers. Relationship edges (`pod-mounts-pvc`, `service-selects-pod`, `pod-calls-pod`, `pod-calls-service`) SHALL be retained in the Cytoscape output.

#### Scenario: Cluster group node synthesised

- **WHEN** the graph contains any node with `labels.cluster="cluster-alpha"`
- **THEN** the Cytoscape response contains a node `{ data: { id: "cluster/cluster-alpha", name: "cluster-alpha", type: "cluster", labels: {} } }` with no `parent` field

#### Scenario: cluster > node > pod nesting

- **WHEN** a pod node carries `labels.node="cluster-alpha/worker-0"` and the K8s node `cluster-alpha/worker-0` is in the response
- **THEN** the pod's `data.parent` equals `"cluster-alpha/worker-0"` and the K8s node's `data.parent` equals `"cluster/cluster-alpha"`

#### Scenario: pod falls back to cluster parent when its node is not in scope

- **WHEN** a pod carries `labels.node="cluster-alpha/worker-0"` but that K8s node is not present in the response (e.g. filtered out)
- **THEN** the pod's `data.parent` equals `"cluster/cluster-alpha"`

#### Scenario: StorageClass group node synthesised and PVC nested (cluster > storageclass > pvc)

- **WHEN** the response contains a `type="pvc"` node in `cluster-alpha` whose resolved StorageClass is `gp3`
- **THEN** the response contains a group node `{ data: { id: "cluster-alpha/storageclass/gp3", name: "gp3", type: "storageclass", labels: {}, parent: "cluster/cluster-alpha" } }`, the PVC's `data.parent` equals `"cluster-alpha/storageclass/gp3"`, and that StorageClass group node's `data.parent` equals `"cluster/cluster-alpha"`

#### Scenario: PVC without StorageClass falls back to cluster parent

- **WHEN** the response contains a `type="pvc"` node in `cluster-alpha` whose resolved StorageClass is empty (metric absent or unmatched)
- **THEN** no `type="storageclass"` group node is synthesised on its behalf and the PVC's `data.parent` equals `"cluster/cluster-alpha"`

#### Scenario: service and PVC-without-StorageClass parented to cluster, not containing pods

- **WHEN** the response contains a `type="service"` node and a `type="pvc"` node with no resolved StorageClass in `cluster-alpha`
- **THEN** each has `data.parent="cluster/cluster-alpha"`, and neither is the `parent` of any pod

#### Scenario: external nodes have no parent

- **WHEN** the response contains a `type="external"` node
- **THEN** that node's `data` has no `parent` field, and no cluster group node is synthesised for an endpoint carrying no `cluster` label
