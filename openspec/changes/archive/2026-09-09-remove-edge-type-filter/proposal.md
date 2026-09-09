## Why

`GET /v1/edge-types` and the `?edge_type=` parameter of `GET /v1/graph` exist
for each other and for one consumer: the front end populated an **Edge type**
control from the catalogue and sent the selection back as `edge_type`. That
consumer has withdrawn the control (`kube-state-graph-frontend` change
`remove-edge-type-filter`, already applied in its working tree) on the stated
premise that the backend no longer honours the parameter — a premise this
change makes true. Left in place, the two surfaces keep a filter nothing sends
and a catalogue nothing reads, and every edge type added to the builder still
has to be described twice (registry prose + OpenAPI enum) for an audience that
no longer exists.

The parameter is also a poor filter on its own terms. It is a
**projection-level** gate over the edge list only: nodes are admitted by the
cluster / namespace filters and by reference (a K8s node hosting an in-scope
pod, an aggregate serving an in-scope claim), none of which consults
`edge_type`. `?edge_type=pod-calls-pod` therefore returns every node of the
default view — K8s nodes, PVCs, aggregates, controllers — with the
`pod-to-node` / `pod-mounts-pvc` / `pvc-to-netapp-aggr` edges that justified
them stripped out, leaving infrastructure nodes nothing connects. It narrows
the wire shape, not the topology, and every edge already carries `data.type`,
so a client that wants fewer edge types drops them after the fact with full
knowledge of what it removed — which is exactly what the front end's legend
toggles now do.

## What Changes

- **BREAKING (HTTP route)**: `GET /v1/edge-types` is removed. The path falls
  through to the standard 404 body exactly as the removed `GET /v1/clusters`
  does; it is dropped from the served OpenAPI document and from the
  `Cache-Control`-carrying and no-timeout route lists.
- **BREAKING (request parameter)**: `edge_type` is withdrawn from `GET
  /v1/graph`. It joins `name`, `root`, `depth` and `direction` as an unknown
  parameter that is **ignored without error**. Two observable consequences:
  `?edge_type=pod-calls-pod` now returns the same body as the request without
  it, and `?edge_type=pod-calls-pods` (a value the registry does not hold) is
  now a 200 rather than the 400 `invalid_scope` it produced. The request
  surface becomes exactly `start`, `end`, `cluster`, `namespace`, `az`, `env`,
  `prune`.
- **BREAKING (in-process embedder, `pkg/`)**: `graph.Scope` loses its
  `EdgeTypes` field, `graph.NewScope` loses its third (`edgeTypes`) parameter
  **and its error return** (edge-type validation was its only failure mode, and
  the `unparam` linter this repository runs rejects a result that is always
  nil), and `graph.ValidEdgeType` is removed — a parameter that compiled but
  did nothing would reproduce at the Go level the silent-inert-control failure
  the front end change exists to avoid. `kubegraph.ParseValues` /
  `Engine.BuildFromValues` keep their signatures; they simply stop reading the
  key. `graph.StorageScope` is unchanged (it never carried the field).
- **Retained**: the in-code `graph.EdgeTypes` registry and
  `graph.EdgeTypeDefinition`. The registry is not a serving artefact: its
  `MayCrossCluster` bit derives `neverCrossCluster`, which buckets the
  `kube_state_graph_graph_edge_count` cross-cluster gauge, and the
  `pod-service-graph` capability pins the `may_cross_cluster: true`
  declaration on `pod-calls-service`. The registry stays the single place an
  edge type is declared; only its two public consumers (the route and the
  parser's validation) go. Trimming its description prose is a separate,
  optional follow-up and is not proposed here.
- **Unchanged**: `GET /v1/storage-graph` (its parser already ignored
  `edge_type`), the `prune` parameter, the connectivity prune, the edge
  retention / partner re-add rule, every edge and node type, every `labels`
  key, the UUIDv5 edge ids, and the `data.type` field every edge carries.
- Documentation follows: the `/v1/graph` OpenAPI annotations lose the
  `edge_type` `@Param` and the catalogue cross-references — and, in the same
  pass, the stale **Traversal** line describing the long-withdrawn `root` /
  `depth` / `direction` parameters (`make docs` regenerates
  `docs/swagger.{json,yaml}`), `README.md` / `README.zh-tw.md`
  drop the catalogue bullet, the curl example and the `edge_type` table row,
  `docs/BREAKING.md` gains a section, and `CLAUDE.md` is corrected where it
  states the request surface and the registry's consumers.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `graph-api`: the `Edge-type discovery endpoint` requirement is **removed**.
  `Filter parameters` drops `edge_type` from the accepted set and lists it
  among the withdrawn, ignored parameters (its "Edge-type filter with no
  matching edges" scenario is replaced by one asserting the withdrawn
  parameter is ignored). `Cytoscape.js response shape` stops defining an
  edge's `type` by reference to `/v1/edge-types`. `Versioned route prefix` and
  `API-key authentication on /v1/* and /debug/*` re-target the scenarios that
  used `/v1/edge-types` as their probe route at `/v1/graph`. `Deterministic
  response body` and `Per-request timeout (non-graph endpoints)` drop the route
  from their explicit route lists.
- `storage-graph-api`: `Storage-flow graph endpoint` no longer names
  `edge_type` as a parameter the endpoint ignores — there is no such parameter
  anywhere any more; it falls under "any other unknown parameter".
- `otlp-observability`: `HTTP request tracing via otelgin` cites
  `GET /v1/edge-types` as an example span name; the example becomes
  `GET /v1/storage-graph` and a scenario pins that the removed route yields no
  span, mirroring the existing `/v1/clusters` scenario.
- `container-integration`: `Coverage of the API contract` drops the
  "`/v1/edge-types` returning the static catalogue" bullet.

`pod-service-graph` is **not** modified: its statement that "the edge-type
registry SHALL declare `may_cross_cluster: true` for `pod-calls-service`" is
about the retained in-code registry and stays true.

## Impact

**Code (this repository)**

- `internal/api/server.go` — drop the `/edge-types` route registration.
  `internal/api/handlers.go` — delete `handleEdgeTypes`, `edgeTypesBody` and
  their swag block; remove the `edge_type` `@Param`, the catalogue
  cross-reference and the `edge_type` example URL from the `/v1/graph`
  annotations. `docs/swagger.{json,yaml}` regenerate (`make check-docs` fails
  CI until they do).
- `pkg/graph/scope.go` — remove `Scope.EdgeTypes`, the `edgeTypes` parameter
  and validation loop of `NewScope`, `edgeTypeAllowed`, `edgeTypeSet`.
  `pkg/graph/project.go` — `filterEdges` loses its type gate.
  `pkg/graph/registry.go` — remove `ValidEdgeType` / `validEdgeTypes`; keep
  `EdgeTypes` and `EdgeTypeDefinition`. `pkg/kubegraph/parse.go` — stop
  reading `v["edge_type"]`.
- **Tests removed** (they test the removed behaviour):
  `internal/api/edge_type_validation_test.go`,
  `pkg/kubegraph/parse_edge_type_test.go`, `TestGolden_EdgeTypes` with
  `internal/api/testdata/golden/edge-types.json`,
  `TestEdgeTypesEndpoint_StaticCatalogue` (`internal/api/server_test.go`),
  `TestEdgeTypesCatalogue` (`internal/integration/graph_e2e_test.go`), the
  edge-type cases of `pkg/graph/scope_test.go`, and the two `Scope{EdgeTypes:
  …}` projection tests in `pkg/graph/project_test.go` /
  `project_netapp_test.go`.
- **Tests re-targeted**: `internal/api/auth_middleware_test.go` and
  `internal/api/tracing_test.go` used `/v1/edge-types` as the cheap
  authenticated route with no upstream call; they move to `/v1/graph` against
  the empty mock querier. Six integration tests (`graph_e2e_test.go` ×2,
  `red_metrics_test.go` ×4) narrowed the body with `edge_type=pod-calls-pod`
  before asserting; they drop the parameter and select by `data.type` /
  endpoint ids client-side, which their assertions already mostly do.
  `pkg/graph/registry_test.go` loses its `ValidEdgeType` assertions and keeps
  the `MayCrossCluster` ones. `pkg/kubegraph/parse_storage_test.go` keeps its
  "edge_type is ignored" assertions verbatim — they now hold for the same
  reason on both endpoints.
- Golden bodies for `/v1/graph` are byte-identical: no golden fixture sets
  `edge_type`, and the projection with an empty type set was already the
  "all edges" path.

**Observability**

- `kube_state_graph_http_requests_total{path="/v1/edge-types"}` (and the
  duration histogram's matching series) stop being produced; requests to the
  old path bucket under `path="<unmatched>"` like any 404. This is a label
  **value** disappearing, not a metric or label contract change. A dashboard
  or alert keyed on that path goes stale.
- The `GET /v1/edge-types` server span disappears; nothing else in tracing
  changes.

**External repositories** (each is a separate change in its own repository;
listed so the release is coordinated, not because this change edits them)

- `kube-state-graph-frontend` — change `remove-edge-type-filter` (15 of 17
  tasks applied, uncommitted, on `feat/pure-ui-frontend`). Its requests
  already omit `edge_type` and it issues no catalogue request, so it is safe
  against the old and the new backend alike. **Release order matters in one
  direction only**: if the backend ships first while a front end that still
  has the control is deployed, that control becomes silently inert (accepts a
  value, writes it to the URL, changes nothing). Ship the front end first or
  together.
- `kube-state-graph-demo` — `charts/kube-state-graph-frontend/values.yaml`
  and `charts/ksg-demo/values.yaml` both set `endpoints.edgeTypes:
  /api/v1/edge-types`. With the key present, `scripts/verify.sh` §10 curls the
  catalogue through the front door and **fails** ("no types returned — the
  edge-type control would be empty") the moment the route 404s; the check is
  skipped only when the key is absent. The key must be dropped from both
  values files (the front end then logs one unknown-key warning per load
  until it is). `README.md` (proxy table, filter-bar paragraph,
  troubleshooting row), `CLAUDE.md` (the "`edge_type` options come from
  `/v1/edge-types`" invariant) and the nginx Secret's comment name the route
  and go stale. The demo's own in-flight change
  `replace-panel-with-frontend-spa` lists `edge_type` among the filter-bar
  dimensions in its proposal, tasks and `frontend-graph-controls` spec.
- `graph-api-gateway` (external embedder of `pkg/`) — compiles unchanged if it
  goes through `kubegraph.Engine.BuildFromValues` / `ParseValues`; a direct
  `graph.NewScope(clusters, namespaces, edgeTypes, inventory)` call or a read
  of `Scope.EdgeTypes` is a compile error to fix, which is the intended
  signal. It receives one fewer route to proxy.

**Clients in general** — any caller sending `edge_type` keeps getting 200s and
now receives the full projection; any caller that validated its own input
against `/v1/edge-types` receives a 404 from that call. Neither is detectable
from the response shape, which is why `docs/BREAKING.md` must carry the entry
and why the front end is sequenced first.

**Rollback** — revert this change. The front end sends no `edge_type` either
way, so reverting restores a parameter nobody sends and a route nobody reads;
there is no data or configuration migration in either direction.

**Dependencies** — none added or removed.
