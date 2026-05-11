#!/usr/bin/env bash
# Helm-based alternative to the raw `kubectl apply -f manifests/`
# step in bootstrap.sh. Run AFTER bootstrap.sh has created the kind
# cluster and applied the supporting manifests (namespace, VictoriaMetrics,
# kube-state-metrics, Beyla, Alloy, Tempo, Grafana, api-keys Secret) — this
# script only manages the kube-state-graph Deployment + Service via the
# in-repo Helm chart at charts/kube-state-graph/.
#
# Workflow:
#   1. ./bootstrap.sh                        # one-time: cluster + supporting manifests
#   2. kubectl -n kube-state-graph delete deploy,svc kube-state-graph --ignore-not-found
#   3. ./helm-install.sh                     # take over with Helm
#
# Re-running this script is a `helm upgrade --install`, so it doubles as a
# redeploy after `make build && kind load docker-image`.
set -euo pipefail

CLUSTER_NAME=kube-state-graph
NAMESPACE=kube-state-graph
RELEASE=kube-state-graph
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
CHART_DIR="$REPO_ROOT/charts/kube-state-graph"
VALUES_FILE="$SCRIPT_DIR/values-kind.yaml"
DOCKER="${DOCKER:-$(command -v docker || command -v podman)}"

if [[ -z "$DOCKER" ]]; then
  echo "ERROR: neither docker nor podman found on PATH" >&2
  exit 1
fi

if ! command -v helm >/dev/null; then
  echo "ERROR: helm not on PATH" >&2
  exit 1
fi

echo "==> Building image"
(cd "$REPO_ROOT" && make build)
"$DOCKER" build -t localhost/kube-state-graph/server:dev \
  -f "$REPO_ROOT/deploy/docker/server.Dockerfile" "$REPO_ROOT"

echo "==> Loading image into Kind ($CLUSTER_NAME)"
kind load docker-image localhost/kube-state-graph/server:dev --name "$CLUSTER_NAME"

echo "==> helm upgrade --install $RELEASE"
helm upgrade --install "$RELEASE" "$CHART_DIR" \
  --namespace "$NAMESPACE" \
  --values "$VALUES_FILE" \
  --wait \
  --timeout 3m

echo "==> Rollout status"
kubectl -n "$NAMESPACE" rollout status deploy/"$RELEASE" --timeout=180s

cat <<MSG
==> Helm release "$RELEASE" deployed.
    API:  http://localhost:30080
    Try:  curl -H 'X-API-Key: dev-key-rotate-me-please' http://localhost:30080/v1/clusters

    Render without applying:
      helm template $RELEASE $CHART_DIR --namespace $NAMESPACE --values $VALUES_FILE

    Uninstall:
      helm uninstall $RELEASE --namespace $NAMESPACE
MSG
