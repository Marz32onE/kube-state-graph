package observability

// These methods let *Metrics satisfy the small, no-op-tolerant metrics
// interfaces declared by the reusable pkg/ packages (pkg/promql.Metrics and
// pkg/build.Metrics) without those packages importing internal/observability.
// The interfaces are structural, so only the method set must line up.

// ObserveQueryDuration records an upstream PromQL query duration in seconds,
// labelled by query name. Satisfies pkg/promql.Metrics.
func (m *Metrics) ObserveQueryDuration(name string, seconds float64) {
	m.UpstreamQueryDur.WithLabelValues(name).Observe(seconds)
}

// IncQueryFailure increments the upstream PromQL query failure counter for the
// named query. Satisfies pkg/promql.Metrics.
func (m *Metrics) IncQueryFailure(name string) {
	m.UpstreamQueryFail.WithLabelValues(name).Inc()
}

// SetGraphNodeCounts replaces the last-build node-count gauge with counts keyed
// by [cluster, kind]. Satisfies pkg/build.Metrics.
func (m *Metrics) SetGraphNodeCounts(counts map[[2]string]int) {
	m.GraphNodeCount.Reset()
	for k, c := range counts {
		m.GraphNodeCount.WithLabelValues(k[0], k[1]).Set(float64(c))
	}
}

// SetGraphEdgeCounts replaces the last-build edge-count gauge with counts keyed
// by [type, cross_cluster]. Satisfies pkg/build.Metrics.
func (m *Metrics) SetGraphEdgeCounts(counts map[[2]string]int) {
	m.GraphEdgeCount.Reset()
	for k, c := range counts {
		m.GraphEdgeCount.WithLabelValues(k[0], k[1]).Set(float64(c))
	}
}

// SetClustersObserved records the distinct cluster count from the most recent
// build. Satisfies pkg/build.Metrics.
func (m *Metrics) SetClustersObserved(n int) {
	m.ClustersObserved.Set(float64(n))
}
