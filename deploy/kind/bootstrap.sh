#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME=kube-state-graph
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
NAMESPACE=kube-state-graph

echo "==> Creating Kind cluster $CLUSTER_NAME"
if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
  kind create cluster --name "$CLUSTER_NAME" --config "$SCRIPT_DIR/kind-config.yaml"
else
  echo "    cluster already exists, skipping"
fi

echo "==> Building images"
(cd "$REPO_ROOT" && make build fixtures)
docker build -t kube-state-graph/server:dev    -f "$REPO_ROOT/deploy/docker/server.Dockerfile"      "$REPO_ROOT"
docker build -t kube-state-graph/vm-fixtures:dev -f "$REPO_ROOT/deploy/docker/vm-fixtures.Dockerfile" "$REPO_ROOT"

echo "==> Loading images into Kind"
kind load docker-image kube-state-graph/server:dev    --name "$CLUSTER_NAME"
kind load docker-image kube-state-graph/vm-fixtures:dev --name "$CLUSTER_NAME"

echo "==> Applying manifests"
kubectl apply -f "$SCRIPT_DIR/manifests/"

echo "==> Generating fixtures ConfigMap"
kubectl -n "$NAMESPACE" delete configmap vm-fixtures-data --ignore-not-found
kubectl -n "$NAMESPACE" create configmap vm-fixtures-data \
  --from-file=fixtures.yaml="$REPO_ROOT/tests/harness/vm-fixtures/fixtures.yaml"

echo "==> Restarting workloads to pick up fresh ConfigMap"
kubectl -n "$NAMESPACE" rollout restart deploy/vm-fixtures deploy/victoria-metrics deploy/kube-state-graph

echo "==> Waiting for rollouts"
for d in victoria-metrics vm-fixtures kube-state-graph; do
  kubectl -n "$NAMESPACE" rollout status deploy/$d --timeout=180s
done

echo "==> Cluster ready: kubectl --context kind-$CLUSTER_NAME get pods -n $NAMESPACE"
