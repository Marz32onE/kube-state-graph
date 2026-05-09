package promql

import (
	"fmt"
	"time"
)

// Query identifies one of the PromQL templates the build pipeline issues.
// The name is also used as the `query` label on self-metrics.
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
)

// ClusterDiscoveryLookback is the fixed lookback used by /v1/clusters
// discovery. Sized to absorb transient KSM scrape gaps; not configurable.
const ClusterDiscoveryLookback = time.Hour

// Render returns the PromQL string for the named query, parameterised by
// `window` (the bucketed end-start).
func Render(q Query, window time.Duration) string {
	w := FormatDuration(window)

	switch q {
	case QPodInfo:
		return fmt.Sprintf(`last_over_time(kube_pod_info[%s])`, w)
	case QNodeInfo:
		return fmt.Sprintf(`last_over_time(kube_node_info[%s])`, w)
	case QNodeAddresses:
		// External IP only; topology reader filters further if needed.
		return fmt.Sprintf(`last_over_time(kube_node_status_addresses{type="ExternalIP"}[%s])`, w)
	case QPVCBindings:
		return fmt.Sprintf(`last_over_time(kube_pod_spec_volumes_persistentvolumeclaims_info[%s])`, w)
	case QNodeLabels:
		return fmt.Sprintf(`last_over_time(kube_node_labels[%s])`, w)
	case QServiceGraphTotal:
		// Service-graph metrics carry a single `cluster` label representing the
		// trace source (client-side) cluster. Server-side cluster is recovered
		// at build time via the topology pod-UID index, not via PromQL.
		return fmt.Sprintf(`rate(traces_service_graph_request_total[%s])`, w)
	case QClusterDiscovery:
		return fmt.Sprintf(`group by (cluster) (last_over_time(kube_node_info[%s]))`, w)
	case QUpProbe:
		return `up`
	}
	return ""
}
