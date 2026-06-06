package build

// Metrics records last-build observational gauges. It is intentionally tiny so
// an embedding application can supply its own recorder (or none). A nil Metrics
// passed to New is treated as a no-op — no graph-size self-metrics are emitted.
// The concrete kube-state-graph implementation lives in internal/observability
// and satisfies this interface structurally.
type Metrics interface {
	// SetGraphNodeCounts replaces the last-build node-count gauge, keyed by
	// [cluster, kind].
	SetGraphNodeCounts(counts map[[2]string]int)
	// SetGraphEdgeCounts replaces the last-build edge-count gauge, keyed by
	// [type, cross_cluster].
	SetGraphEdgeCounts(counts map[[2]string]int)
	// SetClustersObserved records the distinct cluster count from the build.
	SetClustersObserved(n int)
}
