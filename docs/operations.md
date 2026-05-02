# Operations

## Self-metrics (`/metrics`)

| Metric                                                     | Type      | Notes |
|------------------------------------------------------------|-----------|-------|
| `kube_state_graph_build_duration_seconds{cache_status}`    | histogram | `cache_status` ∈ `hit`/`miss`/`coalesced`. |
| `kube_state_graph_project_duration_seconds`                | histogram | Filter + traversal pruning. |
| `kube_state_graph_serialise_duration_seconds{format}`      | histogram | `cytoscape` / `nodegraph`. |
| `kube_state_graph_cache_hits_total{layer}`                 | counter   | `ristretto`/`singleflight`/`etag`. |
| `kube_state_graph_cache_misses_total{layer}`               | counter   | |
| `kube_state_graph_cache_size_entries`                      | gauge     | |
| `kube_state_graph_cache_cost_bytes`                        | gauge     | |
| `kube_state_graph_cache_evictions_total{reason}`           | counter   | `cost`/`ttl`. |
| `kube_state_graph_cache_rejected_total`                    | counter   | Ristretto admission rejections. |
| `kube_state_graph_singleflight_dedup_total`                | counter   | |
| `kube_state_graph_build_concurrency`                       | gauge     | |
| `kube_state_graph_build_rejected_total{reason}`            | counter   | `capacity`/`timeout`. |
| `kube_state_graph_graph_node_count{cluster,kind}`          | gauge     | Last build only. |
| `kube_state_graph_graph_edge_count{type,cross_cluster}`    | gauge     | Last build only. |
| `kube_state_graph_clusters_observed`                       | gauge     | |
| `kube_state_graph_upstream_query_duration_seconds{query}`  | histogram | |
| `kube_state_graph_upstream_query_failures_total{query}`    | counter   | |
| `kube_state_graph_http_requests_total{path,status}`        | counter   | |

## Recommended alerts

```yaml
- alert: KubeStateGraphUpstreamErrorRate
  expr: |
    sum(rate(kube_state_graph_upstream_query_failures_total[5m]))
      / sum(rate(kube_state_graph_upstream_query_duration_seconds_count[5m])) > 0.05
  for: 10m
  labels: {severity: warning}

- alert: KubeStateGraphCacheRejectionsHigh
  expr: rate(kube_state_graph_cache_rejected_total[10m]) > 1
  for: 10m
  labels: {severity: info}

- alert: KubeStateGraphBuildConcurrencyExhausted
  expr: increase(kube_state_graph_build_rejected_total{reason="capacity"}[5m]) > 0
  for: 5m
  labels: {severity: warning}

- alert: KubeStateGraphReadynessFailing
  expr: probe_success{job="kube-state-graph-readyz"} == 0
  for: 5m
  labels: {severity: critical}
```

## Health probes

- `GET /livez` — process liveness (always 200 while running).
- `GET /readyz` — issues a 1 s `up{}` probe to the upstream. 200 only when
  the upstream answers; 503 otherwise. Use as the Kubernetes readiness probe.

## Capacity planning

- v1 ceiling: ≤ 5 k pods total in scope (across all clusters in
  `--clusters-allowlist`). Lower if profile shows build duration approaching
  `--build-timeout`.
- Cache memory bound by `--cache-max-cost-bytes` (default 256 MiB). Each
  entry holds a typed `*Graph` for one time bucket; entry count is bounded by
  the number of distinct buckets seen recently, not by filter combinations.
- Recommended Pod resource limits: CPU 500m / Memory 512Mi for clusters under
  the v1 ceiling. Scale up if profiling shows GC pressure.

## Tuning notes

- Increase `--build-concurrency` if `kube_state_graph_build_rejected_total{reason="capacity"}`
  ticks during normal traffic.
- Increase `--build-timeout` only if upstream PromQL is slow; the default 15 s
  is generous for ≤ 5 k pods.
- Set `--clusters-allowlist` whenever the centralised VictoriaMetrics holds
  more clusters than this server should expose; allowlist injection bounds
  the upstream scan cost regardless of caller filters.
