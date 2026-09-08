## 1. Dispatch core extraction (design D2)

- [x] 1.1 Split `fanoutQuerier.Instant` in `pkg/promql/fanout.go` into a family-resolution head and an `issue(ctx, fam, name, query, ts)` core carrying selection, fan-out, merge, the optional-family Debug / unmatched-zone Warn split and the fail-closed error; verify `go test ./pkg/promql/ -count=1 -race` passes with no test edits, proving the extraction is behaviour-free.
- [x] 1.2 Verify the unclassified-query-name error is still produced by the head (a `Querier.Instant` call with an unregistered name errors); add a test asserting it if none pins it today.
- [x] 1.3 Run `make test` to confirm the graph goldens and the routing suite are byte-identical after the extraction.

## 2. Per-family zone-matcher bit (design D3)

- [x] 2.1 Derive `familyRendersAZ` (every query in the family carries `dimAZ`, excluding `dimAZRoute`) at package init beside `familyAcceptsAZ` in `pkg/promql/queries.go`, exposed as a `Family` method; verify a table test asserts `ksm` / `kubelet` / `alerts` route AND render, `harvest` routes without rendering, and `servicegraph` / `probe` do neither.
- [x] 2.2 Extend `TestFamilyAcceptsAZ_HomogeneousWithinFamily` (or add its sibling) so a family whose queries disagree on `dimAZ` fails the build; verify by temporarily flipping one query's dims locally and seeing the test fail.

## 3. Request type and validation (specs: request shape, input validation; design D10, D12)

- [x] 3.1 Add `pkg/promql/labelquery.go` with the `LabelQuery` request type (metric, family, optional single `AZ`, label filter map, required `At`, optional `Window`, optional `Limit`, `LabelKeys`) and the exported default-limit and max-value-length constants; verify the package compiles and `go vet ./pkg/promql/` is clean.
- [x] 3.2 Implement family validation via `ParseFamily`; verify an unknown family errors with no upstream call.
- [x] 3.3 Implement metric-name and label-key grammar validation, including the explicit `__name__`-as-filter-key rejection; verify unit tests cover a valid name, a name containing `}`, a key with a dash, and the reserved key.
- [x] 3.4 Implement value validation — control characters, invalid UTF-8, length cap — for the `AZ` value and every filter value; verify unit tests cover a newline in `AZ`, a lone surrogate half, a raw `0xff` byte, and an over-long value, each rejected before any upstream call.
- [x] 3.5 Implement the required-instant rule (zero `At` is a request error, never defaulted to now); verify a unit test asserts the error and that a mock querier records no call.
- [x] 3.6 Implement the `az`-key conflict rule: reject a filter whose key equals the configured `az` label key only when `AZ` is also set; verify unit tests cover the rejected combination (with `LabelKeys` bound to a non-default key) and the accepted one with `AZ` empty.

## 4. Zone rules (specs: zone applies the family's own zone contract; design D4)

- [x] 4.1 Reject `AZ` on `servicegraph` / `probe` with an error naming the family and the filter escape hatch; verify unit tests cover both families.
- [x] 4.2 Accept `AZ` on `harvest` as routing only, rendering no matcher; verify a unit test asserts the rendered string carries no `az` matcher while backend selection narrows to the zone's Harvest backends.
- [x] 4.3 Render the `az` matcher for `ksm` / `kubelet` / `alerts` using the caller's `LabelKeys`; verify a unit test with the key bound to `zone` asserts the rendered matcher uses that name.
- [x] 4.4 Verify via a unit test that an empty `AZ` applies no selection restriction and renders no matcher for every family, including `harvest`.

## 5. Query rendering (specs: exact matches only; design D6)

- [x] 5.1 Render `<metric>{...}` at the instant and `last_over_time(<metric>{...}[w])` when a window is set, with matcher order fixed as the `az` matcher (when rendered) then filters sorted by key; verify a unit test issues the same request with two different map iteration orders and asserts one identical string.
- [x] 5.2 Render every value through the existing string-literal escaper and pin that a value containing `"` or `\` cannot introduce a second matcher; verify a unit test asserts the exact rendered string for `shop",other="x`.
- [x] 5.3 Render an empty filter value verbatim as `key=""` rather than dropping it; verify a unit test asserts the matcher is present.
- [x] 5.4 Render a filterless, zoneless request as the bare metric selector; verify a unit test asserts no empty braces are emitted.

## 6. Router method and result assembly (specs: caller-declared family selects the backends, result contract, size bound; design D1, D7, D8)

- [x] 6.1 Add `Router.QueryLabels(ctx, LabelQuery) ([]map[string]string, error)` dispatching through the D2 core with the caller-declared family and the single zone; verify a unit test over a fake multi-backend table asserts the selected backends for a zoned and for a zoneless query.
- [x] 6.2 Convert the merged vector to label maps, stripping the reserved metric-name label and copying every other label verbatim; verify a unit test asserts `__name__` is absent and all other labels present, with no sample value anywhere in the result.
- [x] 6.3 Sort the result deterministically as a pure function of the label sets; verify a unit test that answers two backends in opposite orders yields an element-for-element identical result.
- [x] 6.4 Enforce the result bound against the merged, de-duplicated count and error naming the bound and the observed count; verify unit tests cover an over-bound failure, a caller-raised bound succeeding, and that a series held by two backends consumes one unit of budget.
- [x] 6.5 Confirm de-duplication comes from the existing merge (a series on a catch-all and a zone backend appears once); verify a unit test asserts a single result element.

## 7. Degradation and observability (specs: degradation, self-metric cardinality; design D9, D11)

- [x] 7.1 Before dispatch, check `Table.Select(fam, nil)` on the same snapshot and fail with an error naming the family when it is empty; verify a unit test over a table without `alerts` asserts the error and that no client is called.
- [x] 7.2 Verify via unit tests that a served-but-unmatched zone returns empty with a Warn naming the family and the zone, and that a backend error fails the call naming that backend — both inherited from the D2 core, neither reimplemented.
- [x] 7.3 Add the exported fixed query-name constant and pass it as the `name` argument for every label query; verify a unit test with a recording `Metrics` asserts that a thousand distinct metric names produce exactly one observed query-name label value.

## 8. Documentation and change closure

- [x] 8.1 Add an embedder section to `docs/upstream-backend-routing.md` covering the request shape (family required, `az` optional, everything else a filter), the per-family zone table, the `=`-only rule, the `az`-key conflict rule, the label-set result, the bound and the unserved-family error; verify the documented example compiles as a test or is copied from one.
- [x] 8.2 Update `CLAUDE.md`'s upstream-backend-routing bullet with the caller-declared-family path in one or two sentences; verify the wording matches the shipped behaviour, not the proposal.
- [x] 8.3 Run `make test`, `make vet`, `make lint` and `make vuln` clean.
- [x] 8.4 Run `openspec validate "add-routed-metrics-query-helper" --strict` and `openspec verify "add-routed-metrics-query-helper"` clean.
