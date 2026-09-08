## MODIFIED Requirements

### Requirement: Query family classification

Every upstream query the server issues SHALL belong to exactly one of six fixed families, and the mapping SHALL be a hardcoded contract with no configuration surface:

- `ksm` — every `kube_*` kube-state-metrics series (pod, node, PVC, Service, EndpointSlice, owner, and controller-annotation families).
- `kubelet` — `kubelet_volume_stats_used_bytes` and `kubelet_volume_stats_capacity_bytes`.
- `harvest` — every NetApp Harvest series: `volume_labels`, the six `qos_*` workload families, the two `qos_policy_fixed_max_throughput_*` families, `aggr_new_status`, `aggr_space_used`, `aggr_space_total`, `node_new_status`, `node_labels`, `node_cpu_busy`, `node_total_ops`, `node_total_latency`, `node_total_data`.
- `servicegraph` — the three `traces_service_graph_*` series.
- `probe` — the `up{}` probe.
- `alerts` — the `ALERTS` series (the `alert-overlay` capability). This family is zone-routable (`az` selects its backends, and `az` / `env` / `namespace` are rendered as matchers on it); it is the only family a valid table may leave unserved.

A query with no declared family SHALL be a build-time failure of the repository's own test suite, not a runtime default: the classification table SHALL be exhaustive over the query set by construction.

A **caller-declared** family is the one exception to derivation from the query name, and it is confined to the embedder-facing label query of the `metrics-label-query` capability: because that query names an arbitrary metric, no classification table can decide which store holds it, so the caller declares the family per call. A declared family SHALL be validated against the same six names, and SHALL then be dispatched through the **unchanged** rules of this capability — the same backend selection by zone, the same identical query string per backend, the same de-duplicating merge, and the same fail-closed behaviour on a backend error. A caller-declared family SHALL NOT introduce a second dispatch policy, SHALL NOT widen the family set, and SHALL NOT make the family of a server-issued query configurable.

#### Scenario: Every query is classified

- **WHEN** the repository's test suite runs
- **THEN** a test enumerates every declared query and fails if any one of them has no family entry

#### Scenario: Harvest separable from kube-state-metrics

- **WHEN** the table declares one backend serving `ksm`, `kubelet`, `servicegraph` and `probe` at one URL and another serving `harvest` at a different URL
- **THEN** every `kube_*` and `kubelet_*` query is sent only to the first URL and every Harvest query only to the second

#### Scenario: Alerts separable from kube-state-metrics

- **WHEN** the table declares one backend serving `ksm` at one URL and another serving `alerts` at a different URL
- **THEN** the `ALERTS` query is sent only to the second URL and never to the first

#### Scenario: Caller-declared family dispatches through the same rules

- **WHEN** a consumer issues a label query naming an arbitrary metric and declaring family `harvest` with zone `zone-b`, against a table whose Harvest backends are split by zone
- **THEN** the query reaches exactly the Harvest backends the zone rule selects for a server-issued Harvest query under `az=zone-b`, and a backend error fails the call naming that backend

#### Scenario: Server-issued queries keep their derived family

- **WHEN** the graph build issues its own queries
- **THEN** each one's family is still read from the hardcoded classification table, and no caller value can override it
