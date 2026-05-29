# Connection-string endpoint resolution

Service-graph metrics carry `client` and `server` string labels in addition to
the pod-UID labels. For an endpoint whose pod-UID label is **empty**, the
human-readable `client` / `server` label is the only signal that names the
remote. The API server inspects that label to decide what graph node the
endpoint becomes.

There is **no knob** for this — the previous `KSG_OTHERS_NAME_PATTERN` /
`--others-name-pattern` substring substitution is gone (D29). Connection-string
detection is hardcoded: an endpoint label is treated as a URL when it contains
the literal substring `://`, and the resolution below is applied to it.

> **See also:** the missing pod-UID human-label fallback (D27) produces
> `type=external` nodes when an endpoint label is **not** a `://`
> connection string. The `service/` (intra-cluster `<cluster>/<namespace>/...`),
> `others/`, and `external/` ID namespaces are **disjoint** — see "Service vs
> Others vs External" below.

## Resolution order (per endpoint side)

Each endpoint side (`client` and `server`) is resolved independently:

```
for endpoint side ∈ {client, server}:
  let v = the series' `client` or `server` label value
  if pod_uid != "":
    → pod-UID resolution via topology (synth pod on miss)   # unchanged
  else if contains(v, "://"):
    → parse v as a URL and apply the .svc DNS grammar below
  else if v != "":
    → external node {id="external/<v>", type="external", labels={}}  (D27 fallback)
  else:
    → edge dropped
```

The pod-UID branch is tried **first** — a populated `client_k8s_pod_uid` /
`server_k8s_pod_uid` always resolves to a pod (or a synth pod on a topology
miss) regardless of the label value. Connection-string parsing only applies
when the UID is empty.

## Connection-string (`://`) grammar

When the endpoint label is a `://` URL and the UID is empty, the host portion
of the URL is parsed. An optional `.svc.<cluster-domain>` suffix (e.g.
`.svc.cluster.local`) is stripped, and the remaining dotted labels are
interpreted as a Kubernetes in-cluster DNS name:

| Host shape (after stripping `.svc.<domain>`) | Interpreted as | Resolves to |
|----------------------------------------------|----------------|-------------|
| `<service>.<namespace>` (2 labels) | A Service DNS name | a `type="service"` node + on-demand `service-selects-pod` edges to every backing pod |
| `<pod>.<service>.<namespace>` (3 labels) | A headless-Service per-pod DNS name | the **real backing pod** (resolved via topology) |
| anything else / unresolvable | Not an in-cluster DNS name | an `others/<label>` node (`type="others"`, `labels={}`) |

### Service-level name (`<service>.<namespace>`)

A two-label host is the cluster-internal DNS name of a Service. It becomes a
dedicated node:

- `type="service"`
- `id = <cluster>/<namespace>/<service>`
- `labels = { cluster, namespace }`
- `ipaddress = [cluster_ip]` taken from `kube_service_info` — **unless** the
  Service is headless (`cluster_ip="None"`), in which case `ipaddress` is
  omitted.

The server then emits **`service-selects-pod`** edges (directed
service → pod, intra-cluster) from the service node to every pod currently
backing it. Backing pods are discovered from the EndpointSlice metrics:
`kube_endpointslice_endpoints` provides each endpoint's `targetref_*`
(the pod), and `kube_endpointslice_labels` carries
`label_kubernetes_io_service_name` so a slice can be joined back to its
owning Service.

### Headless per-pod name (`<pod>.<service>.<namespace>`)

A three-label host is the per-pod DNS name a headless Service hands out (one
A record per backing pod, e.g. for a StatefulSet). The leading label is the
pod name, so this resolves **directly to the real backing pod** via topology —
no intermediate `service` node is created for this edge endpoint.

### Unresolvable connection strings

A `://` label whose host does not fit the `.svc` grammar (an external URL such
as `https://payments.partner.example/api`, an IP-literal host, a single-label
host, etc.) becomes an `others` node:

- `type="others"`
- `id = others/<label>` (the verbatim label value)
- `labels = {}` — note the previous `pattern` key is **gone** (D29).

## Service vs Others vs External

Three distinct paths can produce a non-pod / non-node endpoint:

| `type`     | ID shape                       | Trigger                                                                 | `labels` payload          |
|------------|--------------------------------|-------------------------------------------------------------------------|---------------------------|
| `service`  | `<cluster>/<namespace>/<svc>`  | `://` label whose host is an in-cluster `<service>.<namespace>` name    | `{ cluster, namespace }`  |
| `others`   | `others/<label>`               | `://` label that is **not** an in-cluster DNS name (unresolvable URL)   | `{}` (empty)              |
| `external` | `external/<label>`             | UID empty AND label is **not** a `://` connection string (D27 fallback) | `{}` (empty)              |

The three ID spaces and dedupe maps are **independent**. The split is the
actionable signal:

- `service` nodes are first-class in-cluster Kubernetes Services, fully
  resolved (cluster_ip + backing pods).
- A sudden growth of `others` reflects more external-URL dependencies (or
  in-cluster URLs whose DNS shape KSG could not resolve — e.g. missing
  EndpointSlice allowlist labels; see below).
- A sudden growth of `type="external"` is the signal that a producer
  (typically Beyla / Alloy) dropped `k8s.pod.uid` resource attributes on
  endpoints that are **not** URL-shaped — distinct from the steady-state
  third-party `others` set.

## Edge cluster labels

`pod-calls-pod` edges carry a single `labels.cluster` whose value is the
**trace source / client-side** cluster — the cluster that produced the
service-graph metric. The label is only emitted when the **client** side
resolves to a pod; when the client side is a non-pod endpoint (`service`,
`others`, or `external`), the `cluster` key is omitted entirely (non-pod
endpoints are not cluster-scoped on the edge itself).

The remote (server-side) cluster is **not** stamped on the edge — it is
recovered from the resolved target node's own `labels.cluster`. Consumers
detect cross-cluster status by comparing the edge's source-node and
target-node `labels.cluster` (both nodes are guaranteed to be present in the
same response).

`service-selects-pod` edges are always intra-cluster (a Service and its
backing pods live in the same cluster) and directed service → pod.

## Examples

```
client_k8s_pod_uid="<uid>", server="http://checkout.shop.svc.cluster.local"
  → source = cluster-alpha/<uid>                    (type=pod, edge labels.cluster=cluster-alpha)
    target = cluster-alpha/shop/checkout            (type=service)
            + cluster-alpha/shop/checkout ──service-selects-pod──► each backing pod

client_k8s_pod_uid="<uid>", server="http://web-0.web.shop.svc.cluster.local"
  → headless per-pod name (3 labels) → target = cluster-alpha/<backing-pod-uid> (type=pod)

client_k8s_pod_uid="<uid>", server="https://payments.partner.example/api"
  → unresolvable "://" → target = others/https://payments.partner.example/api (type=others)

client="legacy-billing", server="checkout", server_k8s_pod_uid="<uid>"
  → client label not a "://" URL, client UID empty
    → source = external/legacy-billing (type=external, D27); edge labels.cluster omitted

client_k8s_pod_uid="<uid>", server_k8s_pod_uid="<uid>"
  → both UIDs present → pod-UID resolution both sides; "://" parsing not reached
```

## KSM configuration requirement

Service resolution depends on three kube-state-metrics families
(`kube_service_info`, `kube_endpointslice_endpoints`,
`kube_endpointslice_labels`) — see
[Operations → Exporter compatibility contract](operations.md#exporter-compatibility-contract)
for the full label sets and the `KSG_METRIC_PREFIX` interaction.

**Critical:** the slice → service join reads
`label_kubernetes_io_service_name`, which kube-state-metrics only emits when
its `--metric-labels-allowlist` includes
`endpointslices=[kubernetes.io/service-name]`. This label is **not** exposed by
default. Without it, KSG cannot join EndpointSlices back to their owning
Service, so a `<service>.<namespace>` connection string still produces a
`service` node but its `service-selects-pod` edges will be missing (or the
host may fall back to `others` if no `kube_service_info` series is found).

## Limitations

- Connection-string detection keys on the literal `://` substring only; labels
  that name a dependency without a scheme (`host:port`, `user@host`) are **not**
  treated as URLs — they follow the D27 `external` fallback instead.
- Verbatim `others/<label>` names mean two producers using different casing or
  trailing slashes produce different `others` nodes. Normalise upstream if that
  matters.
- The `.svc.<cluster-domain>` suffix is stripped heuristically; non-standard
  cluster domains resolve as long as the trailing `.svc.<domain>` is present.
