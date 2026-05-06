# Operations

## Self-metrics (`/metrics`)

| Metric                                                     | Type      | Notes |
|------------------------------------------------------------|-----------|-------|
| `kube_state_graph_build_duration_seconds`                  | histogram | Wall-clock build time per request. |
| `kube_state_graph_project_duration_seconds`                | histogram | Filter + traversal pruning. |
| `kube_state_graph_serialise_duration_seconds{format}`      | histogram | `cytoscape` / `nodegraph`. |
| `kube_state_graph_build_concurrency`                       | gauge     | |
| `kube_state_graph_build_rejected_total{reason}`            | counter   | `capacity`/`timeout`. |
| `kube_state_graph_graph_node_count{cluster,kind}`          | gauge     | Last build only. |
| `kube_state_graph_graph_edge_count{type,cross_cluster}`    | gauge     | Last build only. |
| `kube_state_graph_clusters_observed`                       | gauge     | |
| `kube_state_graph_upstream_query_duration_seconds{query}`  | histogram | |
| `kube_state_graph_upstream_query_failures_total{query}`    | counter   | |
| `kube_state_graph_http_requests_total{path,status}`        | counter   | |
| `kube_state_graph_auth_rejected_total{reason}`             | counter   | `missing` (no header) / `invalid` (unknown key). |

## Recommended alerts

```yaml
- alert: KubeStateGraphUpstreamErrorRate
  expr: |
    sum(rate(kube_state_graph_upstream_query_failures_total[5m]))
      / sum(rate(kube_state_graph_upstream_query_duration_seconds_count[5m])) > 0.05
  for: 10m
  labels: {severity: warning}

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
- v1 ships **no in-process result cache**. Upstream PromQL load scales linearly with HTTP traffic; ETag-based revalidation is the only client-side amortisation. A future cache mechanism for distributed deployment is anticipated — until it lands, plan capacity around `requests/s × build_cost`.
- Recommended Pod resource limits: CPU 500m / Memory 512Mi for clusters under
  the v1 ceiling. Scale up if profiling shows GC pressure.

## API-key authentication

The server enforces an `X-API-Key` header on `/v1/*` and `/debug/*` whenever
keys are configured. Health (`/livez`, `/readyz`), Prometheus scrape
(`/metrics`), and the OpenAPI / Scalar UI routes are exempt.

### Recommended deployment

1. Create a `Secret` holding accepted keys (one per line, `#` comments allowed):

   ```yaml
   apiVersion: v1
   kind: Secret
   metadata:
     name: kube-state-graph-api-keys
   type: Opaque
   stringData:
     keys: |
       op-team-2026-q2
       grafana-readonly-2026-q2
   ```

2. Mount it on the API-server Deployment and point the server at the file:

   ```yaml
   volumes:
     - name: api-keys
       secret:
         secretName: kube-state-graph-api-keys
         defaultMode: 0o400
   containers:
     - name: server
       env:
         - name: KSG_API_KEYS_FILE
           value: /etc/kube-state-graph/api-keys/keys
         - name: KSG_API_KEYS_RELOAD_INTERVAL
           value: 30s
       volumeMounts:
         - name: api-keys
           mountPath: /etc/kube-state-graph/api-keys
           readOnly: true
   ```

   `local/kind/manifests/05-api-key-secret.yaml` and
   `local/kind/manifests/30-api-server.yaml` show the same wiring for the dev
   rig.

### Rotation

- Edit the `Secret` and `kubectl apply` it. Kubelet propagates the new content
  to the mounted volume (~60 s typical sync). The server re-reads the file on
  the configured `KSG_API_KEYS_RELOAD_INTERVAL` cadence (default 30 s) and
  atomically swaps the active key set. **No Pod restart required.**
- Worst-case rotation latency ≈ kubelet sync + reload interval ≈ ~90 s.
- To force an immediate read, restart the Pod (`kubectl rollout restart`).

### Monitoring

- Alert on `rate(kube_state_graph_auth_rejected_total[5m])` rising sharply —
  signals an out-of-date client, scraper, or rotation gone wrong.
- The header value is **never logged**. Logs include the request path, status,
  and the auth outcome only as a numeric status code on the existing HTTP
  request line.

### Caveats

- `/metrics` is intentionally open. Gate it via `NetworkPolicy` (allow only
  the Prometheus ServiceAccount), or expose it on a separate listen address
  bound to localhost / cluster-internal only.
- Static keys provide a coarse server-side gate. Per-caller scoping, OAuth2,
  or mTLS are not implemented; layer those at a reverse proxy if needed.

## Tuning notes

- Increase `--build-concurrency` if `kube_state_graph_build_rejected_total{reason="capacity"}`
  ticks during normal traffic.
- Increase `--build-timeout` only if upstream PromQL is slow; the default 15 s
  is generous for ≤ 5 k pods.
- Set `--clusters-allowlist` whenever the centralised VictoriaMetrics holds
  more clusters than this server should expose; allowlist injection bounds
  the upstream scan cost regardless of caller filters.
