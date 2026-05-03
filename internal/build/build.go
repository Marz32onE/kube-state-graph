package build

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/observability"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

// Builder runs the topology + service-graph readers and assembles a
// multi-cluster Graph for one bucketed time window.
type Builder struct {
	q       *promql.Client
	cfg     config.Config
	metrics *observability.Metrics
}

// New constructs a Builder.
func New(q *promql.Client, cfg config.Config, m *observability.Metrics) *Builder {
	return &Builder{q: q, cfg: cfg, metrics: m}
}

// Build runs all upstream queries for [end - window, end] and returns the
// joined multi-cluster Graph.
func (b *Builder) Build(ctx context.Context, window time.Duration, end time.Time) (*graph.Graph, error) {
	allowlistRegex := promql.AllowlistRegex(b.cfg.ClustersAllowlist)

	// Cluster-too-large probe at the requested window's end, NOT time.Now() —
	// historical requests must be evaluated against historical cluster size.
	if err := b.probeClusterSize(ctx, allowlistRegex, end); err != nil {
		return nil, err
	}

	topology, err := ReadTopology(ctx, b.q, window, end, allowlistRegex)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewError(ReasonTimeout, "build timeout", err)
		}
		return nil, NewError(ReasonUpstream, "topology read failed", err)
	}

	// Outside-retention check: zero pods + healthy upstream ⇒ retention miss.
	if len(topology.Pods) == 0 && len(topology.Nodes) == 0 {
		if up, _ := b.upProbe(ctx); up {
			return nil, NewError(ReasonOutsideRetention, "no topology rows in window; upstream healthy", nil)
		}
	}

	sg, err := ReadServiceGraph(ctx, b.q, window, end, allowlistRegex, b.cfg.ExternalNamePattern, topology)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewError(ReasonTimeout, "build timeout", err)
		}
		return nil, NewError(ReasonUpstream, "service-graph read failed", err)
	}

	nodes, edges := assemble(topology, sg)
	g := graph.NewGraph(nodes, edges, time.Now().UTC())

	// Cross-cluster status is derived from the resolved endpoint nodes'
	// `cluster` labels, since edges only carry the trace-source cluster
	// (Option A: the metric does not stamp server-side cluster; it is
	// recovered via the topology pod-UID index at parse time).
	crossCluster := 0
	for _, e := range g.Edges {
		if e.Type != graph.EdgeTypePodCallsPod {
			continue
		}
		src, srcOK := g.NodesByID[e.Source]
		tgt, tgtOK := g.NodesByID[e.Target]
		if !srcOK || !tgtOK {
			continue
		}
		if src.Labels()["cluster"] != tgt.Labels()["cluster"] {
			crossCluster++
		}
	}
	slog.Info("graph built",
		"clusters", topology.ClustersObserved,
		"nodes", len(g.NodesByID),
		"edges", len(g.Edges),
		"cross_cluster_edges", crossCluster,
		"start", end.Add(-window).UTC().Format(time.RFC3339),
		"end", end.UTC().Format(time.RFC3339),
	)

	// Self-metrics: observational gauges for last build.
	b.metrics.GraphNodeCount.Reset()
	for k, count := range g.NodeCountByKind() {
		b.metrics.GraphNodeCount.WithLabelValues(k[0], k[1]).Set(float64(count))
	}
	b.metrics.GraphEdgeCount.Reset()
	for k, count := range g.EdgeCountByType() {
		b.metrics.GraphEdgeCount.WithLabelValues(k[0], k[1]).Set(float64(count))
	}
	b.metrics.ClustersObserved.Set(float64(len(topology.ClustersObserved)))
	return g, nil
}

func assemble(topology Topology, sg ServiceGraphResult) ([]graph.GraphNode, []*graph.Edge) {
	// Nodes: pods + k8s nodes + pvcs + synthesised pods + externals.
	total := len(topology.Pods) + len(topology.Nodes) + len(topology.PVCs) +
		len(sg.SynthPods) + len(sg.ExternalNodes)
	nodes := make([]graph.GraphNode, 0, total)
	for _, p := range topology.Pods {
		nodes = append(nodes, p)
	}
	for _, n := range topology.Nodes {
		nodes = append(nodes, n)
	}
	for _, pv := range topology.PVCs {
		nodes = append(nodes, pv)
	}
	for _, p := range sg.SynthPods {
		nodes = append(nodes, p)
	}
	for _, e := range sg.ExternalNodes {
		nodes = append(nodes, e)
	}

	edges := make([]*graph.Edge, 0,
		len(topology.RestartEdges)+len(sg.Edges)+len(topology.Pods)+len(topology.PodPVCs))
	edges = append(edges, topology.RestartEdges...)
	edges = append(edges, TopologyEdges(topology)...)
	edges = append(edges, sg.Edges...)
	return nodes, edges
}

func (b *Builder) probeClusterSize(ctx context.Context, allowlistRegex string, ts time.Time) error {
	vec, err := b.q.Instant(ctx, string(promql.QClusterSizeProbe),
		promql.Render(promql.QClusterSizeProbe, 0, allowlistRegex), ts.UTC())
	if err != nil {
		return NewError(ReasonUpstream, "cluster-size probe failed", err)
	}
	if len(vec) == 0 {
		return nil
	}
	count := int(vec[0].Value)
	if count > b.cfg.MaxPods {
		return NewError(ReasonClusterTooLarge,
			fmt.Sprintf("scope contains %s pods, exceeds --max-pods=%d",
				strconv.Itoa(count), b.cfg.MaxPods),
			nil)
	}
	return nil
}

func (b *Builder) upProbe(ctx context.Context) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	vec, err := b.q.Instant(probeCtx, string(promql.QUpProbe),
		promql.Render(promql.QUpProbe, 0, ""), time.Now().UTC())
	if err != nil {
		return false, err
	}
	return len(vec) > 0, nil
}
