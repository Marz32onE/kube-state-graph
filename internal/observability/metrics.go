package observability

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the kube_state_graph_* Prometheus metrics.
type Metrics struct {
	Registry *prometheus.Registry

	BuildDuration       *prometheus.HistogramVec
	ProjectDuration     prometheus.Histogram
	SerialiseDuration   *prometheus.HistogramVec
	CacheHits           *prometheus.CounterVec
	CacheMisses         *prometheus.CounterVec
	CacheSizeEntries    prometheus.Gauge
	CacheCostBytes      prometheus.Gauge
	CacheEvictions      *prometheus.CounterVec
	CacheRejected       prometheus.Counter
	SingleflightDedup   prometheus.Counter
	BuildConcurrency    prometheus.Gauge
	BuildRejected       *prometheus.CounterVec
	GraphNodeCount      *prometheus.GaugeVec
	GraphEdgeCount      *prometheus.GaugeVec
	ClustersObserved    prometheus.Gauge
	UpstreamQueryDur    *prometheus.HistogramVec
	UpstreamQueryFail   *prometheus.CounterVec
	HTTPRequests        *prometheus.CounterVec
}

// NewMetrics registers and returns a fresh Metrics bundle.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		BuildDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kube_state_graph_build_duration_seconds",
			Help:    "Time to build (or hit cache for) a multi-cluster graph snapshot.",
			Buckets: prometheus.DefBuckets,
		}, []string{"cache_status"}),
		ProjectDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "kube_state_graph_project_duration_seconds",
			Help:    "Time spent applying filters and traversal over a cached graph.",
			Buckets: []float64{0.0001, 0.001, 0.01, 0.1, 1},
		}),
		SerialiseDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kube_state_graph_serialise_duration_seconds",
			Help:    "Time spent encoding the response and computing the ETag.",
			Buckets: []float64{0.0001, 0.001, 0.01, 0.1, 1},
		}, []string{"format"}),
		CacheHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kube_state_graph_cache_hits_total",
			Help: "Cache hits per layer.",
		}, []string{"layer"}),
		CacheMisses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kube_state_graph_cache_misses_total",
			Help: "Cache misses per layer.",
		}, []string{"layer"}),
		CacheSizeEntries: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kube_state_graph_cache_size_entries",
			Help: "Number of entries currently in the in-process cache.",
		}),
		CacheCostBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kube_state_graph_cache_cost_bytes",
			Help: "Approximate cache memory cost in bytes.",
		}),
		CacheEvictions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kube_state_graph_cache_evictions_total",
			Help: "Cache evictions by reason.",
		}, []string{"reason"}),
		CacheRejected: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kube_state_graph_cache_rejected_total",
			Help: "Cache Set calls rejected by the admission policy.",
		}),
		SingleflightDedup: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kube_state_graph_singleflight_dedup_total",
			Help: "Number of duplicate concurrent build requests coalesced by singleflight.",
		}),
		BuildConcurrency: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kube_state_graph_build_concurrency",
			Help: "Number of in-flight builds.",
		}),
		BuildRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kube_state_graph_build_rejected_total",
			Help: "Builds rejected by reason.",
		}, []string{"reason"}),
		GraphNodeCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kube_state_graph_graph_node_count",
			Help: "Node count of the most recently built graph (per cluster, per kind).",
		}, []string{"cluster", "kind"}),
		GraphEdgeCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kube_state_graph_graph_edge_count",
			Help: "Edge count of the most recently built graph (per type, per cross_cluster).",
		}, []string{"type", "cross_cluster"}),
		ClustersObserved: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kube_state_graph_clusters_observed",
			Help: "Number of distinct cluster label values observed in the most recent build.",
		}),
		UpstreamQueryDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kube_state_graph_upstream_query_duration_seconds",
			Help:    "Upstream PromQL query duration.",
			Buckets: prometheus.DefBuckets,
		}, []string{"query"}),
		UpstreamQueryFail: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kube_state_graph_upstream_query_failures_total",
			Help: "Upstream PromQL query failures by query name.",
		}, []string{"query"}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kube_state_graph_http_requests_total",
			Help: "HTTP requests by path and status.",
		}, []string{"path", "status"}),
	}

	reg.MustRegister(
		m.BuildDuration,
		m.ProjectDuration,
		m.SerialiseDuration,
		m.CacheHits,
		m.CacheMisses,
		m.CacheSizeEntries,
		m.CacheCostBytes,
		m.CacheEvictions,
		m.CacheRejected,
		m.SingleflightDedup,
		m.BuildConcurrency,
		m.BuildRejected,
		m.GraphNodeCount,
		m.GraphEdgeCount,
		m.ClustersObserved,
		m.UpstreamQueryDur,
		m.UpstreamQueryFail,
		m.HTTPRequests,
	)
	return m
}
