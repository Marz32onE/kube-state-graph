## Context

See proposal.md — Why. The facts about the current code that shape the approach:

- **The parameter reaches exactly one gate.** `kubegraph.ParseValues` passes
  `v["edge_type"]` into `graph.NewScope`, which validates each value against
  `graph.ValidEdgeType` and stores the set in `Scope.EdgeTypes`; the only
  reader of that set is `filterEdges` in `pkg/graph/project.go`, through
  `Scope.edgeTypeAllowed`. Node admission (`filterNodes`, the reference-gated
  infra predicates, `pullNetAppParents`) never consults it. `edge_type` never
  reaches PromQL: `promql.queryDims` has no bit for it and `promql.Selector`
  has no field. Removing it therefore touches the projection only; every
  upstream query and every built graph is byte-identical before and after.
- **The route is self-contained.** `handleEdgeTypes` serialises
  `graph.EdgeTypes` with a `Cache-Control` header and touches nothing else. It
  is registered inside the `/v1` group, so it is behind `otelgin`, the API-key
  middleware and the span-enrich middleware like the two graph routes.
- **The registry has a second, silent consumer.** `graph.EdgeTypes` derives
  `neverCrossCluster` at init, which `EdgeCountByType` uses to skip the
  per-edge node lookup for types declared `MayCrossCluster: false`; that count
  feeds the `kube_state_graph_graph_edge_count` gauge and the `graph built`
  log line. `pkg/graph/registry_test.go` pins the `MayCrossCluster` bits and
  the `pod-service-graph` spec names the registry declaration. The registry is
  therefore not "the catalogue's backing store" — it is the builder's
  declaration table that the catalogue happened to serialise.
- **Unknown parameters are already a contract.** `name`, `root`, `depth` and
  `direction` were withdrawn by `push-request-filters-upstream` and degrade as
  ignored unknown parameters; the `graph-api` spec has ten scenarios pinning
  that shape, and the front end's removal change was written against it.
- **The removed `GET /v1/clusters` set the route-removal pattern**: no
  registration, so the request falls through to `NoRoute` → the standard error
  body, the access-log / metric `path` label buckets under `<unmatched>`, and
  the spec pins "404, no upstream call, no traced route template".
- **`/v1/edge-types` is the test suite's cheap authenticated route.** Six
  tests in `internal/api/auth_middleware_test.go` and
  `internal/api/tracing_test.go` hit it because it needs no query parameters
  and no upstream. `internal/api/edge_type_validation_test.go` already
  demonstrates the replacement: `/v1/graph?start&end` against
  `newMockQuerier(t, nil)` returns 200 with an empty graph.
- **Six integration tests narrow with `edge_type=pod-calls-pod`** before
  asserting; every one of them already selects the edge it cares about by
  endpoint ids or by `data.type`, so the parameter was a convenience, not a
  precondition.
- **`make check-docs`** fails CI on any drift between the swag annotations in
  `internal/api/handlers.go` and the committed `docs/swagger.{json,yaml}`;
  both currently describe the route and the parameter.

## Goals / Non-Goals

**Goals:**

- Remove the route and the parameter with **no change to any `/v1/graph` or
  `/v1/storage-graph` body for a request that did not send `edge_type`** —
  every existing golden stays byte-identical.
- Make the withdrawal a compile-time event for Go embedders and a documented,
  spec-pinned no-op for HTTP clients, so neither can keep "using" the filter
  without noticing.
- Keep the edge-type declaration table in one place with its `MayCrossCluster`
  semantics and its spec pin intact.
- Leave the test suite with the same coverage of auth, tracing and the RED /
  cross-cluster paths it has today, re-targeted rather than deleted.

**Non-Goals:**

- A replacement discovery endpoint, a 410 Gone, or a redirect.
- Any change to the connectivity prune, `prune`, the edge retention rule, the
  cluster / namespace projection, edge ids, node or edge types, or `labels`
  keys.
- Trimming or restructuring `graph.EdgeTypeDefinition` (the `Description`
  prose, the JSON tags). That is an independent cleanup; doing it here would
  mix a behaviour removal with a taste change.
- Edits to `kube-state-graph-frontend` or `kube-state-graph-demo` — separate
  repositories, coordinated through the Migration Plan below.
- Cleaning up the pre-existing stale mentions of `name` / `root` / `depth` in
  the `/v1/graph` swag description. Only the `edge_type` mentions are in scope;
  the rest is noted as an Open Question.

## Decisions

### D1 — `edge_type` becomes an ignored unknown parameter, not a rejected one

The parser stops reading the key. No `withdrawn_parameter` reason, no 400.

*Why:* this is the established degrade path for `name` / `root` / `depth` /
`direction`, it is what the `graph-api` "unknown filter parameter SHALL be
ignored without error" clause already promises, and it is the premise the
front-end change was written and released against. A rejection would make
`edge_type` the only parameter the server has ever refused by *name*, would
break every bookmark and shared link that still carries it, and would need a
new `build.Reason` + `mapBuildError` entry for a one-release transition.

*Alternative rejected:* 400 with a new reason so the inertness is loud. Loud
for whom? The front end no longer sends it, and an unknown third-party client
that receives a 400 for a parameter the OpenAPI document no longer lists has
no worse a time than one that receives the unfiltered body. The visible
consequence — `?edge_type=pod-calls-pods` flips from 400 to 200 — is stated in
`docs/BREAKING.md` and pinned by a spec scenario instead.

### D2 — `Scope.EdgeTypes` and the `NewScope` parameter are removed, not ignored

`graph.Scope` loses the field; `graph.NewScope(clusters, namespaces, inventory
bool)` loses its third argument; `graph.ValidEdgeType` and the `validEdgeTypes`
set go with them; `Scope.edgeTypeAllowed` and `edgeTypeSet` are deleted; the
`filterEdges` type gate is removed.

*Why:* a Go parameter that is accepted and discarded is the in-process twin of
the inert UI control the front end just removed. `pkg/` is imported by
`graph-api-gateway`; a compile error there is the one signal that reaches that
team without a runbook, and it is a mechanical fix.

`NewScope` also loses its `error` result. Edge-type validation was its only
failure mode; every remaining dimension is an opaque string set whose values
the request parser has already checked for length and control characters, and
an unknown cluster or namespace is an empty result rather than a rejection.
Keeping the result would leave a return that is always nil — which the
`unparam` linter this repository enables rejects outright, and which would
force every caller to handle an impossible error.

*Alternative rejected:* keep the signature, ignore the argument, deprecate in a
comment. Rejected for the reason above — and because the deprecated argument
would still have to be *validated or not*, and either answer is a surprise.

### D3 — The registry stays; only its catalogue consumers go

`graph.EdgeTypes` and `graph.EdgeTypeDefinition` are untouched. `ValidEdgeType`
is removed because its only callers were `NewScope` and its own tests.

*Why:* see Context — `neverCrossCluster` is derived from the registry, the
`pod-service-graph` spec pins a registry declaration, and the registry is the
one place a new edge type is declared for the builder. Deleting it would move
the `MayCrossCluster` facts into a hand-maintained set beside `EdgeCountByType`,
which is the "stale gate" the registry comment was written to prevent.

*Alternatives rejected:* (a) collapse the registry to a `map[EdgeType]bool`
of cross-cluster bits — loses the label-key and endpoint-type declarations
that tests and specs still reference by name, for a saving of prose nobody is
asking for; (b) delete the `Description` fields now — orthogonal, see
Non-Goals.

### D4 — Route removal is a fall-through 404, exactly like `/v1/clusters`

`v1.GET("/edge-types", …)` is deleted and the handler, its body type and its
swag block go with it. Nothing is registered in its place.

*Why:* the `/v1/clusters` removal already established the shape (standard 404
body, `<unmatched>` path bucket, no span template, no OpenAPI entry), and the
specs pin it. A 410 Gone would need a live route registration to produce it,
which keeps the path in the Gin route table, in the OpenAPI document (or
triggers the drift guard) and in the `path` metric label — for a route whose
one consumer already stopped calling it.

### D5 — Test re-targeting uses `/v1/graph` against the empty mock querier

The auth and tracing tests move from `/v1/edge-types` to
`/v1/graph?start=…&end=…` with `newMockQuerier(t, nil)`, which returns 200
with an empty graph today (the deleted `edge_type_validation_test.go` relied on
exactly that). Span-name assertions become `GET /v1/graph`.

*Why:* it is the closest existing route with the same middleware chain
(`otelgin` → API key → span enrich). The alternative, `/v1/storage-graph`,
also needs `az` and `env` and would exercise `BuildStorage` in tests that are
about middleware, not builds. The `waitForSpans` helper stays: the 4 KB
write-buffer race it documents was *observed* on the large catalogue body, but
`otelgin` still ends the span in a deferred call, so the wait is still the
correct shape for a positive assertion.

### D6 — Integration tests drop the parameter and select client-side

The six `q.Set("edge_type", "pod-calls-pod")` calls are removed. Each test's
assertion already keys on endpoint ids (`red-c → red-s`,
`"target":"cluster-beta/beta-1"`) or on `data.type`; where one relied on the
body containing *only* pod-call edges, it filters the parsed edge slice by
`Data.Type` first. `TestEdgeTypesCatalogue` is replaced by a test that (a)
issues the same graph request with and without `edge_type=pod-calls-pod` and
asserts byte-identical bodies, and (b) asserts `GET /v1/edge-types` is a 404
with the standard error body — which is the `container-integration` coverage
bullet this change substitutes for the catalogue one.

### D7 — Documentation moves in one pass, swag first

Order inside the implementation: edit the `/v1/graph` swag annotations
(remove the `edge_type` `@Param`, the catalogue cross-reference in the
Endpoint-resolution paragraph, and the `&edge_type=pod-calls-pod` example
URL), delete the `handleEdgeTypes` swag block, run `make docs`, commit
`docs/swagger.{json,yaml}` in the same change so `make check-docs` is green.
`README.md` / `README.zh-tw.md` lose the catalogue bullet, the curl example
switches to `/v1/graph`, the `edge_type` row leaves the parameter table.
`docs/BREAKING.md` gains a section in the existing style (removed route,
withdrawn parameter, embedder signature change, what a client should do).
`CLAUDE.md` is corrected at the request-surface bullet, the
"`/v1/edge-types` reads from `graph.EdgeTypes`" bullet (which becomes a
statement about the registry being the builder's declaration table) and the
lifecycle diagram line that lists `edge_type` among the projection filters.

## Risks / Trade-offs

- **An unknown client keeps sending `edge_type` and now receives the full
  edge set.** → Nothing in the response reveals it; this is the accepted cost
  of D1 and the reason `docs/BREAKING.md` carries the entry and the front end
  ships first. The client already had every edge's `data.type` to filter on.
- **A front end that still has the control is deployed after the backend
  ships.** → The control becomes silently inert. Mitigated by release order
  (front end first or together — its change is already applied in its working
  tree and is safe against both backends) and stated in the Migration Plan.
- **`kube-state-graph-demo`'s `make verify` fails.** → Its §10 curls the
  catalogue whenever `endpoints.edgeTypes` is present in the front end's
  `config.json`, and both of its values files set it. The check self-skips
  once the key is dropped; that drop is a demo-repo change and is listed as a
  Migration step, not assumed.
- **`graph-api-gateway` stops compiling.** → Intended (D2). The fix is
  deleting one argument / one field read; `kubegraph.Engine.BuildFromValues`
  callers are unaffected.
- **Dashboards or alerts keyed on `path="/v1/edge-types"`.** → The series
  stops being produced; a `404` to the old path counts under
  `path="<unmatched>"`. Label value only, no contract change; noted in
  `docs/BREAKING.md`.
- **A MODIFIED delta that restates a long requirement (Cytoscape.js response
  shape, Filter parameters) drifts from the main spec by a stray edit.** → Each
  MODIFIED block was copied whole from `openspec/specs/` and edited in the
  named places only; `openspec validate --strict` is a task gate, and the
  archive diff is the review artefact.

## Migration Plan

1. **Front end** — `kube-state-graph-frontend` change `remove-edge-type-filter`
   ships (or is already deployed). Its requests carry no `edge_type` and it
   issues no catalogue request, so it runs against the old and the new backend
   alike.
2. **Backend** — this change. Release notes point at the new
   `docs/BREAKING.md` section.
3. **Demo repository** — drop `endpoints.edgeTypes` from
   `charts/kube-state-graph-frontend/values.yaml` and
   `charts/ksg-demo/values.yaml`; delete the `/api/v1/edge-types` row from the
   README proxy table, the "`edge_type` options come from `/v1/edge-types`"
   paragraph and the troubleshooting row; correct the matching `CLAUDE.md`
   invariant and the nginx Secret comment; retire the §10 catalogue block in
   `scripts/verify.sh` (it would otherwise remain as dead, key-gated code).
   Until step 3 lands, `make verify` fails at §10 against a backend from step
   2.
4. **Embedders** — `graph-api-gateway` removes the third `NewScope` argument
   and any `Scope.EdgeTypes` read; no behaviour change for it beyond one fewer
   proxied route.

**Rollback** — revert this change alone. The front end sends no `edge_type`
either way, so a revert restores a route nobody reads and a parameter nobody
sends; no data, configuration or golden migration in either direction. The
demo repository's step 3 does not need reverting (the front end tolerates a
missing `endpoints.edgeTypes` and `verify.sh` skips the block).

## Open Questions

- ~~The `/v1/graph` swag description still advertises `name` as a filter and a
  `root` / `depth` / `direction` traversal, all withdrawn by
  `push-request-filters-upstream`.~~ **Resolved during implementation**: the
  stale Traversal line was removed and the Filters line now names the four live
  dimensions with the five withdrawn ones listed as ignored, in the same
  `make docs` run. Documentation only; no spec impact.
- Whether to trim `graph.EdgeTypeDefinition.Description` (long prose written
  for the catalogue) now that nothing serves it. Deferrable: it changes no
  behaviour and no spec.
