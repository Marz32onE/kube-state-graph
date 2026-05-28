# Others-name substitution (`KSG_OTHERS_NAME_PATTERN`)

Service-graph metrics carry `client` and `server` string labels in addition to
the pod-UID labels. For RPCs whose remote endpoint is not a pod (third-party
HTTP APIs, managed databases, message brokers, …), the pod-UID label is empty
or arbitrary and the human-readable label is the only signal that names the
dependency.

Setting `KSG_OTHERS_NAME_PATTERN` (or `--others-name-pattern`) to a substring
tells the API server: when the `client` (or `server`) label value contains
this substring, treat that endpoint as an `others` graph node (operator-
declared third-party endpoint) and use the label value verbatim as its
display name.

> **See also:** the missing pod-UID human-label fallback (D27) produces
> `type=external` nodes when the producer dropped `client_k8s_pod_uid` or
> `server_k8s_pod_uid` but the human label survived. The `others/` and
> `external/` ID namespaces are **disjoint** — see "Others vs External"
> below.

## Recommended values

| Pattern | Captures                                          | Typical use case |
|---------|---------------------------------------------------|------------------|
| `://`   | `http://...`, `https://...`, `redis://...`        | Most deployments — URL-shaped third parties. |
| `@`     | `user@host`, `mysql@db.example.com`               | Username-shaped externals. |
| `:`     | `host:port`                                       | Port-shaped externals (use cautiously — false positives possible). |

## Behaviour

```
for each service-graph series, for endpoint side ∈ {client, server}:
  let v = the series' `client` or `server` label value
  if KSG_OTHERS_NAME_PATTERN != "" and contains(v, KSG_OTHERS_NAME_PATTERN):
    → others node {id="others/<v>", name="<v>", type="others", labels={pattern: "<configured>"}}
  else if pod_uid != "":
    → pod-UID resolution via topology (synth pod on miss)
  else if human_label != "":
    → external node {id="external/<v>", name="<v>", type="external", labels={}}  (D27 fallback)
  else:
    → edge dropped
```

Per-endpoint independent: a single edge can be pod→pod, pod→others,
others→pod, others→others, or any combination involving `external`.
Edge `type` stays `pod-calls-pod`.

## Edge cluster labels

`pod-calls-pod` edges carry a single `labels.cluster` whose value is the
**trace source / client-side** cluster — the cluster that produced the
service-graph metric. The label is only emitted when the **client** side
resolves to a pod; when the client side is a non-pod endpoint (`others`
via the pattern rule, or `external` via the missing-UID fallback), the
`cluster` key is omitted entirely (non-pod endpoints are not cluster-scoped).

The remote (server-side) cluster is **not** stamped on the edge — it is
recovered from the resolved target node's own `labels.cluster`. Consumers
detect cross-cluster status by comparing the edge's source-node and
target-node `labels.cluster` (both nodes are guaranteed to be present in the
same response).

## Examples

```
client="http://api.example.com", server="checkout"
  pattern="://" → source = others/http://api.example.com (type=others)
                  target = cluster-alpha/<uid>           (type=pod)

client="checkout", server="https://payments.partner.example/api"
  pattern="://" → source = cluster-alpha/<uid>
                  target = others/https://payments.partner.example/api

client="http://api.example.com", server="https://payments.partner.example/api"
  pattern="://" → both endpoints others

client="checkout", server="cart"
  pattern="://" → no match either side; pod-UID resolution applies
```

## Others vs External

Two distinct paths can produce a non-pod node:

| `type`     | ID prefix     | Trigger                                          | `labels` payload |
|------------|---------------|--------------------------------------------------|------------------|
| `others`   | `others/...`  | `KSG_OTHERS_NAME_PATTERN` matched the label      | `{ "pattern": "<configured>" }` |
| `external` | `external/...`| Pod UID empty AND human label non-empty (D27)    | `{}` (empty)     |

The two ID spaces and dedupe maps are **independent**. A sudden growth of
`type="external"` node count is the actionable signal that a producer
(typically Beyla / Alloy) dropped `k8s.pod.uid` resource attributes — the
disjoint type discriminates this from the steady-state operator-declared
`others` set.

When a label both matches the pattern and has an empty UID, the pattern rule
wins per the resolution order (1 → 3). The missing-UID fallback only fires
when the pattern does not match.

## Limitations

- v1 supports a single substring; no regex, no multi-pattern. See the design
  doc Open Questions for v1.x evolution.
- Verbatim names mean two producers using different casing or trailing slashes
  produce different `others` nodes. Normalise upstream if that matters.
