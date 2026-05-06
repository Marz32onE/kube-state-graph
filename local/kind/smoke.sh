#!/usr/bin/env bash
# Manual smoke against the local kind rig. CI does NOT run this — it covers
# what the single-cluster Kind rig demonstrates visually: real kube-state-metrics
# topology plus Beyla→Alloy-derived pod-calls-pod edges. Multi-cluster /
# cross-cluster paths remain covered by internal/integration/ tests against
# testcontainers VictoriaMetrics.
set -euo pipefail

NAMESPACE=kube-state-graph
SVC=kube-state-graph
HOST=${KSG_SMOKE_HOST:-http://localhost:30080}
VM_HOST=${KSG_SMOKE_VM_HOST:-http://localhost:30428}
READYZ_BUDGET=${KSG_SMOKE_READYZ_BUDGET:-60}
SVCGRAPH_BUDGET=${KSG_SMOKE_SVCGRAPH_BUDGET:-180}
RIG_CLUSTER=${KSG_SMOKE_CLUSTER:-kind-local}
# Must match a key in local/kind/manifests/05-api-key-secret.yaml.
API_KEY=${KSG_SMOKE_API_KEY:-smoke-test-key}
AUTH_HEADER="X-API-Key: $API_KEY"

echo "==> Smoke target: $HOST (rig cluster: $RIG_CLUSTER)"

assert_status() {
  local path=$1
  local expect=$2
  local got
  got=$(curl -sS -o /dev/null -w '%{http_code}' -H "$AUTH_HEADER" "$HOST$path")
  if [[ "$got" != "$expect" ]]; then
    echo "FAIL: $path returned $got, expected $expect"
    exit 1
  fi
  echo "OK   $path -> $got"
}

# /livez is exempt from auth — verify without the header on purpose.
unauth=$(curl -sS -o /dev/null -w '%{http_code}' "$HOST/livez")
if [[ "$unauth" != "200" ]]; then
  echo "FAIL: /livez (no auth) returned $unauth, expected 200 (open path)"
  exit 1
fi
echo "OK   /livez -> 200 (no API key required)"

# /v1/edge-types WITHOUT a key should be 401 once the secret is mounted.
denied=$(curl -sS -o /dev/null -w '%{http_code}' "$HOST/v1/edge-types")
if [[ "$denied" != "401" ]]; then
  echo "FAIL: /v1/edge-types without API key returned $denied, expected 401"
  exit 1
fi
echo "OK   /v1/edge-types (no auth) -> 401 (auth enforced)"

echo "==> Waiting up to ${READYZ_BUDGET}s for /readyz"
for i in $(seq 1 "$READYZ_BUDGET"); do
  if curl -sf "$HOST/readyz" >/dev/null; then
    echo "OK   /readyz -> 200 after ${i}s (no API key required)"
    break
  fi
  sleep 1
  if [[ "$i" -eq "$READYZ_BUDGET" ]]; then
    echo "FAIL: /readyz did not return 200 within ${READYZ_BUDGET}s"
    exit 1
  fi
done

echo "==> /v1/clusters"
clusters=$(curl -sS -H "$AUTH_HEADER" "$HOST/v1/clusters")
echo "$clusters" | grep -q "\"$RIG_CLUSTER\"" || { echo "FAIL: $RIG_CLUSTER missing in $clusters"; exit 1; }
echo "OK   /v1/clusters contains $RIG_CLUSTER"

echo "==> /v1/edge-types"
edges=$(curl -sS -H "$AUTH_HEADER" "$HOST/v1/edge-types")
for et in pod-runs-on-node pod-mounts-pvc pod-calls-pod; do
  echo "$edges" | grep -q "\"$et\"" || { echo "FAIL: edge type $et missing"; exit 1; }
done
echo "OK   /v1/edge-types lists pod-runs-on-node, pod-mounts-pvc, pod-calls-pod"

NOW=$(date -u +%s)
START=$((NOW - 300))
echo "==> /v1/graph (5m window)"
graph=$(curl -sS -H "$AUTH_HEADER" "$HOST/v1/graph?start=$START&end=$NOW")
echo "$graph" | grep -q '"nodes":'             || { echo "FAIL: nodes missing"; exit 1; }
echo "$graph" | grep -q '"edges":'             || { echo "FAIL: edges missing"; exit 1; }
echo "$graph" | grep -q '"pod-runs-on-node"'   || { echo "FAIL: pod-runs-on-node edges missing"; exit 1; }
echo "OK   /v1/graph returns nodes + pod-runs-on-node edges"

echo "==> /v1/graph?cluster=$RIG_CLUSTER"
filtered=$(curl -sS -H "$AUTH_HEADER" "$HOST/v1/graph?cluster=$RIG_CLUSTER&start=$START&end=$NOW")
echo "$filtered" | grep -q "\"$RIG_CLUSTER\"" || { echo "FAIL: filtered missing $RIG_CLUSTER"; exit 1; }
echo "OK   cluster filter applied"

echo "==> /metrics"
metrics=$(curl -sS "$HOST/metrics")
echo "$metrics" | grep -q "^kube_state_graph_" || { echo "FAIL: self-metrics missing"; exit 1; }
echo "OK   /metrics emits kube_state_graph_* series (no API key required)"

# Beyla→Alloy pipeline takes a moment to warm up: spans must be paired by the
# servicegraph connector before any series exists, then scraped by VM. Wait up
# to SVCGRAPH_BUDGET seconds for the first paired sample with both pod UIDs.
echo "==> Waiting up to ${SVCGRAPH_BUDGET}s for traces_service_graph_request_total with pod UIDs"
SG_QUERY='traces_service_graph_request_total{cluster="kind-local",client_k8s_pod_uid!="",server_k8s_pod_uid!=""}'
for i in $(seq 1 "$SVCGRAPH_BUDGET"); do
  count=$(curl -sS --data-urlencode "query=$SG_QUERY" "$VM_HOST/api/v1/query" | grep -o '"value"' | wc -l | tr -d ' ')
  if [[ "$count" -ge 1 ]]; then
    echo "OK   service-graph series present after ${i}s ($count samples)"
    break
  fi
  sleep 1
  if [[ "$i" -eq "$SVCGRAPH_BUDGET" ]]; then
    echo "FAIL: no traces_service_graph_request_total{cluster=kind-local,*pod_uid!=\"\"} within ${SVCGRAPH_BUDGET}s"
    echo "       check beyla DaemonSet logs and alloy Deployment logs"
    exit 1
  fi
done

echo "==> /v1/graph pod-calls-pod edges"
p2p=$(curl -sS -H "$AUTH_HEADER" "$HOST/v1/graph?cluster=$RIG_CLUSTER&edge_type=pod-calls-pod&start=$START&end=$NOW")
edge_count=$(echo "$p2p" | grep -o '"pod-calls-pod"' | wc -l | tr -d ' ')
if [[ "$edge_count" -lt 1 ]]; then
  echo "FAIL: no pod-calls-pod edges in /v1/graph response"
  echo "$p2p"
  exit 1
fi
echo "OK   /v1/graph returns $edge_count pod-calls-pod edge marker(s)"

echo "==> All smoke assertions passed"
