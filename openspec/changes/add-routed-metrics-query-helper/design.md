## Context

See proposal.md — Why. The constraints that shape the approach:

- `fanoutQuerier.Instant` (`pkg/promql/fanout.go`) already owns backend
  selection, the identical-string fan-out, the fingerprint-deduplicating merge,
  the optional-family Debug / unmatched-zone Warn split, and the fail-closed
  error naming the backend. All of it keys off `FamilyOf(Query(name))`, which is
  a lookup into a closed table.
- The per-backend clients live only inside `routerState`, reachable from a
  `*Router`. Nothing else in the package can address a backend.
- `Render` (`pkg/promql/queries.go`) is a `switch` over `Query` constants. It
  cannot express a metric it does not know, and its request matchers come from
  `Selector.render(queryDims[q], keys)` — a per-QUERY dimension mask.
- `Family.AcceptsAZ` is already **derived** from `queryDims` at package init
  rather than restated: a family is zone-routed iff every query in it carries
  `dimAZ` (matcher and route) or `dimAZRoute` (route only — Harvest). That
  derivation pattern is the precedent this change extends by one bit.
- `pkg/promql` has no clock, no config dependency, and no file I/O. It must stay
  that way — `pkg/` cannot import `internal/*`, and the package is imported by
  external modules.

## Goals / Non-Goals

**Goals:**

- One routed call for an arbitrary metric that cannot silently return the wrong
  data: no dropped filter, no partial fan-out, no truncated result, no unserved
  family answered as "empty".
- Reuse the existing dispatch machinery rather than parallel it, so a future
  change to zone selection or merge semantics moves both paths at once.
- Keep caller-supplied strings from reaching a query string unvalidated.
- Add nothing to the HTTP surface, change no existing signature, touch no
  configuration.

**Non-Goals:**

- Sample values, range vectors, or any aggregation. This returns label sets.
- A `pkg/kubegraph` facade method. The facade's job is the graph request
  contract; this is a query-layer utility and belongs beside the router.
- Multi-zone fan-in. Explicitly one zone per call.
- Named `env` / `cluster` / `namespace` dimensions. They are label filters
  (see D5).
- Operator-defined families, or a family reserved for embedders. The six
  families are the store map; the helper reads it, it does not extend it.
- A generic PromQL escape hatch. `=` only, one metric, no expression input.
- Exposure through `Querier` or `QuerierSource`. Widening either would break
  every mock and embedder for a feature neither needs.

## Decisions

### D1 — A method on `*Router`, not a new interface

The helper needs the per-backend clients, which only a `*Router` holds. It is a
concrete method (`Router.QueryLabels`) rather than a fifth optional upgrade
interface.

*Alternatives:* (a) a `LabelQuerier` interface type-asserted like `Prober` /
`RouterMetrics` — rejected because nothing in the repository consumes it, so the
interface would exist purely to be asserted by an embedder who already holds the
concrete `*Router`; (b) a free function taking `*Table` plus a client factory —
rejected because it would construct its own clients, bypassing connection reuse
and the reload-survives-identity rule.

A single-upstream embedder reaches it the documented way: `SingleBackendTable` →
`NewRouter`. That is already the compatibility path the whole test suite runs on.

### D2 — Extract the dispatch core; the caller-declared family is a parameter

`fanoutQuerier.Instant` splits into two: a family-resolution head (unchanged
behaviour, unchanged error for an unclassified query name) and an `issue(ctx,
fam, name, query, ts)` core carrying selection, fan-out, merge, the two
no-backend outcomes and fail-closed. `QueryLabels` calls the core with the
caller's family.

This is what makes the `upstream-backend-routing` delta a clarification rather
than a second policy: there is literally one implementation of dispatch, and the
only difference between the two entry points is where the family came from.

*Alternative:* a synthetic `Query` constant registered in `queryFamily` per call
— rejected outright; `queryFamily` is a compile-time-exhaustive table and
mutating it at runtime would break the exhaustiveness guarantee and the
determinism of `Family.AcceptsAZ`.

### D3 — One more derived per-family bit, from the same `queryDims` table

`familyAcceptsAZ` (route) is derived at init. This change derives one sibling
the same way — "every query in this family renders an `az` matcher" (`dimAZ`
alone, so Harvest's routing-only `dimAZRoute` is excluded). The helper reads the
pair to decide what a supplied zone does:

| Family | routes by `az` | renders `az` matcher |
|---|---|---|
| `ksm`, `kubelet`, `alerts` | yes | yes |
| `harvest` | yes | no |
| `servicegraph`, `probe` | no | no |

Deriving rather than restating is the whole point: an arbitrary metric has no
`queryDims` entry, so the family's own bits are the only honest statement of
what that store's series are labelled with. Restating them as a hand-written
table would be a second contract that could drift from the first. The existing
homogeneity test is extended to the new bit so a family whose queries disagree
fails the build rather than resolving quietly.

### D4 — A zone the family cannot route by is an ERROR, not a silent drop

Supplying `az` on `servicegraph` / `probe` fails the request. The graph build's
precedent is the opposite — `queryDims` silently narrows only what a query
accepts — but that is a *closed* set of queries whose behaviour is specified and
tested per query. Here the caller chose the metric, so "your zone was ignored" is
unfalsifiable from the outside: the result looks exactly like a correct answer.

`az` on `harvest` is accepted and honoured **as routing only** — that is the
documented family semantic (the per-zone store boundary *is* the filter), and it
is not silent because the request still narrows which store answers.

The escape hatch costs nothing: `az` is an ordinary label name, so a caller who
really wants to match it literally on a non-routed family leaves the field empty
and passes it in the filter map, where it is rendered with no family rule
attached (D6 governs the conflict case).

### D5 — `az` is the only named dimension; everything else is a filter

`az` earns a field because it does something a matcher cannot: it decides which
store is asked. `env`, `cluster` and `namespace` never route — in the graph
build they only ever become matchers — so a named field for them would buy
exactly one thing: applying the family's per-query dimension rules to them
(reject `env` on Harvest, reject `cluster` on `alerts`). That protection was
considered and rejected: those rules describe the labels of the **build's own
queries**, not of an arbitrary metric the caller named, and the caller is the
only party who knows what that metric carries. Rendering a filter literally is
the honest behaviour; a rule that "knows better" about a metric it has never
seen is not.

The request type is correspondingly small, and `LabelKeys` is needed only for
the `az` key.

*Alternative:* mirror the graph build's `Selector` (all four dimensions) —
rejected as above, and because it would import the build's `cluster=unknown`
alternation (a `=~` matcher the `=`-only rule forbids) for a bucket the helper
does not implement.

### D6 — Rendering: bare instant selector, optional lookback, `=` only, fixed order

The query is `<metric>{<matchers>}` at the instant, or
`last_over_time(<same>[<window>])` when a window is given — the shape every
existing query already uses, so a caller reading a historical instant gets the
same semantics the graph build gets.

Matcher order is fixed and deterministic: the family-derived `az` matcher (when
rendered), then the caller's label filters sorted by key. Two requests differing
only in map iteration order therefore render the identical string, which is what
makes the result independent of Go's map ordering and keeps the Debug-logged
query stable.

A filter whose key is the configured `az` label key is rejected **only when the
`az` field is also set**: two matchers on one label either duplicate or
contradict each other while looking like a valid request. With the field empty
the filter is the D4 escape hatch and is rendered as any other.

Only `=` is offered. That is not a scope cut — it is the security property: with
no `=~` form, no caller value is ever compiled as a regular expression, so
`regexp.QuoteMeta` is not part of the trust chain and a value can only ever
match itself.

### D7 — The reserved metric-name label is stripped from the result

Whether `__name__` survives depends on how the expression was evaluated (a bare
selector keeps it; a rolling function's handling varies by engine), so returning
it would make the result shape depend on whether the caller passed a window.
Stripping it is store-independent and loses nothing — the caller named the
metric.

*Alternative:* re-stamp `__name__` from the request when absent — rejected as
inventing data the upstream did not return.

### D8 — Over-limit is an error, never a truncation

Same reasoning as D6 of the routing capability: a truncated list is
indistinguishable from a narrower estate. The bound is per-request with a package
default, and exceeding it names both the bound and the observed count so the
caller can decide between narrowing and raising.

The count compared against the bound is the **merged, de-duplicated** count, so a
series held by three backends does not consume three units of budget.

*Alternative:* stream / paginate — rejected as far beyond the ask; a caller
needing that volume wants a different tool.

### D9 — An unserved family is an ERROR for the helper, not a Debug-empty

The dispatch core's "optional family served by no backend → empty vector at
Debug" outcome is right for the server's `alerts` leg: the build issues it
unconditionally, silence is the documented normal state, and a Warn on every
build would train the operator to ignore the level.

The helper is different in kind: it is invoked deliberately, per call, by code
that named the family. Returning empty would make "no backend serves `alerts`"
indistinguishable from "the estate holds no such series" — the exact ambiguity
this codebase treats as a defect. `QueryLabels` therefore checks
`Table.Select(fam, nil)` before dispatch and fails with an error naming the
family when it is empty. The check reads the same table snapshot the dispatch
uses, so a reload cannot make one call see two tables. For the five required
families the check is structurally satisfied — `NewTable` rejects a table that
leaves one unserved — so in practice it fires only for `alerts`.

The **unmatched-zone** outcome (served, but not for this zone) is deliberately
kept as empty-plus-Warn: that is the routing capability's rule for every
zone-routed family, an empty filtered result is legitimate, and the Warn already
names the family and the zone.

### D10 — Validation gates, escaping backstops

Two layers, in this order:

1. **Reject** — family against `ParseFamily`; metric name against the
   metric-name grammar; filter keys against the label-name grammar (plus an
   explicit `__name__` rejection so the metric is named in exactly one place,
   and the D6 conflict rule); and every value — `az` and the filters — scanned
   for control characters and invalid UTF-8 with a documented length cap.
2. **Escape** — accepted values pass through the existing string-literal escaper
   (backslash and double quote) at render time.

Order matters: escaping alone would let a control character through into a query
string, and validation alone would still be defeated by a legal-but-quoted value.
The invalid-UTF-8 check mirrors `kubegraph.ParseValues`, which added it because a
bad byte decodes to U+FFFD and passes a naive control-character scan.

Rejection happens before any upstream call, so a malformed request costs nothing
and cannot appear in an upstream log.

### D11 — One fixed self-metric query name

`Client.Instant` labels `kube_state_graph_upstream_query_duration_seconds` and
`..._failures_total` by the `name` argument, and stamps it on the span. Passing
the caller's metric name would make an unbounded label set out of a bounded one.
A single exported constant is passed instead; the caller's metric name still
reaches the Debug log and the span's `db.statement`, which are not series.

Per-backend attribution is unchanged — it already flows through
`kube_state_graph_upstream_backend_query_failures_total{backend}`.

### D12 — The evaluation instant is required, not defaulted

`pkg/promql` holds no clock and must not acquire one (the repository injects
`clock.Clock` everywhere precisely so time is not read implicitly). A zero
instant is a caller bug, and defaulting it to `time.Now()` would answer a
different question than the caller asked while looking correct.

## Risks / Trade-offs

- **An arbitrary metric name plus caller-supplied values is a new injection
  surface — the first in this codebase.** → D10's two layers, both specified as
  requirements with their own scenarios rather than left to the implementation;
  tests must cover a quote, a brace, a newline, an invalid UTF-8 byte and an
  over-long value, in a filter value and in the `az` value alike.
- **A caller can issue a high-cardinality query and load the upstream.** → D8
  bounds what is returned, but not what upstream scanned. Upstream search limits
  (`-search.maxUniqueTimeseries`) remain the real ceiling, and the fail-closed
  rule turns a limit breach into a named error rather than a partial answer.
- **The family bit derived in D3 changes meaning if `queryDims` changes.** → It
  is derived, not restated, so it moves together with the matcher table by
  construction; the existing homogeneity test is extended to it so a family
  whose queries disagree fails the build rather than resolving quietly.
- **D4's reject-don't-drop rule will surprise a caller who expects the graph
  request's permissive behaviour.** → The error names the family and points at
  the filter escape hatch; the asymmetry is specified, not incidental.
- **D5 gives `env` / `cluster` filters no family protection.** → A caller
  filtering `cluster=c1` on `harvest` matches the ONTAP cluster label, not a
  Kubernetes one — exactly what was asked, rendered literally. The docs state
  which label each family's stores carry, and nothing is silently rewritten.
- **D9's error-on-unserved rule differs from the server's optional-leg
  Debug.** → The asymmetry is specified with its reasoning, and the two outcomes
  are reached through different entry points, so neither can leak into the
  other.
- **Extracting the dispatch core touches the hot path every build runs
  through.** → It is a pure extraction with no behaviour change; the existing
  routing tests (which run in the single-backend degenerate table on every
  package's suite) are the regression net, and the graph goldens pin the output
  byte-for-byte.
- **Consumers may read the helper as a general PromQL gateway and ask for
  `=~`, aggregation, range results, or named dimensions.** → Non-Goals says no;
  the `=`-only rule is load-bearing for D6's security property, so widening it
  is a new change with its own design, not an increment.

## Migration Plan

Purely additive: new exported symbols in `pkg/promql`, one internal extraction,
no signature change, no configuration change, no HTTP change. Nothing to migrate
and nothing to roll back beyond reverting the commit — a deployment that never
calls the helper cannot observe it.

## Open Questions

- The default result bound's exact value. It is a documented package constant
  with no downstream contract, so moving it later changes no spec, no API shape
  and no task; a first value can be set from what a realistic label-set page
  costs and revised on feedback.
- The maximum accepted value length. Same shape of question — the requirement
  fixes that a documented cap exists and is enforced, not its numeric value.
