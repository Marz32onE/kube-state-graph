# Multi-cluster setup

`kube-state-graph` reads from a single centralised VictoriaMetrics. To
populate it from N clusters, each cluster's scrape pipeline must apply a
uniform `cluster` external label.

## Producer-side checklist

1. **kube-state-metrics** — install per cluster. No special flags needed.
2. **vmagent / Prometheus** scraping `kube-state-metrics` — set the cluster
   external label:

   ```yaml
   # vmagent extraArgs (Helm values) or prometheus.yml global section:
   external_labels:
     cluster: prod-east
   remoteWrite:
     - url: https://vm.example.com/api/v1/write
   ```

3. **Service-graph metrics producer** — Tempo's metrics-generator,
   OpenTelemetry Collector with the `servicegraph` connector, or Grafana
   Alloy. The producer needs to emit `client_k8s_pod_uid` and
   `server_k8s_pod_uid` dimensions; the cluster of each side does **not**
   need to be stamped on the metric. Each producer instance simply tags
   every emitted series with its own cluster as the `cluster` external
   label (the trace source). The API server recovers the server-side
   cluster at build time by joining `server_k8s_pod_uid` against the
   topology pod-UID index — Kubernetes pod UIDs are unique cross-cluster in
   practice, so the lookup is unambiguous:

   ```yaml
   processors:
     k8sattributes:
       extract:
         metadata: [k8s.pod.uid, k8s.pod.name, k8s.namespace.name, k8s.node.name]
   connectors:
     servicegraph:
       dimensions: [k8s.pod.uid, k8s.namespace.name]
   exporters:
     prometheusremotewrite:
       endpoint: https://vm.example.com/api/v1/write
       external_labels:
         cluster: prod-east   # the cluster running this producer = client side
   ```

   Apps must propagate trace context across calls (W3C Trace Context); without
   propagation the connector cannot pair client + server spans and no edge
   metrics result.

## Verifying the contract

The API server's discovery endpoint walks `kube_node_info{cluster=...}`. To
sanity-check the producer side, run:

```promql
group by (cluster) (last_over_time(kube_node_info[1h]))
group by (cluster) (rate(traces_service_graph_request_total[5m]))
```

Both should return one row per cluster (the second query returns one row per
trace-source / client-side cluster — server-side cluster does not appear as a
metric label and is recovered at build time by the API server).

## Caller-side scoping

Every build loads every cluster present in upstream VictoriaMetrics. Callers narrow scope per request via the `?cluster=` query parameter on `/v1/graph` (repeatable, OR-combined). Server-side scope narrowing is not available — bounded query cost is delegated to upstream VictoriaMetrics search limits.

## Cluster name discipline

- Treat names as opaque strings; the API server does not normalise.
- Series missing the `cluster` label are bucketed under `cluster="unknown"` —
  fix the scrape pipeline if `unknown` shows up in `/v1/clusters`.
