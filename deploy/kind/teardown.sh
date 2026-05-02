#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME=kube-state-graph

if kind get clusters | grep -q "^$CLUSTER_NAME$"; then
  echo "==> Deleting Kind cluster $CLUSTER_NAME"
  kind delete cluster --name "$CLUSTER_NAME"
else
  echo "==> Cluster $CLUSTER_NAME not found, nothing to do"
fi
