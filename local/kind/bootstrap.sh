#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME=kube-state-graph
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
NAMESPACE=kube-state-graph
DOCKER="${DOCKER:-$(command -v docker || command -v podman)}"
if [[ -z "$DOCKER" ]]; then
  echo "ERROR: neither docker nor podman found on PATH" >&2
  exit 1
fi

echo "==> Creating Kind cluster $CLUSTER_NAME"
if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
  kind create cluster --name "$CLUSTER_NAME" --config "$SCRIPT_DIR/kind-config.yaml"
else
  echo "    cluster already exists, skipping"
fi

echo "==> Building image"
(cd "$REPO_ROOT" && make build)
# Tag with explicit localhost/ prefix so docker and podman both produce a
# canonical reference. Manifests pin this same string with imagePullPolicy=Never
# (see local/kind/manifests/30-api-server.yaml).
"$DOCKER" build -t localhost/kube-state-graph/server:dev -f "$REPO_ROOT/deploy/docker/server.Dockerfile" "$REPO_ROOT"

echo "==> Loading image into Kind"
kind load docker-image localhost/kube-state-graph/server:dev --name "$CLUSTER_NAME"

echo "==> Applying manifests"
kubectl apply -f "$SCRIPT_DIR/manifests/"

echo "==> Loading Grafana dashboard ConfigMap"
kubectl -n "$NAMESPACE" delete configmap grafana-dashboard-nodegraph --ignore-not-found
kubectl -n "$NAMESPACE" create configmap grafana-dashboard-nodegraph \
  --from-file=kube-state-graph-nodegraph.json="$REPO_ROOT/deploy/grafana/kube-state-graph-nodegraph.json"

echo "==> Restarting workloads to pick up fresh ConfigMaps"
kubectl -n "$NAMESPACE" rollout restart \
  deploy/victoria-metrics deploy/kube-state-graph deploy/grafana \
  deploy/alloy deploy/tempo

echo "==> Waiting for rollouts"
for d in victoria-metrics kube-state-metrics kube-state-graph grafana alloy tempo pvc-demo; do
  kubectl -n "$NAMESPACE" rollout status deploy/$d --timeout=180s
done
kubectl -n "$NAMESPACE" rollout status daemonset/beyla --timeout=180s

cat <<MSG
==> Local kind rig ready.
    API:     http://localhost:30080  (kube-state-graph)
    VM:      http://localhost:30428  (VictoriaMetrics, Prometheus-compatible API)
    Grafana: http://localhost:30300  (admin / admin)

    Topology metrics: kube-state-metrics scrapes the kind cluster itself
    (--resources=pods,nodes; allowlist limited to kube_pod_info,
    kube_node_info, kube_node_labels). The VM scrape job relabels the
    series with cluster=kind-local so kube-state-graph accepts them.

    Service-graph metrics: Beyla DaemonSet auto-instruments every Go/HTTP
    process in the kube-state-graph namespace via eBPF and ships OTLP
    spans to Alloy. Alloy's otelcol.connector.servicegraph
    (dimensions=[k8s.pod.uid]) produces
    traces_service_graph_request_total{client_k8s_pod_uid,
    server_k8s_pod_uid,...} and remote-writes to VictoriaMetrics, so
    /v1/graph?edge_type=pod-calls-pod returns real edges between
    in-cluster Go services (kube-state-graph → VictoriaMetrics →
    kube-state-metrics, Grafana → kube-state-graph, etc.) — no synthetic
    traffic generator needed. Cross-cluster scenarios remain covered by
    internal/integration/ (testcontainers-go VictoriaMetrics).

    OTLP traces: kube-state-graph exports its own spans
    (kube-state-graph.build → prometheus.query → kube-state-graph.project
    → kube-state-graph.serialise) plus all Beyla traces through Alloy
    into Tempo. Logs from kube-state-graph land in Alloy stdout via the
    debug exporter — view with:
        kubectl -n $NAMESPACE logs deploy/alloy -f

    To browse traces:
      1. Open Grafana → Explore → datasource "Tempo".
      2. Either click "Search", filter Service Name="kube-state-graph";
         or use TraceQL: { resource.service.name = "kube-state-graph" }.
      3. Drill into a trace to see the build / PromQL / serialise span tree.

    Open Grafana → folder kube-state-graph → Node Graph dashboard for the
    cytoscape view.
MSG
