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
  deploy/victoria-metrics deploy/kube-state-graph deploy/grafana

echo "==> Waiting for rollouts"
for d in victoria-metrics kube-state-metrics kube-state-graph grafana; do
  kubectl -n "$NAMESPACE" rollout status deploy/$d --timeout=180s
done

cat <<MSG
==> Local kind rig ready.
    API:     http://localhost:30080  (kube-state-graph)
    VM:      http://localhost:30428  (VictoriaMetrics, Prometheus-compatible API)
    Grafana: http://localhost:30300  (admin / admin)

    Real metrics: kube-state-metrics scrapes the kind cluster itself
    (--resources=pods,nodes; allowlist limited to kube_pod_info,
    kube_node_info, kube_node_labels). The VM scrape job relabels the
    series with cluster=kind-local so kube-state-graph accepts them.

    Service-graph metrics are NOT generated in this rig; cross-cluster /
    pod-call edge logic is exercised by integration tests in
    internal/integration/ (testcontainers-go VictoriaMetrics).

    Open Grafana → folder kube-state-graph → Node Graph dashboard.
MSG
