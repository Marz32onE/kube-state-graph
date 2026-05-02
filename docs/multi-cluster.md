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

3. **Service-graph metrics producer** — an OpenTelemetry Collector or Grafana
   Alloy with the `servicegraph` connector + `k8sattributes` processor,
   configured to emit `client_cluster` and `server_cluster` so the API server
   can resolve cross-cluster RPC:

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
         cluster: prod-east
   ```

   Apps must propagate trace context across calls (W3C Trace Context); without
   propagation the connector cannot pair client + server spans and no edge
   metrics result.

## Verifying the contract

The API server's discovery endpoint walks `kube_node_info{cluster=...}`. To
sanity-check the producer side, run:

```promql
group by (cluster) (last_over_time(kube_node_info[1h]))
group by (client_cluster, server_cluster) (rate(traces_service_graph_request_total[5m]))
```

Both should return one row per cluster (and per cluster pair, for the second
query).

## Allowlisting

`--clusters-allowlist=prod-east,prod-west` limits the API server to a subset
even if VictoriaMetrics holds more cluster labels. The flag is injected into
every PromQL query and into the discovery query.

## Cluster name discipline

- Treat names as opaque strings; the API server does not normalise.
- Series missing the `cluster` label are bucketed under `cluster="unknown"` —
  fix the scrape pipeline if `unknown` shows up in `/v1/clusters`.
