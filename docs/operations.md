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
- v1 ships **no in-process result cache**. Upstream PromQL load scales linearly with HTTP traffic. A future cache mechanism for distributed deployment is anticipated — until it lands, plan capacity around `requests/s × build_cost`.
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
| Server (`GET /v1/...`) | `http.request.method`, `http.route`, `http.response.status_code` |
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

## Exporter compatibility contract

`kube-state-graph` consumes a small, fixed set of upstream metric families. By default it expects stock kube-state-metrics naming. Deployments that re-publish the same series under an organisational prefix (a fork of KSM, a custom exporter, a multi-tenant pipeline) can configure a single additive prefix without forking the API server.

### What is configurable

| Knob | Env var | Flag | Default | Notes |
|------|---------|------|---------|-------|
| Metric-name prefix | `KSG_METRIC_PREFIX` | `--metric-prefix` | empty | Prepended verbatim to every kube-state-metrics-shaped series the topology reader queries. Trailing underscore is the operator's responsibility — none is injected. |

Example: `KSG_METRIC_PREFIX=o11y_` causes the topology reader to query `o11y_kube_pod_info`, `o11y_kube_node_info`, etc. instead of the stock names.

Validation: the prefix must match the Prometheus metric-name charset `^[a-zA-Z_:][a-zA-Z0-9_:]*$` when non-empty. Invalid values fail server startup with an error mentioning `metric-prefix`.

### What is NOT configurable

The prefix targets the metric-name **prefix** only. Everything else is a fixed contract a compatible exporter MUST honour:

- **Metric-name suffix**: the topology reader queries the six families listed below (after stripping the prefix). An exporter that exposes the same data under a different suffix (e.g. `myorg_pods` instead of `myorg_kube_pod_info`) is NOT supported by the current knob.
- **Label-name set**: each family must publish the labels the reader joins on. See the per-family list below.

| Query identifier | Series the reader queries (with prefix `P`) | Required labels |
|------------------|---------------------------------------------|-----------------|
| `kube_pod_info` | `Pkube_pod_info` | `cluster`, `namespace`, `pod`, `uid`, `node`, optional `pod_ip` |
| `kube_node_info` | `Pkube_node_info` | `cluster`, `node` |
| `kube_node_status_addresses` | `Pkube_node_status_addresses{type="ExternalIP"}` | `cluster`, `node`, `type`, `address` |
| `kube_pod_spec_volumes_persistentvolumeclaims_info` | `Pkube_pod_spec_volumes_persistentvolumeclaims_info` | `cluster`, `namespace`, `pod`, `persistentvolumeclaim` (or `claim_name`), optional `volume` |
| `kube_node_labels` | `Pkube_node_labels` | `cluster`, `node`, plus any `label_*` flattened Kubernetes labels |
| `cluster_discovery` (`/v1/clusters`) | `group by (cluster) (last_over_time(Pkube_node_info[1h]))` | `cluster` |

### Out of scope for the prefix knob

The configurable prefix is **NOT** applied to:

- **`traces_service_graph_request_total`** — produced by a different exporter family (Grafana Alloy / Tempo's `servicegraph` connector), so it is unlikely to share an org-wide kube-state-metrics prefix. A separate knob can ship in a follow-up change if a real deployment ever surfaces.
- **`up{}`** — the Prometheus-native readiness probe is universal; prefixing it would break `/readyz` against any standard Prometheus / VictoriaMetrics deployment.

### Service-graph metric label contract

`traces_service_graph_request_total` is produced by Grafana Alloy /
Tempo's `servicegraph` connector, not by kube-state-metrics. Its label set
is summarised below; the K8s pod-UID dimensions are **RECOMMENDED** but no
longer hard-required for an edge to appear in `/v1/graph`.

| Label | Required? | Notes |
|-------|-----------|-------|
| `client`, `server` | yes (at least one per side) | Human-readable endpoint name. Used as the substitution input for `KSG_EXTERNAL_NAME_PATTERN` and as the fallback identity when the corresponding pod-UID dimension is empty. |
| `client_k8s_pod_uid`, `server_k8s_pod_uid` | recommended | When populated, the reader resolves the endpoint to a pod via the global pod-UID index. When empty and the corresponding `client`/`server` label is non-empty, the endpoint surfaces as `external/<label>` (D27 missing-UID fallback). When BOTH the UID and the label are empty for an endpoint, the edge is dropped. |
| `cluster` | yes | Trace-source / client-side cluster; required for the edge's `labels.cluster` field when the client side resolves to a pod. |
| `client_k8s_namespace_name`, `server_k8s_namespace_name` | optional | Carried to synth pods when the UID-based lookup misses topology. |

A sudden bloom of `type="external"` nodes whose names look like internal
workloads is a signal to investigate the trace pipeline (Beyla resource
detector, Alloy `k8sattributes` processor) — not the API. Before D27, such
endpoints were silently dropped; the visible fallback is intentional.

### When the prefix knob is not enough

If your custom exporter diverges on the metric-name suffix (not just the prefix) or on the label-name set, the current single-prefix knob will not suffice. Open an issue describing the exporter; per-metric overrides or full label remapping can be added additively in a future revision (see design.md D26 alternatives).

## Tuning notes

- Increase `--build-timeout` only if upstream PromQL is slow; the default 15 s is generous for ≤ 5 k pods.
- Increase `--api-timeout` only if `/v1/clusters` discovery or `/readyz` probe is observed to time out under healthy upstream conditions; default 5 s is generous for both.
- Tune VictoriaMetrics search limits (`-search.maxQueryDuration`, `-search.maxPointsPerTimeseries`, `-search.maxSamplesPerQuery`) when the upstream holds large multi-cluster fleets — KSG does not bound query cost itself.
