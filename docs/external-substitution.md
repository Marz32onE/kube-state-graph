# External-name substitution (`KSG_EXTERNAL_NAME_PATTERN`)

Service-graph metrics carry `client` and `server` string labels in addition to
the pod-UID labels. For RPCs whose remote endpoint is not a pod (third-party
HTTP APIs, managed databases, message brokers, …), the pod-UID label is empty
and the human-readable label is the only signal that names the dependency.

Setting `KSG_EXTERNAL_NAME_PATTERN` (or `--external-name-pattern`) to a
substring tells the API server: when the `client` (or `server`) label value
contains this substring, treat that endpoint as an `external` graph node and
use the label value verbatim as its display name.

## Recommended values

| Pattern | Captures                                          | Typical use case |
|---------|---------------------------------------------------|------------------|
| `://`   | `http://...`, `https://...`, `redis://...`        | Most deployments — URL-shaped externals. |
| `@`     | `user@host`, `mysql@db.example.com`               | Username-shaped externals. |
| `:`     | `host:port`                                       | Port-shaped externals (use cautiously — false positives possible). |

## Behaviour

```
for each service-graph series, for endpoint side ∈ {client, server}:
  let v = the series' `client` or `server` label value
  if KSG_EXTERNAL_NAME_PATTERN != "" and contains(v, KSG_EXTERNAL_NAME_PATTERN):
    → external node {id="external/<v>", name="<v>", type="external", labels={pattern: "<configured>"}}
  else:
    → pod-UID resolution via topology
```

Per-endpoint independent: a single edge can be pod→pod, pod→external,
external→pod, or external→external. Edge `type` stays `pod-calls-pod`.

## Edge cluster labels

`pod-calls-pod` edges carry a single `labels.cluster` whose value is the
**trace source / client-side** cluster — the cluster that produced the
service-graph metric. The label is only emitted when the **client** side
resolves to a pod; when the client side is an external endpoint, the
`cluster` key is omitted entirely (external endpoints are not cluster-scoped).

The remote (server-side) cluster is **not** stamped on the edge — it is
recovered from the resolved target node's own `labels.cluster`. Consumers
detect cross-cluster status by comparing the edge's source-node and
target-node `labels.cluster` (both nodes are guaranteed to be present in the
same response).

## Examples

```
client="http://api.example.com", server="checkout"
  pattern="://" → source = external/http://api.example.com (type=external)
                  target = cluster-alpha/<uid> (type=pod)

client="checkout", server="https://payments.partner.example/api"
  pattern="://" → source = cluster-alpha/<uid>
                  target = external/https://payments.partner.example/api

client="http://api.example.com", server="https://payments.partner.example/api"
  pattern="://" → both endpoints external

client="checkout", server="cart"
  pattern="://" → no match either side; pod-UID resolution applies
```

## Limitations

- v1 supports a single substring; no regex, no multi-pattern. See the design
  doc Open Questions for v1.x evolution.
- Verbatim names mean two producers using different casing or trailing slashes
  produce different external nodes. Normalise upstream if that matters.
