package build

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/marz32one/kube-state-graph/internal/clock"
	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/observability"
	"github.com/marz32one/kube-state-graph/internal/promql"
	"github.com/marz32one/kube-state-graph/internal/telemetry"
)

// Builder runs the topology + service-graph readers and assembles a
// multi-cluster Graph for one bucketed time window.
type Builder struct {
	q       promql.Querier
	r       promql.Renderer
	cfg     config.Config
	metrics *observability.Metrics
	clk     clock.Clock
}

// New constructs a Builder. clk may be nil; nil falls back to clock.System.
// The Renderer is derived from cfg.MetricPrefix and held on the Builder so
// every PromQL query the build pipeline issues picks up the configured
// upstream metric-name prefix (see design.md D26).
func New(q promql.Querier, cfg config.Config, m *observability.Metrics, clk clock.Clock) *Builder {
	if clk == nil {
		clk = clock.System{}
	}
	return &Builder{
		q:       q,
		r:       promql.Renderer{Prefix: cfg.MetricPrefix},
		cfg:     cfg,
		metrics: m,
		clk:     clk,
	}
}

// Build runs all upstream queries for [end - window, end] and returns the
// joined multi-cluster Graph.
func (b *Builder) Build(ctx context.Context, window time.Duration, end time.Time) (*graph.Graph, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "kube-state-graph.build",
		trace.WithAttributes(
			attribute.Int64("kube_state_graph.window_seconds", int64(window.Seconds())),
			attribute.Int64("kube_state_graph.end_unix", end.Unix()),
		),
	)
	defer span.End()

	topology, err := ReadTopology(ctx, b.q, b.r, window, end)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewError(ReasonTimeout, "build timeout", err)
		}
		return nil, NewError(ReasonUpstream, "topology read failed", err)
	}

	// Outside-retention check: zero pods + healthy upstream ⇒ retention miss.
	if len(topology.Pods) == 0 && len(topology.Nodes) == 0 {
		if up, _ := b.upProbe(ctx); up {
			startStr := end.Add(-window).UTC().Format(time.RFC3339)
			endStr := end.UTC().Format(time.RFC3339)
			msg := fmt.Sprintf("no topology rows in window [%s, %s] (window=%s); upstream healthy",
				startStr, endStr, window)
			err := NewError(ReasonOutsideRetention, msg, nil)
			span.RecordError(err)
			span.SetStatus(codes.Error, string(ReasonOutsideRetention))
			slog.WarnContext(ctx, "outside_retention",
				"start", startStr,
				"end", endStr,
				"window", window.String(),
				"metric_prefix", b.cfg.MetricPrefix,
			)
			return nil, err
		}
	}

	sg, err := ReadServiceGraph(ctx, b.q, b.r, window, end, b.cfg.ExternalNamePattern, topology)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewError(ReasonTimeout, "build timeout", err)
		}
		return nil, NewError(ReasonUpstream, "service-graph read failed", err)
	}

	nodes, edges := assemble(topology, sg)
	g := graph.NewGraph(nodes, edges, b.clk.Now().UTC())

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
	slog.InfoContext(ctx, "graph built",
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

	span.SetAttributes(
		attribute.Int("kube_state_graph.cluster_count", len(topology.ClustersObserved)),
		attribute.Int("graph.node.count", len(g.NodesByID)),
		attribute.Int("graph.edge.count", len(g.Edges)),
		attribute.Int("kube_state_graph.cross_cluster_edges", crossCluster),
	)
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
		len(sg.Edges)+len(topology.Pods)+len(topology.PodPVCs))
	edges = append(edges, TopologyEdges(topology)...)
	edges = append(edges, sg.Edges...)
	return nodes, edges
}

func (b *Builder) upProbe(ctx context.Context) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, b.cfg.APITimeout)
	defer cancel()
	vec, err := b.q.Instant(probeCtx, string(promql.QUpProbe),
		b.r.Render(promql.QUpProbe, 0), b.clk.Now().UTC())
	if err != nil {
		return false, err
	}
	return len(vec) > 0, nil
}
