package promql

// Metrics records upstream PromQL query observations. It is intentionally tiny
// so an embedding application can supply its own recorder (or none). A nil
// Metrics passed to New is treated as a no-op — no upstream-query self-metrics
// are emitted. The concrete kube-state-graph implementation lives in
// internal/observability and satisfies this interface structurally.
type Metrics interface {
	// ObserveQueryDuration records a query's wall-clock duration in seconds,
	// labelled by query name.
	ObserveQueryDuration(name string, seconds float64)
	// IncQueryFailure increments the failure counter for the named query.
	IncQueryFailure(name string)
}
