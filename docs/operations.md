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

- Bounded query cost is delegated to upstream VictoriaMetrics search limits (`-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`, `-search.maxSamplesPerQuery`). Tune these on VM, not on KSG.
- v1 ships **no in-process result cache**. Upstream PromQL load scales linearly with HTTP traffic; ETag-based revalidation is the only client-side amortisation. A future cache mechanism for distributed deployment is anticipated — until it lands, plan capacity around `requests/s × build_cost`.
- Concurrency is delegated to HPA + Pod resource limits. Recommended starting point: CPU 500m / Memory 512Mi per replica; HPA target on CPU 60% or p95 build latency. Scale up if profiling shows GC pressure or upstream contention.

## API-key authentication

The server enforces an `X-API-Key` header on `/v1/*` whenever keys are
configured. Health (`/livez`, `/readyz`), Prometheus scrape (`/metrics`), and
the OpenAPI / Scalar UI routes are exempt.

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

## OpenTelemetry tracing and logging

`kube-state-graph` ships an OpenTelemetry pipeline that emits **traces** and **structured logs** alongside the existing Prometheus self-metrics. The pipeline is **disabled by default** and configured **only** through OTel-standard environment variables — there are no `--otlp-*` CLI flags to keep operator workflow consistent with the rest of an OTel-instrumented fleet.

### Enabling

Set the OTLP endpoint:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317
OTEL_EXPORTER_OTLP_PROTOCOL=grpc            # or http/protobuf
OTEL_EXPORTER_OTLP_INSECURE=true            # plain text gRPC; flip for TLS
OTEL_SERVICE_NAME=kube-state-graph          # default if unset
OTEL_RESOURCE_ATTRIBUTES=deployment.environment=prod,cluster=prod-eu1
OTEL_TRACES_SAMPLER=parentbased_traceidratio
OTEL_TRACES_SAMPLER_ARG=0.05                # 5 % head sampling
```

When `OTEL_EXPORTER_OTLP_ENDPOINT` (and the per-signal `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` / `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` overrides) is unset, the binary installs no-op tracer and logger providers so there is zero export overhead and zero new background goroutines.

### Span topology

Every `/v1/*` request produces this tree (probes, `/metrics`, and `/docs/*` are excluded from tracing on purpose):

```
GET /v1/graph                               (otelgin server span)
└─ kube-state-graph.build                   (Builder.Build)
   ├─ prometheus.query  (kube_pod_info)     (errgroup leg)
   ├─ prometheus.query  (kube_node_info)
   ├─ prometheus.query  (kube_node_status_addresses)
   ├─ prometheus.query  (kube_pod_spec_volumes_persistentvolumeclaims_info)
   ├─ prometheus.query  (kube_node_labels)
   └─ prometheus.query  (traces_service_graph_request_total)
└─ kube-state-graph.project                 (filter / cluster scope / traversal)
└─ kube-state-graph.serialise               (Cytoscape | nodegraph)
```

Selected attributes:

| Span | Attributes |
|------|------------|
| Server (`GET /v1/...`) | `http.request.method`, `http.route`, `http.response.status_code`, `kube_state_graph.etag` |
| `kube-state-graph.build` | `kube_state_graph.window_seconds`, `kube_state_graph.end_unix`, `kube_state_graph.cluster_count`, `graph.node.count`, `graph.edge.count`, `kube_state_graph.cross_cluster_edges` |
| `prometheus.query` | `db.system=prometheus`, `db.statement=<rendered PromQL>`, `kube_state_graph.query_name`, `kube_state_graph.result_series_count` |
| `kube-state-graph.project` | `graph.node.count`, `graph.edge.count` (post-filter) |
| `kube-state-graph.serialise` | `kube_state_graph.serialiser` (`cytoscape` or `nodegraph`), `graph.node.count`, `graph.edge.count` |

The W3C `traceparent` header is honoured on inbound requests (the resulting server span chains under the caller's trace) and propagated automatically on every outbound PromQL HTTP call via `otelhttp`.

### Log correlation

Logs use Go's standard `log/slog` and are fanned out to two sinks:

1. **stderr** — the existing JSON / text stream `kubectl logs` consumes; never depends on the collector being up.
2. **OTLP logs** — when `OTEL_EXPORTER_OTLP_ENDPOINT` is set, every record is mirrored to the configured collector via `otelslog`.

Both sinks include `trace_id` and `span_id` fields on every record emitted from a request-scoped `context.Context` carrying an active span. Records emitted before any span exists (startup banner, shutdown error) omit those fields.

### Secret redaction

The auth middleware **never** logs the value of a presented `X-API-Key`, including via the OTLP bridge. Header attributes auto-collected by `otelgin` are filtered to drop `Authorization` / `X-API-Key`. If your downstream collector applies attribute scrubbing, layer it on top — KSG already does the redaction at the source.

### Graceful shutdown

`SIGTERM` first drains the HTTP server (within the existing 10 s grace deadline), then calls `Shutdown` on the trace and log providers using the same deadline. If exports remain pending past the deadline, the process exits with a non-zero status and an `otlp shutdown timed out` line on stderr — the exporter does **not** extend `terminationGracePeriodSeconds`.

### Cost notes

- `db.statement` carries the full rendered PromQL. If your collector pipeline disallows logging query strings, strip it via a Collector processor or set `OTEL_TRACES_SAMPLER=always_off`.
- Default sampler is `parentbased_alwayson`; switch to `parentbased_traceidratio` for production fleets where every `/v1/graph` would otherwise emit a full trace.
- `/livez`, `/readyz`, `/metrics`, and `/docs/*` are intentionally not traced. A 50-replica deployment would otherwise emit hundreds of useless probe spans per second.

## Tuning notes

- Increase `--build-timeout` only if upstream PromQL is slow; the default 15 s is generous for ≤ 5 k pods.
- Increase `--api-timeout` only if `/v1/clusters` discovery or `/readyz` probe is observed to time out under healthy upstream conditions; default 5 s is generous for both.
- Tune VictoriaMetrics search limits (`-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`, `-search.maxSamplesPerQuery`) when the upstream holds large multi-cluster fleets — KSG does not bound query cost itself.
