package promql

import (
	"fmt"
	"time"
)

// Query identifies one of the PromQL templates the build pipeline issues.
// The name is also used as the `query` label on self-metrics.
//
// The constant values are the bare upstream metric names (kube-state-metrics
// naming). At render time a configurable prefix may be prepended via
// Renderer; the Query string itself is not rewritten so that self-metric and
// span dimensions stay stable across deployments that differ only by prefix.
type Query string

const (
	QPodInfo           Query = "kube_pod_info"
	QNodeInfo          Query = "kube_node_info"
	QNodeAddresses     Query = "kube_node_status_addresses"
	QPVCBindings       Query = "kube_pod_spec_volumes_persistentvolumeclaims_info"
	QNodeLabels        Query = "kube_node_labels"
	QServiceGraphTotal Query = "traces_service_graph_request_total"
	QClusterDiscovery  Query = "cluster_discovery"
	QUpProbe           Query = "up"

	// Service / endpointslice topology (D29 connection-string resolution).
	// KSM-shaped, so prefix-aware via Renderer.
	QServiceInfo            Query = "kube_service_info"
	QEndpointSliceEndpoints Query = "kube_endpointslice_endpoints"
	QEndpointSliceLabels    Query = "kube_endpointslice_labels"
)

// ClusterDiscoveryLookback is the fixed lookback used by /v1/clusters
// discovery. Sized to absorb transient KSM scrape gaps; not configurable.
const ClusterDiscoveryLookback = time.Hour

// Renderer renders Query templates to PromQL strings, optionally prepending
// Prefix to every kube-state-metrics-shaped metric name. The prefix is
// additive — it is not applied to the service-graph metric
// (`traces_service_graph_request_total`, produced by a different exporter
// family) or to the Prometheus-native readiness probe (`up`).
//
// Zero value (`Renderer{}`) preserves stock kube-state-metrics behaviour.
type Renderer struct {
	Prefix string
}

// Render returns the PromQL string for the named query, parameterised by
// `window` (the bucketed end-start) and prefixed per r.Prefix where
// applicable.
func (r Renderer) Render(q Query, window time.Duration) string {
	w := FormatDuration(window)

	switch q {
	case QPodInfo:
		return fmt.Sprintf(`last_over_time(%skube_pod_info[%s])`, r.Prefix, w)
	case QNodeInfo:
		return fmt.Sprintf(`last_over_time(%skube_node_info[%s])`, r.Prefix, w)
	case QNodeAddresses:
		// External IP only; topology reader filters further if needed.
		return fmt.Sprintf(`last_over_time(%skube_node_status_addresses{type="ExternalIP"}[%s])`, r.Prefix, w)
	case QPVCBindings:
		return fmt.Sprintf(`last_over_time(%skube_pod_spec_volumes_persistentvolumeclaims_info[%s])`, r.Prefix, w)
	case QNodeLabels:
		return fmt.Sprintf(`last_over_time(%skube_node_labels[%s])`, r.Prefix, w)
	case QServiceInfo:
		return fmt.Sprintf(`last_over_time(%skube_service_info[%s])`, r.Prefix, w)
	case QEndpointSliceEndpoints:
		return fmt.Sprintf(`last_over_time(%skube_endpointslice_endpoints[%s])`, r.Prefix, w)
	case QEndpointSliceLabels:
		return fmt.Sprintf(`last_over_time(%skube_endpointslice_labels[%s])`, r.Prefix, w)
	case QServiceGraphTotal:
		// Service-graph metrics come from Alloy/Tempo, not kube-state-metrics;
		// the configurable prefix deliberately does NOT apply here. The metric
		// carries a single `cluster` label representing the trace source
		// (client-side) cluster; server-side cluster is recovered at build time
		// via the topology pod-UID index, not via PromQL.
		return fmt.Sprintf(`rate(traces_service_graph_request_total[%s])`, w)
	case QClusterDiscovery:
		return fmt.Sprintf(`group by (cluster) (last_over_time(%skube_node_info[%s]))`, r.Prefix, w)
	case QUpProbe:
		// Prometheus-native; the configurable prefix does not apply.
		return `up`
	}
	return ""
}

// Render returns the PromQL string for the named query using a zero-prefix
// Renderer. Retained for tests and existing callers that do not need a
// configurable prefix; production code paths go through a Renderer held by
// build.Builder / api.Server.
func Render(q Query, window time.Duration) string {
	return Renderer{}.Render(q, window)
}
