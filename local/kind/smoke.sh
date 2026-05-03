#!/usr/bin/env bash
# Manual smoke against the local kind rig. CI does NOT run this — it covers
# what the single-cluster Kind rig with real kube-state-metrics can demonstrate
# visually. Multi-cluster, cross-cluster, and pod-calls-pod paths are covered
# by internal/integration/ tests against testcontainers VictoriaMetrics.
set -euo pipefail

NAMESPACE=kube-state-graph
SVC=kube-state-graph
HOST=${KSG_SMOKE_HOST:-http://localhost:30080}
READYZ_BUDGET=${KSG_SMOKE_READYZ_BUDGET:-60}
RIG_CLUSTER=${KSG_SMOKE_CLUSTER:-kind-local}

echo "==> Smoke target: $HOST (rig cluster: $RIG_CLUSTER)"

assert_status() {
  local path=$1
  local expect=$2
  local got
  got=$(curl -sS -o /dev/null -w '%{http_code}' "$HOST$path")
  if [[ "$got" != "$expect" ]]; then
    echo "FAIL: $path returned $got, expected $expect"
    exit 1
  fi
  echo "OK   $path -> $got"
}

assert_status /livez 200

echo "==> Waiting up to ${READYZ_BUDGET}s for /readyz"
for i in $(seq 1 "$READYZ_BUDGET"); do
  if curl -sf "$HOST/readyz" >/dev/null; then
    echo "OK   /readyz -> 200 after ${i}s"
    break
  fi
  sleep 1
  if [[ "$i" -eq "$READYZ_BUDGET" ]]; then
    echo "FAIL: /readyz did not return 200 within ${READYZ_BUDGET}s"
    exit 1
  fi
done

echo "==> /v1/clusters"
clusters=$(curl -sS "$HOST/v1/clusters")
echo "$clusters" | grep -q "\"$RIG_CLUSTER\"" || { echo "FAIL: $RIG_CLUSTER missing in $clusters"; exit 1; }
echo "OK   /v1/clusters contains $RIG_CLUSTER"

echo "==> /v1/edge-types"
edges=$(curl -sS "$HOST/v1/edge-types")
for et in pod-runs-on-node pod-mounts-pvc pod-calls-pod; do
  echo "$edges" | grep -q "\"$et\"" || { echo "FAIL: edge type $et missing"; exit 1; }
done
echo "OK   /v1/edge-types lists pod-runs-on-node, pod-mounts-pvc, pod-calls-pod"

NOW=$(date -u +%s)
START=$((NOW - 300))
echo "==> /v1/graph (5m window)"
graph=$(curl -sS "$HOST/v1/graph?start=$START&end=$NOW")
echo "$graph" | grep -q '"nodes":'             || { echo "FAIL: nodes missing"; exit 1; }
echo "$graph" | grep -q '"edges":'             || { echo "FAIL: edges missing"; exit 1; }
echo "$graph" | grep -q '"pod-runs-on-node"'   || { echo "FAIL: pod-runs-on-node edges missing"; exit 1; }
echo "OK   /v1/graph returns nodes + pod-runs-on-node edges"

echo "==> /v1/graph?cluster=$RIG_CLUSTER"
filtered=$(curl -sS "$HOST/v1/graph?cluster=$RIG_CLUSTER&start=$START&end=$NOW")
echo "$filtered" | grep -q "\"$RIG_CLUSTER\"" || { echo "FAIL: filtered missing $RIG_CLUSTER"; exit 1; }
echo "OK   cluster filter applied"

echo "==> /metrics"
metrics=$(curl -sS "$HOST/metrics")
echo "$metrics" | grep -q "^kube_state_graph_" || { echo "FAIL: self-metrics missing"; exit 1; }
echo "OK   /metrics emits kube_state_graph_* series"

echo "==> All smoke assertions passed"
