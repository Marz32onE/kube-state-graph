#!/usr/bin/env bash
#
# Iterate on the running Kind rig without tearing the cluster down.
#
# Steps:
#   1. Rebuild the kube-state-graph image.
#   2. Load it into the existing Kind cluster.
#   3. kubectl apply -f manifests/ (handles new YAML files like Tempo).
#   4. Refresh the Grafana dashboard ConfigMap.
#   5. Rollout-restart all deployments + the Beyla DaemonSet so they pick up
#      the new image / ConfigMaps.
#   6. Wait for rollouts.
#
# Use kind-up for fresh-cluster bootstrap; kind-down for teardown.
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

if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
  echo "ERROR: Kind cluster $CLUSTER_NAME does not exist. Run 'make kind-up' first." >&2
  exit 1
fi

echo "==> Building image"
(cd "$REPO_ROOT" && make build)
"$DOCKER" build -t localhost/kube-state-graph/server:dev -f "$REPO_ROOT/deploy/docker/server.Dockerfile" "$REPO_ROOT"

echo "==> Loading image into Kind"
kind load docker-image localhost/kube-state-graph/server:dev --name "$CLUSTER_NAME"

echo "==> Applying manifests"
kubectl apply -f "$SCRIPT_DIR/manifests/"

echo "==> Refreshing Grafana dashboard ConfigMap"
kubectl -n "$NAMESPACE" delete configmap grafana-dashboard-nodegraph --ignore-not-found
kubectl -n "$NAMESPACE" create configmap grafana-dashboard-nodegraph \
  --from-file=kube-state-graph-nodegraph.json="$REPO_ROOT/deploy/grafana/kube-state-graph-nodegraph.json"

echo "==> Rolling out workloads"
kubectl -n "$NAMESPACE" rollout restart \
  deploy/victoria-metrics deploy/kube-state-graph deploy/grafana \
  deploy/alloy deploy/tempo deploy/kube-state-metrics deploy/pvc-demo
kubectl -n "$NAMESPACE" rollout restart daemonset/beyla || true

echo "==> Waiting for rollouts"
for d in victoria-metrics kube-state-metrics kube-state-graph grafana alloy tempo pvc-demo; do
  kubectl -n "$NAMESPACE" rollout status deploy/$d --timeout=180s
done
kubectl -n "$NAMESPACE" rollout status daemonset/beyla --timeout=180s

cat <<MSG
==> Redeploy complete. URLs unchanged from last 'kind-up':
    API:     http://localhost:30080
    VM:      http://localhost:30428
    Grafana: http://localhost:30300  (admin / admin)

    Check traces:
      Grafana → Explore → Tempo → Search → Service Name: kube-state-graph
    or:
      kubectl -n $NAMESPACE port-forward svc/tempo 3200:3200 &
      curl -s 'http://localhost:3200/api/search?tags=service.name%3Dkube-state-graph' | jq
MSG
