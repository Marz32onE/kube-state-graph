## MODIFIED Requirements

### Requirement: Storage-flow graph endpoint

The server SHALL expose `GET /v1/storage-graph` returning a storage-flow graph for a caller-specified `[start, end]` window, in the same `{ apiVersion, clusters, elements: { nodes, edges } }` Cytoscape.js shape as `GET /v1/graph`. `start` and `end` SHALL be required and validated exactly as for `/v1/graph` (RFC 3339 or Unix seconds; `end > start`; `missing_start` / `missing_end` / `invalid_start` / `invalid_end` / `invalid_range`). The endpoint SHALL sit behind the same API-key authentication, the same per-build timeout (`--build-timeout` → 504 `timeout`), and the same upstream / outside-retention / cancelled error mapping as `/v1/graph`, and SHALL be described in the served OpenAPI document.

`az` and `env` SHALL be **required** and **single-valued**: a request lacking either SHALL be rejected 400 with `reason: "missing_az"` / `reason: "missing_env"`, and a request repeating either SHALL be rejected 400 with `reason: "invalid_scope"`. The two values SHALL be pushed upstream exactly as the `/v1/graph` selector-level `az` / `env` dimensions are (matchers on the Kubernetes families, backend selection for Harvest), so the body describes one estate: a filer shared across zones or environments is never merged into one diagram. `cluster` and `namespace` SHALL be accepted as optional, repeatable narrowing filters with `/v1/graph` semantics. `prune` SHALL be ignored (this endpoint applies its own reachability projection, never the connectivity prune). `edge_type` — withdrawn from `/v1/graph` as well, so no longer a parameter of any endpoint — and any other unknown parameter SHALL be ignored without error, whatever value they carry.

The top-level `clusters` array SHALL list the Kubernetes cluster identities present on emitted `pod` / `node` / `pvc` nodes and never an ONTAP cluster name.

#### Scenario: Successful request

- **WHEN** a client sends `GET /v1/storage-graph?start=2026-05-01T12:00:00Z&end=2026-05-01T12:05:00Z&az=zone-a&env=prod&aggr=aggr1`
- **THEN** the server returns 200 with a body containing exactly `apiVersion: "v1"`, `clusters`, and `elements` with `nodes` and `edges`

#### Scenario: Missing az

- **WHEN** a client sends `GET /v1/storage-graph?start=...&end=...&env=prod`
- **THEN** the server returns 400 with `reason: "missing_az"`

#### Scenario: Repeated env

- **WHEN** a client sends `GET /v1/storage-graph?start=...&end=...&az=zone-a&env=prod&env=dev`
- **THEN** the server returns 400 with `reason: "invalid_scope"` and a message naming `env`

#### Scenario: Zone and environment reach upstream

- **WHEN** a client sends `?az=zone-a&env=prod`
- **THEN** every kube-state-metrics, kubelet and `ALERTS` query carries `<az-key>="zone-a",<env-key>="prod"`, every Harvest query is issued only to the `harvest` backends whose `zones` include `zone-a` (or catch-alls) with no matcher, and no series from another zone or environment contributes to the body

#### Scenario: Unauthenticated request rejected when keys configured

- **WHEN** API keys are configured and a client sends `GET /v1/storage-graph` without `X-API-Key`
- **THEN** the server returns 401 exactly as `/v1/graph` would

#### Scenario: Graph-only and withdrawn parameters are ignored

- **WHEN** a client sends `GET /v1/storage-graph?start=...&end=...&az=zone-a&env=prod&prune=false&edge_type=not-a-type`
- **THEN** the server returns 200 with a body byte-identical to the same request without `prune` and `edge_type` — neither value is validated, and neither narrows or widens the body
