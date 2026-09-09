## 1. Projection scope and request parsing (`pkg/graph`, `pkg/kubegraph` — design D1, D2, D3)

- [x] 1.1 In `pkg/graph/scope.go` remove `Scope.EdgeTypes`, the `edgeTypes` parameter and its validation loop from `NewScope` (new signature `NewScope(clusters, namespaces []string, inventory bool)`), and the `edgeTypeAllowed` / `edgeTypeSet` helpers; in `pkg/graph/registry.go` remove `ValidEdgeType` and `validEdgeTypes` while leaving `EdgeTypes` / `EdgeTypeDefinition` untouched, and reword the two comments that describe the registry as "consumed by the /v1/edge-types handler"; verify `go build ./...` fails only at the callers the tasks below fix.
- [x] 1.2 In `pkg/graph/project.go` remove the `scope.edgeTypeAllowed(e.Type)` gate from `filterEdges` and drop "Apply the edge-type filter" from the `Project` order-of-operations comment; verify `go vet ./pkg/graph/` is clean and `neverCrossCluster` / `EdgeCountByType` in `graph.go` are untouched (`git diff pkg/graph/graph.go` is empty).
- [x] 1.3 Update `pkg/graph` tests: delete the edge-type validation cases from `scope_test.go` and fix its remaining `NewScope` calls to three arguments; delete the `Scope{EdgeTypes: …}` projection tests at `project_test.go:115` and `project_netapp_test.go:115`; fix the four `NewScope` calls at `project_test.go:285–312`; drop the `ValidEdgeType` assertions from `registry_test.go` and keep its `MayCrossCluster` assertions; verify `go test ./pkg/graph/ -count=1 -race -shuffle=on` passes, including `TestEdgeCountByType_CrossClusterBuckets` unmodified.
- [x] 1.4 In `pkg/kubegraph/parse.go` stop reading `v["edge_type"]`, call the three-argument `NewScope`, and correct the `Request` doc comment (`prune` is the only projection-only parameter) and the `ParseStorageValues` comment; delete `pkg/kubegraph/parse_edge_type_test.go` and add one test asserting `ParseValues` with `edge_type=pod-calls-pods` returns no error and a `Request` equal to the same values without the key; keep `parse_storage_test.go` verbatim; verify `go test ./pkg/kubegraph/ -count=1 -race` passes.

## 2. HTTP surface (`internal/api` — design D4, D5, D7)

- [x] 2.1 Delete the `v1.GET("/edge-types", s.handleEdgeTypes)` registration in `internal/api/server.go`, and `handleEdgeTypes`, `edgeTypesBody` and their swag block in `internal/api/handlers.go`; replace `TestEdgeTypesEndpoint_StaticCatalogue` in `server_test.go` with a test asserting `GET /v1/edge-types` returns 404 with `reason: "not_found"` (mirror the `/v1/clusters` test in `server_happy_test.go:183`); verify the test passes.
- [x] 2.2 Edit the `/v1/graph` swag annotations in `handlers.go`: remove the `edge_type` `@Param`, drop `edge_type` from the **Filters** description line, delete the sentence "See `/v1/edge-types` for the authoritative per-type catalogue.", and remove `&edge_type=pod-calls-pod` from the example URL; on `/v1/storage-graph` change "`edge_type` and `prune` are ignored" to "`prune` and any unknown parameter are ignored"; run `make docs`; verify `make check-docs` passes and `grep -c 'edge-types\|edge_type' docs/swagger.json docs/swagger.yaml` reports 0 for both.
- [x] 2.3 Delete `internal/api/edge_type_validation_test.go`; add a component test (mock querier with a fixture set that yields at least one `pod-calls-pod` and one `pod-to-node` edge, e.g. the golden single-cluster fixtures) asserting that `/v1/graph?…&edge_type=pod-calls-pod` returns 200 with a body byte-identical to the same request without the parameter, and that `?edge_type=pod-calls-pods` is 200 not 400; verify the test passes.
- [x] 2.4 Delete `TestGolden_EdgeTypes` from `golden_test.go` and `internal/api/testdata/golden/edge-types.json`; verify `go test ./internal/api/ -run Golden -count=1` passes with no `-update` and `git status` shows no other golden file modified.
- [x] 2.5 Re-target the four `/v1/edge-types` requests in `auth_middleware_test.go` and the five in `tracing_test.go` at `/v1/graph?start=2026-05-01T11:00:00Z&end=2026-05-01T12:00:00Z` against `newMockQuerier(t, nil)`; rename `TestTracing_EdgeTypesEmitsServerSpan` and assert the span name `GET /v1/graph`; update the `waitForSpans` comment that cites the catalogue body size; add a tracing test asserting `GET /v1/edge-types` is a 404 that exports no span named `GET /v1/edge-types`; verify `go test ./internal/api/ -count=1 -race -shuffle=on` passes.

## 3. Integration suite (`internal/integration` — design D6; needs Docker)

- [~] 3.1 Remove the six `q.Set("edge_type", "pod-calls-pod")` calls (`graph_e2e_test.go` `TestCrossClusterEdgePresent`, `TestConnStringUnresolvableProducesExternalNode`; `red_metrics_test.go` ×4) and, where a test asserted over the whole edge list, first filter the parsed edges by `Data.Type == "pod-calls-pod"`; verify `go test ./internal/integration/ -run 'TestGraphSuite|TestRED' -count=1` passes with Docker available.
- [~] 3.2 Replace `TestEdgeTypesCatalogue` with a test that (a) issues one graph request with and one without `edge_type=pod-calls-pod` and asserts byte-identical bodies, and (b) asserts `GET /v1/edge-types` returns 404 with `reason: "not_found"`; verify it passes with Docker available — this is the `container-integration` coverage bullet the delta spec substitutes.

## 4. Documentation (design D7)

- [x] 4.1 `README.md`: remove the "Exposes a static edge-type catalogue" bullet, point the API-key curl example at `/v1/graph?start=…&end=…`, and delete the `edge_type` row from the parameter table; make the matching three edits in `README.zh-tw.md`; verify `grep -n 'edge-types\|edge_type' README.md README.zh-tw.md` prints nothing.
- [x] 4.2 Add a new leading section to `docs/BREAKING.md` in the existing style covering: the removed `GET /v1/edge-types` route (404, no upstream call, no OpenAPI entry, `path="<unmatched>"` in the HTTP self-metrics); the withdrawn `edge_type` parameter and its two visible consequences (a filtered request now returns the full projection; an unregistered value is 200 not 400); the `pkg/` signature changes (`graph.Scope.EdgeTypes`, `graph.NewScope`, `graph.ValidEdgeType`); what a client that used either surface should do (`data.type`); verify the section renders and names every item above.
- [x] 4.3 `CLAUDE.md`: drop `edge_type` from the request-lifecycle diagram line and the result-cache key note, restate the request-surface bullet as `start, end, cluster, namespace, az, env, prune` with `edge_type` among the withdrawn parameters, and rewrite the "`/v1/edge-types` reads from `graph.EdgeTypes` only" bullet as a statement that `graph.EdgeTypes` is the builder's declaration table whose `MayCrossCluster` bit drives the cross-cluster edge count and whose `pod-calls-service` declaration the `pod-service-graph` spec pins; verify `grep -n 'edge-types\|edge_type' CLAUDE.md` matches only the withdrawn-parameter mention.

## 5. Repository gates

- [~] 5.1 Run `make lint`, `make vet`, `make test` and `make check-docs`; verify all four are green and `make verify-mocks` reports no drift (no mocked interface changed).
- [x] 5.2 Run `grep -rn 'edge_type\|edge-types\|ValidEdgeType\|Scope{EdgeTypes\|\.EdgeTypes\b' --include='*.go' pkg internal cmd`; verify the only hits are the retained `graph.EdgeTypes` registry, its `neverCrossCluster` derivation and `registry_test.go`.
- [x] 5.3 Run `openspec validate remove-edge-type-filter --strict`; verify it reports the change valid before archiving.

## 6. Coordination outside this repository (tracked here, executed in the owning repositories)

- [x] 6.1 Confirm `kube-state-graph-frontend` change `remove-edge-type-filter` is merged and deployed before or together with this release; verify its `main` carries no `endpoints.edgeTypes` key handling beyond the unknown-key warning and no request to an edge-type URL.
- [ ] 6.2 Open the `kube-state-graph-demo` follow-up: drop `endpoints.edgeTypes` from `charts/kube-state-graph-frontend/values.yaml` and `charts/ksg-demo/values.yaml`, remove the `/api/v1/edge-types` proxy-table row, the "`edge_type` options come from `/v1/edge-types`" paragraph and the troubleshooting row from its `README.md`, correct the matching `CLAUDE.md` invariant and the nginx Secret comment, and retire the §10 catalogue block in `scripts/verify.sh`; verify `make verify` passes there against a backend built from this change.
- [ ] 6.3 Notify the `graph-api-gateway` owners of the `graph.NewScope` signature change and the removed `Scope.EdgeTypes` / `ValidEdgeType`; verify their module compiles against the new `pkg/graph`.

## Verification status (recorded at implementation time)

`[~]` marks an edited-but-unverified task. The edits are made; the command
that would prove them could not run **on this machine**, and each names what
it needs.

**Ran and passed here:**

| Command | Result |
|---|---|
| `go test ./pkg/graph/ -count=1 -race -shuffle=on` | ok 12.148s |
| `go test ./pkg/kubegraph/ -count=1 -race` | ok 1.293s |
| `go test ./internal/api/ -count=1 -race -shuffle=on` | ok 5.065s |
| `go build ./...` | clean |
| `go vet ./pkg/graph/`, `go vet ./internal/api/` | clean |
| `make docs` + swagger inspection | 7 paths, `/v1/graph` params exactly `start,end,cluster,namespace,az,env,prune`; no `edgeTypesBody` definition |
| `gofmt -l` over every edited package | clean |
| `openspec validate remove-edge-type-filter --strict` | valid |

**Could not run here — 7 GB RAM / 2 cores with swap exhausted:**

- `go vet ./internal/integration/` — killed by the OOM reaper after 25 minutes,
  three times, including at `GOMAXPROCS=1 -p 1 GOGC=40`. The package links
  testcontainers, istio and the ClickHouse driver; compiling that tree needs
  more memory than the host has free. **Not a code signal.** Substitute checks
  performed: `gofmt -e` parses both edited files; every import is still used
  (`url` survives in both, `io`/`http`/`json` unchanged); `graphURL`'s second
  parameter is documented nillable and nil-checked, and `StartAPIServer(nil)`
  has 18 existing call sites; `s.Empty` / `s.Contains` / `s.Require()` are all
  used elsewhere in the suite.
- `make lint` — `golangci-lint` was OOM-killed on the three edited packages
  together AND on `./pkg/graph/` alone at `--concurrency 1`. The one lint rule
  this change had to satisfy is `unparam`, which is why `NewScope` dropped its
  always-nil `error` result (design D2).
- `make test` — includes `./internal/integration/`, so it inherits the first
  row's constraint.

**To finish verification**, run on a machine with more memory (or in CI):

```bash
go vet ./internal/integration/
go test ./internal/integration/ -run 'TestGraphSuite|TestRED' -count=1   # needs Docker
make lint
make test
make check-docs
```
