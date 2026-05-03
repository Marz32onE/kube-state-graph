package promql

import (
	"fmt"
	"strings"
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
	QClusterSizeProbe  Query = "cluster_size_probe"
	QUpProbe           Query = "up"
)

// Render returns the PromQL string for the named query, parameterised by
// `window` (the bucketed end-start), and an optional cluster allowlist regex
// fragment (`""` ⇒ no filtering).
func Render(q Query, window time.Duration, allowlistRegex string) string {
	w := FormatDuration(window)
	clusterSel := ""
	if allowlistRegex != "" {
		clusterSel = fmt.Sprintf(`{cluster=~"%s"}`, allowlistRegex)
	}

	switch q {
	case QPodInfo:
		return fmt.Sprintf(`last_over_time(kube_pod_info%s[%s])`, clusterSel, w)
	case QNodeInfo:
		return fmt.Sprintf(`last_over_time(kube_node_info%s[%s])`, clusterSel, w)
	case QNodeAddresses:
		// External IP only; topology reader filters further if needed.
		sel := injectLabel(clusterSel, `type="ExternalIP"`)
		return fmt.Sprintf(`last_over_time(kube_node_status_addresses%s[%s])`, sel, w)
	case QPVCBindings:
		return fmt.Sprintf(`last_over_time(kube_pod_spec_volumes_persistentvolumeclaims_info%s[%s])`, clusterSel, w)
	case QNodeLabels:
		return fmt.Sprintf(`last_over_time(kube_node_labels%s[%s])`, clusterSel, w)
	case QServiceGraphTotal:
		// Service-graph metrics carry a single `cluster` label representing the
		// trace source (client-side) cluster. Server-side cluster is recovered
		// at build time via the topology pod-UID index, not via PromQL.
		return fmt.Sprintf(`rate(traces_service_graph_request_total%s[%s])`, clusterSel, w)
	case QClusterDiscovery:
		return fmt.Sprintf(`group by (cluster) (last_over_time(kube_node_info%s[%s]))`, clusterSel, w)
	case QClusterSizeProbe:
		return fmt.Sprintf(`count(kube_pod_info%s)`, clusterSel)
	case QUpProbe:
		return `up`
	}
	return ""
}

// injectLabel splices an additional label matcher into an existing
// `{cluster=~"..."}` selector or starts a fresh one.
func injectLabel(existing, addition string) string {
	if existing == "" {
		return "{" + addition + "}"
	}
	// existing looks like `{cluster=~"a|b|c"}`. Insert before the closing brace.
	return strings.TrimSuffix(existing, "}") + "," + addition + "}"
}
