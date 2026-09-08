## Why

An embedder linking `pkg/promql` can reach a routed upstream only through the
graph engine's own fixed query set. `Router.QuerierFor(sel).Instant` resolves a
query's family from the hardcoded `queryFamily` table and **fails** on any name
that is not one of the ~45 `promql.Query` constants, so a consumer that wants a
different series — a metric this repository never queries, or one belonging to
the operator's own estate — has no routed path to it at all. The only escape is
to bypass routing entirely: read `Table.Select`, construct a `*Client` per
backend by hand, and re-implement the zone rules, the credential wiring, the
fan-out and the duplicate-series merge that `fanoutQuerier` already owns.

That is exactly the code the routing capability exists to prevent a consumer
from writing, and getting it subtly wrong (an undeduplicated merge, a missed
catch-all backend) produces a plausible, wrong answer rather than an error.

## What Changes

- **NEW** an embedder-facing label-query helper on `*promql.Router`: given one
  metric name, one query family, an optional single availability zone and a
  set of exact label equalities, it selects the backends serving that family
  (narrowed to the zone when one is given), issues one identical query to
  each, merges and de-duplicates the results, and returns the matched series'
  **label sets** — no sample values.
- The **family is supplied by the caller**, not derived from a query-name
  table, and is validated against the six declared families. This is what lets
  the helper carry an arbitrary metric name while still reaching the right
  store; the six families and their zone-routing rules are unchanged, and the
  routing file is untouched.
- **`az` is optional and single-valued.** When given it applies the family's
  existing zone contract: `ksm` / `kubelet` / `alerts` are routed to the zone's
  backends AND carry the `az` matcher; `harvest` is routed with no matcher;
  `servicegraph` / `probe` are not zone-routed, so a zone on them is a request
  error rather than a silently ignored value. When absent, the query reaches
  **every** backend serving the family and the results are merged. A caller
  wanting two zones issues two calls — the graph build's multi-value OR
  semantics deliberately do not apply here.
- **Every other label — `env`, `cluster`, `namespace`, anything — is an
  ordinary exact-match filter.** `az` is the one named dimension because it is
  the one that decides WHICH store is asked, a choice no matcher can make; a
  filter only decides what the chosen store returns, and the caller naming an
  arbitrary metric is the only party who knows its labels.
- The **metric name is arbitrary**, so the request is untrusted input that ends
  up inside a PromQL string. The helper validates the metric name and every
  label key against the PromQL grammar, and rejects control characters and
  invalid UTF-8 in every value — `az` and the filters alike (the rule
  `kubegraph.ParseValues` already applies to request-scoped selector values) —
  before any value is escaped into a string literal.
- **Only `=` is supported** on label filters. No `!=`, no `=~`, no `!~` — one
  operator keeps the rendered query a pure function of a `map[string]string`
  and leaves no way to smuggle a regular expression through a caller-supplied
  value. A filter on the configured `az` label key is rejected only when the
  `az` field is also set (two matchers on one label duplicate or contradict);
  with the field empty it is allowed, which is how a caller matches the `az`
  label literally on a family that does not route by it.
- A family **served by no backend** in the live table is an **error** for the
  helper, not the empty-and-Debug outcome the server applies to its own
  optional `alerts` leg. The server issues that leg unconditionally on every
  build, so silence is its normal state; the helper is invoked deliberately,
  and an unserved family is a configuration gap the caller must see. A zone no
  backend covers keeps the routing capability's empty-plus-Warn outcome.
- The helper records its upstream latency and failures under one **fixed**
  query name, never the caller's metric name — an arbitrary name on the
  existing `query`-labelled self-metrics would be an unbounded-cardinality
  hole.
- No new HTTP route, no change to `/v1/graph` or `/v1/storage-graph`, no new
  dependency, no configuration change, and no change to any existing
  signature. Not breaking.

**Assumption to confirm at review:** "return a key-value map" is read as **one
`map[string]string` per matched series** (that series' labels), returned as a
deterministically ordered, de-duplicated slice. The alternative reading — one
map of `label key → distinct values across all matched series` — collapses the
per-series association and is not proposed; say so and the specs change shape.

## Capabilities

### New Capabilities

- `metrics-label-query`: the routed, family-scoped label query an embedder
  issues for an arbitrary metric name — its request shape (family required,
  `az` optional, everything else a filter), its validation and escaping rules,
  its `=`-only filter semantics, the label-set result contract, and its
  degradation behaviour (unserved family, unmatched zone, backend failure).

### Modified Capabilities

- `upstream-backend-routing`: the `Query family classification` requirement
  states the family mapping is hardcoded and exhaustive over the query set, and
  that an unclassified query is a build-time failure. That stays true for every
  query the **server** issues; the delta adds the caller-declared family path
  used by `metrics-label-query`, and pins that it reuses the existing zone
  selection, fan-out merge and fail-closed rules unchanged rather than
  introducing a second dispatch policy.

## Impact

- **Code**: new `pkg/promql/labelquery.go` (request type, validation,
  rendering, result assembly) plus its unit tests; one new method on
  `*promql.Router` in `pkg/promql/router.go`; one derived per-family bit beside
  `familyAcceptsAZ` in `pkg/promql/queries.go` (does the family render the
  `az` matcher, as opposed to merely routing by it). Reuses `Table.Select`,
  `mergeVectors`, `escapeLiteral` and the per-backend clients already held by
  `routerState` — no new I/O path, no new client construction.
- **API surface**: additive exports in `pkg/promql` only. `pkg/build`,
  `pkg/graph`, `pkg/kubegraph`, `pkg/cytoscape` and `internal/*` are untouched;
  `Querier` / `QuerierSource` keep their signatures, so every mock and embedder
  is unaffected.
- **Self-metrics**: one new fixed `query` label value on the existing
  `kube_state_graph_upstream_query_duration_seconds` / `..._failures_total`
  series; per-backend failures continue through
  `kube_state_graph_upstream_backend_query_failures_total{backend}`. No new
  metric name and no new label on an existing metric.
- **Security**: caller-supplied strings reach a PromQL query for the first time
  in this codebase. The validation and escaping rules are the load-bearing part
  of this change and are specified, not left to the implementation.
- **Docs**: `docs/upstream-backend-routing.md` gains the embedder section
  describing the helper.
- **Dependencies**: none added.
