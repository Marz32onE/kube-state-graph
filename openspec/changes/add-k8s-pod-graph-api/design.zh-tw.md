## 背景

本 repository 只交付**graph API server** 這一個元件。其餘——centralised VictoriaMetrics、餵資料的 per-cluster scrape pipeline（`kube-state-metrics`、vmagent / Prometheus、客製化的 service-graph metrics source）、以及 Kind 為主的 integration-test harness——都視為外部依賴；在本 repo 僅以測試 scaffolding 形式存在。

API server 假設資料流已就緒：

```
cluster A: kube-state-metrics ──┐
           service-graph source ┤
                                 │  (vmagent / Prometheus
cluster B: kube-state-metrics ──┤   帶 external_labels:
           service-graph source ┤   { cluster: "<name>" })
                                 │
       ...                       ├──► centralised VictoriaMetrics ◄── Graph API server（本 repo）
                                 │                                     （Prometheus HTTP API client）
cluster N: kube-state-metrics ──┤
           service-graph source ─┘
```

- 每個 cluster 的 scrape pipeline 在 remote-write 到共用 VictoriaMetrics 之前，對 `kube-state-metrics` 與 service-graph metrics 一致套用 `cluster=<name>` external label。
- `kube-state-metrics` 匯出 `kube_pod_info{cluster=...,uid=...}`、`kube_node_info{cluster=...,node=...}`、`kube_node_status_addresses{cluster=...}`、`kube_pod_spec_volumes_persistentvolumeclaims_info{cluster=...}`、`kube_node_labels{cluster=...}` 等。
- 獨立的（repo 外）service-graph producer 會輸出帶 pod UID label 的 metrics，並同時帶 `client_cluster` 與 `server_cluster`，讓跨 cluster RPC 端到端保留：
  - `traces_service_graph_request_total{client_cluster, server_cluster, client_k8s_pod_uid, server_k8s_pod_uid, client_k8s_namespace_name, server_k8s_namespace_name, connection_type}`。
- API server 經 Prometheus HTTP API 從 VictoriaMetrics **依請求即時**讀取所需資料，範圍為呼叫端指定的時間區間。它不與任何 cluster 的 Kubernetes API server 通訊、不直接 scrape `kube-state-metrics`、也不連到 service-graph producer。

本 repo 的 integration-test harness（單一 Kind cluster、in-cluster VictoriaMetrics、合成 multi-cluster series 的 fake-fixtures producer）僅為了讓 CI 與開發者有可重現的目標。刻意**不**啟動多個 Kind cluster 或真實 per-cluster scrape pipeline——那屬於部署工作，不在本 repo。

對 API server 的約束：

- Go 1.22+ 標準庫 `log/slog` 記錄 log。
- Gin 做 HTTP routing。
- `github.com/prometheus/client_golang/api` 與 `.../api/v1` 做對外 query。
- `github.com/dgraph-io/ristretto/v2` 做 in-process cache。
- `golang.org/x/sync/singleflight` 合併請求。
- `golang.org/x/sync/errgroup` 與 `.../semaphore` 做 parallel fan-out 與併發上限。
- 無 Kubernetes client-go、無 informer、無直接 VictoriaMetrics SDK。
- 單一可設定的 upstream URL（centralised VictoriaMetrics 的 Prometheus 相容 endpoint）。

## 目標 / 非目標

**目標：**

- 交付 Go（Gin）HTTP server，在呼叫端指定的 `[start, end]` 時間區間內，對一或多個 Kubernetes cluster 回傳統一的 nodes-and-edges JSON，由 VictoriaMetrics 即時計算。
- 將**跨 cluster** RPC edge（`pod-calls-pod` 且 `client_cluster != server_cluster`）視為一等 graph element。
- 對 centralised VictoriaMetrics 發帶 `@` timestamp modifier 與 range-aware 函式（`last_over_time`、`rate`）的 PromQL，在記憶體內 join 範圍內所有 cluster 的結果集，組出 graph。
- 以分層 cache（HTTP `ETag`、`singleflight`、Ristretto）服務併發、相同 time-range 的查詢，讓共用 dashboard 的多位使用者在同一 time bucket 只攤平成一次 upstream fan-out——與 cluster / namespace / edge-type filter 組合無關。
- 以 `(cluster, pod-uid)` 複合鍵作為 pod node 的穩定 identity 與 pod-pod edge 的 join key；node 與 PVC ID 同樣為 cluster-scoped。
- 主要回應為 Cytoscape.js 形狀的 JSON，另提供 Grafana Node Graph 相容 route 供視覺驗證。
- 提供 cluster discovery（`GET /v1/clusters`），資料即時來自 VictoriaMetrics；以及靜態 edge-type catalogue（`GET /v1/edge-types`）。
- 提供 integration-test harness（單一 Kind、in-cluster VictoriaMetrics、fake-fixtures producer 產生 multi-cluster `kube_*` 與 `traces_service_graph_*` series、smoke script），證明 API server 回傳非空、格式正確的 multi-cluster graph，且含跨 cluster edge。

**非目標：**

- 實作客製 service-graph collector（Alloy / OTLP collector）。Harness 使用 fake-fixtures producer 直接寫入合約要求的 metrics。
- 營運、設定或強化 `kube-state-metrics` 或 VictoriaMetrics；它們是依賴，不是交付物。
- 直接對任何 cluster 的 Kubernetes API 通話；所有 cluster 事實經 metrics 讀取。
- HTTP API 上的 authentication、authorisation、multi-tenant isolation 或 TLS termination（假設由 reverse proxy 處理）。Per-cluster RBAC 亦不在範圍——經本 server 可讀到的每個 reachable cluster 一律同等可讀。
- Ingest traces；trace-derived metrics 在上游產生，API server 只讀結果 series。
- 即時 streaming 或 WebSocket API。
- 跨 process restart 持久化 cache entry；cache 僅 in-memory。
- 多實例分散式 cache（Redis、memcached）。v1 假設單實例部署。
- 以 graph database（Neo4j、Memgraph、ArangoDB）做 partial / traversal query；v1 以 in-memory adjacency 足夠。
- VictoriaMetrics multi-tenant（vmcluster `accountID:projectID`）routing。v1 以單租戶 centralised VM 搭配 `cluster` external label 為隔離模型；multi-tenancy 留作 v1.1 逃生閥。
- 在 integration-test harness 中啟動多個 Kind cluster或真實 per-cluster scrape pipeline。

## 決策

### D1. 單一 upstream：經 Prometheus API 的 centralised VictoriaMetrics

Server 接受一個 upstream URL（`--prom-url`，預設 `http://localhost:8428`）指向 centralised VictoriaMetrics 的 Prometheus 相容 endpoint。所有輸入（任意 cluster 的 kube-state-metrics series 與 service-graph series）都從這一個 backend 查詢。

Multi-cluster 以 **label** 區辨：每條 series 帶 `cluster=<name>`（topology）或 `client_cluster=<name>` / `server_cluster=<name>`（service-graph）。API server 不知道 per-cluster URL。

- 理由：符合既有 centralised observability 部署；N 個 reader 收斂成單一 client；單次 PromQL 可涵蓋所有 cluster。
- 曾考慮的替代方案：
  - 每 cluster 一個 upstream、由 API server fan-out（否決——重複連線邏輯，且跨 cluster edge 兩端會落在兩份 query 結果，難以解析）。
  - VictoriaMetrics multi-tenant（每 cluster `accountID:projectID`）（否決——需 vmcluster、營運較重，且單一 PromQL 跨 cluster edge 較難；v1.1 逃生閥）。
  - 經 client-go informer 直接存取 Kubernetes API（否決——informer 只知所 watch cluster 的*當前*狀態，無法回答歷史 time-range，且帶回 N 路 watch 與 per-cluster RBAC）。

### D2. 依時間區間即時建置，無 server-side snapshot

每次 `GET /v1/graph?start=...&end=...` 都為所給視窗重新建置 multi-cluster graph：

1. 解析並驗證 `start` / `end`。
2. 計算 canonical cache key（D5）。
3. 查 cache（D6）。命中則從 cache 服務（`X-Cache: HIT`）。
4. 未命中則進入 `singleflight.Do(key)`，讓併發相同請求收斂成一次 build。
5. 在 singleflight 內，經 `errgroup.WithContext` 對 centralised VictoriaMetrics parallel 執行所需 PromQL，在記憶體內 join 各 cluster 結果集，產出全域 multi-cluster `Graph`，並填入 cache。
6. 對 cached `Graph` 套用 filter（`cluster`、`namespace`、`node`、`edge_type`）與 traversal pruning（`root`、`depth`、`direction`），再序列化成請求格式（Cytoscape.js 或 Grafana Node Graph）回傳。

無背景 `Snapshotter`、無 `atomic.Pointer[Graph]`、無固定 refresh interval、無 `POST /admin/refresh`。

- 理由：API 合約是 time-ranged，server 不能特權化單一「當前」snapshot；cache 讓同一視窗重複讀取便宜；設計上易水平擴展（v1 僅單實例，但無需移除的共享可變狀態）。
- 替代方案：定期 snapshot（否決——與 time-travel 查詢不相容）；完全無 cache 每請求重建（否決——N 個 dashboard 分頁 = N 倍 upstream 負載）。

### D3. Pod、node、PVC identity 為 cluster-scoped

`Graph` ID：

- Pod node：`(cluster, pod-uid)`。序列化 ID 形式為 `<cluster>/<pod-uid>`。
- K8s node：`(cluster, node-name)`。序列化為 `<cluster>/<node-name>`。
- PVC node：`(cluster, namespace, pvc-name)`。序列化為 `<cluster>/<namespace>/<pvc-name>`。

Edge endpoint 引用上述複合 ID。

- 理由：pod UID 實務上多為 UUIDv4 全域唯一，但與 cluster 名併用幾乎無成本、ID 在 log 與 JSON 自解釋，且合約不依賴 UUID 碰撞機率。Node 名與 PVC 名**不**跨 cluster 全域唯一（例如 `worker-0`）——該處 cluster scoping 為必須。
- 替代：僅 pod UID（否決——與需 cluster 的 node/PVC 不一致）；`cluster.namespace.name` 三合一表示 pod（否決——pod 重啟會碰撞；service-graph 在視窗內仍可能引用舊 UID）。

### D4. Edge types

Edge 分類為 typed category：

- `pod-runs-on-node`（僅 intra-cluster）：由時間區間內評估的 `kube_pod_info{node=..., cluster=...}` 衍生。
- `pod-mounts-pvc`（僅 intra-cluster）：由 join `kube_pod_spec_volumes_persistentvolumeclaims_info` 與 pod 所在 node，且限單一 cluster。
- `pod-calls-pod`（intra-cluster **或 cross-cluster**）：由 `rate(traces_service_graph_request_total[<window>]) @ <end>` 非零 rate，join 回 `(client_cluster, client_k8s_pod_uid)` 與 `(server_cluster, server_k8s_pod_uid)`。Edge 一律帶 `labels.client_cluster` 與 `labels.server_cluster`；是否 cross-cluster 由消費端比對兩字串（依 D9 嚴格字串規則，`labels` 內不放 boolean flag）。

每條 edge 帶 `type`、`source`、`target`，以及型別專屬 `attrs`（序列化 JSON 形狀見 D9）。

- 理由：消費端可按 edge type filter；概念上對齊 Tempo `serviceGraph`；跨 cluster 流量為一等概念。
- 替代：無型別 edge + 自由 form attributes map（否決——難驗證與渲染）。
- 新 edge types 僅可 additive；既有 `type` 字串永不改作他用（見 D14）。

### D5. 時間區間語意與 cache-key bucketing

`start` 與 `end` 為必填 query 參數，格式為 RFC 3339 或 Unix 秒。Server 強制：

- `end > start`。
- `end - start <= --max-window`（預設 `24h`）。
- `end <= now + --max-skew`（預設 `1m`）。

為提升 cache 效果，兩個時間戳在形成 cache key 前會 **bucket**。Bucket 大小依 `end` 的 time class：

| Time class | 對 `end` 的判斷 | Bucket size | Cache TTL |
|-----------|----------------|-------------|-----------|
| `live` | `end >= now - 1m` | 15 s | 30 s |
| `recent` | `end >= now - 1d` | 60 s | 5 min |
| `historical` | `end >= now - 7d` | 5 min | 1 h |
| `frozen` | `end < now - 7d` | 5 min | 24 h |

`start` 與 `end` 皆對齊到 bucket 邊界；upstream PromQL 使用 **bucket 後**的時間戳，使落在同一 bucket 的呼叫者得到 bit-stable 結果。回應帶 bucket 對齊的 `start_actual` / `end_actual`。

Cache key **僅含時間**，涵蓋完整 multi-cluster graph：

```
key = xxhash(canonical_json({
  start_bucket,
  end_bucket,
  bucket_size
}))
```

Filter 參數（`cluster`、`namespace`、`node`、`edge_type`、`root`、`depth`、`direction`）與 `format` **不**進入 cache key；在回應階段對 cached 全域 multi-cluster `Graph` 做 projection（D6、D7）。

- 理由：filter 組合會碎裂 cache；multi-cluster 下若把 `cluster` 納入 key，cache footprint 會乘上不同 cluster-filter 組合數。僅時間 keying 讓同一視窗的所有 filter 請求共用一個 cache entry。
- 為何不在 PromQL 階段 filter：VictoriaMetrics 仍會掃 index；label selector 縮小網路 payload，不必然降低 upstream 評估成本。完整 multi-cluster graph 在目標規模（≤ 5k pods × ≤ 10 clusters ≈ 數十 MB）內可 cache 後再 projection。
- 無上限 cluster 數的緩解：可選 `--clusters-allowlist` 在所有 PromQL 注入 `cluster=~"a|b|c"`，無論 VM 內有多少 cluster 都可限制 upstream 成本。
- 替代：filter 進 cache key（否決——碎裂）；per-cluster cache entry（否決——破壞跨 cluster edge 且膨脹記憶體）；對原始時間戳 hash（否決——次秒漂移毀 hit rate）。

### D6. Cache 層：Ristretto + singleflight + ETag

三層協調：

1. **HTTP 層——`ETag` 與 `Cache-Control`。** 每個回應帶 `ETag: "<sha256 of body>"` 與依 D5 time class 導出的 `Cache-Control: public, max-age=<ttl-seconds>`。呼叫端可用 `If-None-Match` 短路 → server 回 `304 Not Modified` 無需重新序列化。
2. **Singleflight（`golang.org/x/sync/singleflight`）。** Key 與 Ristretto 相同的 time-only cache key。N 個併發相同請求收斂為一次 upstream fan-out；所有呼叫者共用同一 `Graph`。必須啟用。
3. **Ristretto（`github.com/dgraph-io/ristretto/v2`）。** Cost-based、sharded、低爭用 cache。Per-entry TTL（依 time class 變動）。預設 `MaxCost = 256 MiB`、`NumCounters = 1e6`、`BufferItems = 64`——皆可設定。每條 entry 的 cost = cached `Graph` 的近似 in-memory 大小（由 node + edge 數計算，非序列化 JSON）。

**Cache 存的是 typed `*Graph` Go struct**（該視窗的完整 multi-cluster graph）——不是序列化 JSON。每個請求：

1. 載入 cached `*Graph`（或 miss 時在 singleflight 下 build）。
2. 對共享 `Graph` **唯讀**套用 filter spec（`cluster`、`namespace`、`node`、`edge_type`）與 traversal pruning（`root`、`depth`、`direction`）。Filter+prune 回傳輕量 view，非複本。
3. 將 view 序列成請求的 `format`（Cytoscape.js 或 Grafana Node Graph）。
4. 由序列化 body 計算 `ETag` 並寫回應。

因 waiter 永遠從回傳的 `*Graph` 讀取（非後續 `cache.Get`），Ristretto write 的最終可見性不會造成 re-build race。

可選小型 **L2 cache** 存序列化回應，key 為 `(time_bucket_key, filter_hash, format)`，TTL 階梯同 L1。v1 除非 profiling 顯示序列化+ETag 很熱，否則略過；文件標為 v1.1 逃生閥。

小型 `Cache` interface（Get / Set / Delete / Stats / Close）包裝 Ristretto，便於替換實作。

- 選 Ristretto 而非 `hashicorp/golang-lru/v2`：需要 per-entry 變動 TTL；sharded 內部減少 dashboard 併發讀時單一 mutex 瓶頸；W-TinyLFU + Doorkeeper 抗單次歷史查詢的 scan flood；cost-based budget 提供真實記憶體上限。

### D7. Filtering、cluster scoping、partial-graph traversal

`GET /v1/graph` 除必填 `start` / `end` 外接受：

- `?cluster=<name>`——可重複；僅保留 `cluster` 在集合內的 node。跨 cluster edge 若一端在集合內、一端在外，**仍保留**（遠端 endpoint 因 cached `*Graph` 含所有 cluster 而可正確解析）；遠端 node 一併保留（自有 `labels.cluster`）。跨 cluster 語意由 edge 上 `labels.client_cluster` 與 `labels.server_cluster` 表達。`cluster` 設成未知值不算錯誤——該名稱僅得到空結果。
- `?namespace=<ns>`——可重複；限制 pod / PVC node 的 `namespace` 在集合內。namespace 值可跨 cluster 比對；與 `?cluster=` 併用可縮到單一 cluster 的 namespace。
- `?node=<node-name>`——可重複；限制 K8s node 名。若名稱跨 cluster 不唯一，請與 `?cluster=` 併用。
- `?edge_type=<type>`——可重複；僅保留該 edge types。若某型別在目前 `Graph` 無 edge，靜默略過（無錯誤、僅空）。
- `?root=<id>&depth=<n>&direction=in|out|both`——partial-graph traversal：自複合 ID（`<cluster>/<pod-uid>` 或 `<cluster>/<node-name>`）做 BFS，以 `depth` 為界（預設 2，最大 6）。

Filtering **在回應階段**對 cached `*Graph` 套用，不重查 upstream。PromQL 永遠抓取範圍內所有 cluster 的完整視窗（受 `--clusters-allowlist` 限制）；cached `*Graph` 為所有 filtered view 的共用基底。

- 理由：cache key 小、hit 率高；filter+序列化在典型 graph 大小為微秒級。
- 空 filter ⇒ 該時間區間的完整 multi-cluster graph。
- Filter 在型別之間為 AND、同型別多值為 OR。
- Traversal 先依 `root`/`depth`/`direction` prune，再對結果套用 `cluster` / `namespace` / `node` / `edge_type`。

### D8. Producer 合約與 integration-test producer

API server 依賴 **metric contract**，不依賴特定 producer。合約：

```
# Topology（per cluster）
kube_pod_info{cluster, namespace, pod, uid, node, ...}
kube_node_info{cluster, node, ...}
kube_node_status_addresses{cluster, node, type="ExternalIP", address=...}
kube_pod_spec_volumes_persistentvolumeclaims_info{cluster, namespace, pod, volume, claim_name, ...}
kube_node_labels{cluster, node, label_*=...}

# Service graph（可能 cross-cluster）
traces_service_graph_request_total{
  client, server,
  client_cluster, server_cluster,
  client_k8s_pod_uid, server_k8s_pod_uid,
  client_k8s_namespace_name, server_k8s_namespace_name,
  connection_type="virtual_node|messaging_system|database"
}
traces_service_graph_request_failed_total{ ...same labels... }
traces_service_graph_request_server_seconds_bucket{ ...same labels..., le="..." }
```

`cluster` external label 由各 cluster 的 scrape pipeline（`vmagent` / Prometheus `external_labels`）套用。對 service-graph metrics，producer（OTel Collector 配 `servicegraph` connector + `k8sattributes` processor，`dimensions: [k8s.pod.uid, k8s.namespace.name]`）負責同時輸出 `client_cluster` 與 `server_cluster`。執行時皆不屬 API server 職責。

**Integration-test producer——fake fixtures program：**

`tests/harness/vm-fixtures/` 內的 Go program，暴露 `/metrics`，由 VictoriaMetrics scrape，內容為手刻 multi-cluster series，符合上列合約。包含：

- 多個 synthetic cluster（例如 `cluster-alpha`、`cluster-beta`）的 `kube_pod_info` / `kube_node_info` / `kube_pod_spec_volumes_persistentvolumeclaims_info` / `kube_node_labels`。
- 至少一條**跨 cluster** 的 `traces_service_graph_request_total`（`client_cluster=cluster-alpha, server_cluster=cluster-beta`），供 smoke script 驗證。

設定經 repo 內 YAML fixture，情境可重現。無真實 `kube-state-metrics`、無 OTLP collector、無 OTel SDK、無 traces。

- 理由：被測單位是 API server；直接合成 metric contract 讓測試聚焦 join / build / HTTP；multi-cluster 僅需不同 `cluster` label。
- Fixtures **必須**輸出上列精確 label set，生產換成真實 producer 時僅為設定變更。

**否決：** 多 Kind + 真實 `kube-state-metrics`（成本雙倍、筆電資源、驗證與 fixtures 相同合約）；synthetic OTLP + collector（完整 pipeline 在生產上游）；`telemetrygen`（無 parent/child，servicegraph 無法配對）；OpenTelemetry Demo（過重）。

### D9. 輸出格式：Cytoscape.js JSON，與 Grafana Node Graph 相容

**Node 與 edge schema（canonical，兩種格式共用）：**

| Object | Field | Type | 來源 / 說明 |
|---|---|---|---|
| Node | `id` | string | Cluster-scoped 複合。Pod：`<cluster>/<pod-uid>`。Node：`<cluster>/<node-name>`。PVC：`<cluster>/<namespace>/<claim>`。**External**：`external/<label-value>`（無 cluster）。 |
| Node | `name` | string | Pod 名 / node 名 / PVC claim 名。External node 為 `client` 或 `server` label 原文。Grafana panel 顯示用。 |
| Node | `type` | string | `"pod"`、`"node"`、`"pvc"`、`"external"` 之一。 |
| Node | `labels` | `map[string]string` | 僅字串 key/value。Pod/node/PVC 必含 `cluster`、pods/PVC 含 `namespace`、pods 含 `node`（cluster-scoped node ID）、node 在已知時含 `external_ip`。K8s pod/node label 原文攤平。**External** 僅最少 labels（設定之 `pattern` 值在 `pattern` key）；**不**帶 `cluster`。新 key 僅 additive。 |
| Edge | `id` | string | 自固定 namespace UUID 與 canonical tuple `(type, source, target)` 導出之 UUIDv5。同 edge 重建 ID 穩定；符合 RFC 4122。 |
| Edge | `type` | string | `/v1/edge-types` 註冊型別之一（如 `"pod-runs-on-node"`、`"pod-mounts-pvc"`、`"pod-calls-pod"`）。 |
| Edge | `source` / `target` | string | 同回應內存在之 node `id`。 |
| Edge | `labels` | `map[string]string` | `pod-calls-pod`：`client_cluster`、`server_cluster`。`pod-mounts-pvc`：`claim_name`、`storage_class`。`pod-runs-on-node`：`scheduled_at`。新 key additive。 |

**嚴格字串型別。** Node 與 edge 的 `labels` 皆為 `map[string]string`。非字串資料（數值 edge metrics 如 `rate`、`p99_ms`、`error_rate`；boolean 如 `cross_cluster`、`ghost`）**延後**到未來 typed struct field。v1 不在 `labels` 內用 `"true"`/`"false"` 字串編 boolean；跨 cluster 由消費端比對 `pod-calls-pod` 上 `labels.client_cluster` 與 `labels.server_cluster`。

主要 `GET /v1/graph` 回應為 **Cytoscape.js** 形狀 JSON（結構同英文 `design.md` 內範例）。

第二條 route `GET /v1/graph/nodegraph` 將相同資料投影成 **Grafana Node Graph** API datasource 形狀（平行 `nodes_fields`/`nodes` 與 `edges_fields`/`edges`）。對應：Node `name` → `title`；`labels.cluster` ` · ` `labels.namespace`（或僅 cluster）→ `subTitle`；Node `type` → `mainStat`；Edge `type` → `mainStat`；Edge `secondaryStat` v1 留空。

- 理由：單一 canonical schema 驅動兩種格式；未來欄位可加在 `labels` 以維持非破壞性。
- Edge `id` 用 UUIDv5：確定性、golden test 穩定、與可讀 `(source, target, type)` 解耦。

### D10. 以 `log/slog`、JSON handler 記錄 log

`slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: ...}))` 為預設；等級可設定。每個 HTTP 請求一行 structured log：method、path、status、duration、request ID、套用之 `cluster` filter、`cache_status`（`hit | miss | coalesced`）。

每次 build 另有一行：`slog.Info("graph built", ...)`。

### D11. 實作要點（v1 必須）

- **封閉 graph node types**：Go interface `GraphNode` 與具體 `PodNode`、`NodeNode`、`PVCNode`；canonical 欄位供 D9 serializer 使用。`cluster` 放在 `Labels()["cluster"]`，非 wire 上獨立欄位。
- **Pure join layer**：`Build(topology Topology, edges []ServiceGraphEdge, clustersAllowlist []string) *Graph` 為純函式，產出該視窗完整未 filter multi-cluster graph。單元測試針對此函式、無 HTTP/Prometheus。
- **Pure projection layer**：`Project(g *Graph, scope Scope) GraphView` 對不可變 `*Graph` 套用 filter 與 traversal，回傳唯讀 view；僅 pointer slice，不複製 node/edge struct。
- **Query registry**：PromQL 字串為具名常數，參數化 `<window>`、`<end>`、可選 `<clusters_allowlist>` fragment；parser 將 Prometheus `model.Vector` 映到 typed Go struct。
- **每個 metric family 一條 PromQL instant query**，在 bucket 化 `end` 以 `last_over_time` / `rate` 評估；query **不含** filter-derived selector，僅靜態 `--clusters-allowlist`。Client 端 parse Vector。
- **Parallel upstream fan-out**：`errgroup.WithContext`。Wall-clock ≈ 最慢查詢。
- **Per-build context timeout**（預設 15 s，可設定）。任一 sub-query 失敗則整次 build 中止，回 `503` 與 `Retry-After: 1`。
- **併發上限**：`golang.org/x/sync/semaphore`（預設 8 個併發 build），超出回 `503`。
- **Cache key**：canonical JSON 的 xxhash，單一 `uint64`。
- **Adjacency maps**：`Build()` 內建 forward/reverse `map[NodeID][]*Edge`；`Project()` traversal 重用。

### D12. Self-metrics 與可操作性

Server 暴露 `/metrics`（Prometheus exposition），至少含英文 `design.md` D12 所列 histogram/counter/gauge 系列名稱與 label（`kube_state_graph_*`）。

Health：`GET /livez` 行程活著即 200；`GET /readyz` 僅當便宜 upstream probe（`up{}` instant query，1 s timeout）成功為 200。

Operator：`DELETE /admin/cache` 清空 Ristretto；`GET /debug/last-queries` 在 `--enable-debug` 下回傳上次 build 的 query 與 redacted 摘要。

### D13. 測試層級

五層皆須在 archive 變更前存在：Unit（純 join/parse/project）、Component（`httptest.Server` mock Prometheus API）、Golden（`.golden.json`）、Property（invariants）、Integration（Kind + VM + smoke）。PR 觸及特定路徑時跑 integration，否則 nightly。理由與英文版相同。

### D14. 版本化

- 所有 HTTP route 前綴 `/v1/`。
- Body 頂層 `apiVersion: "v1"`。
- 新 edge types 與新 `attrs` 僅 additive；移除欄位為 v2 break。
- Producer 的 `connection_type` 映到穩定內部 enum。
- `cluster` label 值透傳為不透明字串；上游改名為呼叫端可見變更，非 API break。
- Cache key 形狀視為內部實作。

### D15. Edge-type discovery API

`GET /v1/edge-types` 回傳靜態 catalogue（結構同英文 `design.md` 內 JSON 範例）。無 upstream；不受時間或 filter 參數化。`Cache-Control: public, max-age=3600`；`ETag` 來自 registry compile-time hash。

### D16. v1 不用 graph database、不用 client-go informer

理由與「revisit triggers」同英文版：in-memory adjacency 在 v1 規模為微秒級；informer 無歷史 time-range。

### D17. Multi-cluster routing、discovery、cross-cluster edges

**Routing：** 皆以 query parameter 多選 `cluster`，非 path segment。理由：cross-cluster edge 自然跨多 cluster，單 cluster path 會導致語意矛盾。

**Discovery：** `GET /v1/clusters` 由 `group by (cluster) (kube_node_info)` 與可設定 lookback（`--cluster-discovery-lookback`，預設 `1h`）即時導出；60 s 固定 key cache。若有 `--clusters-allowlist` 則與 allowlist 取交集。

**Cross-cluster edges：** `client_cluster != server_cluster` 的 `pod-calls-pod` 與兩端 node 皆在 global cached graph 中；部分 cluster scope 時保留觸及選集之 edge 與兩端 node。

**Cluster 名稱：** 不透明字串透傳；無 canonicalisation；未知 `?cluster=` 名稱僅無 node，非錯誤。

### D18. External-endpoint substitution

Service-graph metrics 帶 Tempo 風格 `client` / `server` 人類可讀 label。預設以 `(client_cluster, client_k8s_pod_uid)` 等對 topology 解析為 pod。非 pod 遠端（外部 HTTP、託管 DB、queue、SaaS）可經環境變數 `KSG_EXTERNAL_NAME_PATTERN`（或 flag `--external-name-pattern`）以 **substring contains** 判斷：若 label 值包含 pattern，則該端為 `external` node（`id = "external/<label_value>"` 等）。Client/server 兩側獨立；edge `type` 仍為 `pod-calls-pod`。External 端對應之 `client_cluster` / `server_cluster` 在 edge `labels` 中 **必須**為空字串 `""`。

空 pattern 則關閉規則，回到純 pod UID 解析。

### D19. Allowlist 與 bounded upstream cost

- `--clusters-allowlist`：設則所有 PromQL 與 discovery 注入 `{cluster=~"a|b|c"}`；allowlist 外 cluster 對本 server 不可見。
- `--max-pods <n>`：範圍內 distinct `kube_pod_info` series 超過上限則 `503`、`reason: "cluster_too_large"`（預設 5000）。

## 風險 / 取捨

- [Cold cache miss latency] → 文件說明首次進入某 time bucket 的查詢需付完整 multi-cluster PromQL fan-out 成本（目標：範圍內 ≤ 5k pods 聚合 ≤ 3 s）；同一 bucket 後續查詢為 cache hit。以 `kube_state_graph_build_duration_seconds` 的 `cache_status` 暴露。
- [Pod UID 在重啟下於長 lookback 視窗內混雜] → 若 `last_over_time(kube_pod_info)` 在同一視窗內對相同 `(cluster, namespace, name)` 回傳多個 UID，保留最新 UID，並輸出 `pod-replaced-by` synthetic edge 連結舊 UID 與目前 UID。於 spec 中文件化。
- [Service-graph metrics 缺失或稀疏] → 僅 topology 的 graph 仍有效；缺 series 則零條 `pod-calls-pod` edge，不視為 build 失敗。
- [多 cluster 時 PromQL fan-out 大] → `--clusters-allowlist` 限制 upstream 成本；超過 `--max-pods` 回 `503`、`reason: cluster_too_large`。Cache 吸收跨呼叫者成本。
- [多樣查詢型態下 cache 記憶體成長] → 由 `MaxCost`（預設 256 MiB）限制；eviction 經 `kube_state_graph_cache_evictions_total` 觀察。
- [Ristretto async write 與 singleflight 競態] → 以 in-band 填入 singleflight 回傳值、將 cache 視為 best-effort warmup 緩解。
- [各 scrape pipeline 的 `cluster` external label 不一致] → 缺 `cluster` label 的 series 歸入 `cluster="unknown"`，並經 `kube_state_graph_clusters_observed` 浮出；文件要求營運方一致設定 label。
- [跨 cluster edge 有一端缺 topology 資料] → 若 producer 發出 `traces_service_graph_request_total` 但某端 `client_k8s_pod_uid` 或 `server_k8s_pod_uid` 在視窗內任何 cluster 的 `kube_pod_info` 都不存在，缺的一端渲染為 synthetic ghost pod node（`attrs.ghost=true`），僅帶 `cluster` 與 `pod_uid`，而非捨棄該 edge。
- [VictoriaMetrics 中 `kube-state-metrics` retention 短於請求視窗] → `last_over_time` 回空；當上游 `up{}` 有資料但 topology 列為零時，回 `400 Bad Request`、`reason: "outside retention"`。
- [Harness 內 fake fixtures 與真實 producer 漂移] → 將 harness 使用的 metric 名稱、label set、cluster label 慣例釘在 D8，使換成真實 producer 時以設定變更為主、非程式變更。
- [API 無 auth] → 文件說明本服務預期置於 reverse proxy 之後。
- [單實例 cache 重啟即失] → v1 可接受；warm-up 成本由 `--max-window` 與典型流量界定。v1.1 逃生閥為 shared Redis L2。
- [Self-metrics 上 multi-cluster cardinality] → `cluster` label 僅出現在觀測用 gauge（`graph_node_count`、`graph_edge_count`）；文件說明預期 `cluster` cardinality（v1 ≤ 20），若超過預算建議在 scrape 層捨棄該 label。

## 遷移計畫

Greenfield repository——無遷移。Rollback 為對 merge commit 做 `git revert`。JSON 合約以頂層 `apiVersion: "v1"` 版本化。

## 待決問題

- D4 所列三種以外最終 edge type 清單（例如 `pod-replaced-by`、`pod-shares-node`、`pod-shares-namespace`）——在撰寫 spec 時定案；v1 若納入須同時出現在 `Build()` 與靜態 `/v1/edge-types` registry。
- `--max-window` 預設值（目前提案 `24h`）以及各 time class 是否應有不同上限。
- DST 或 leap second 的 bucket 邊界政策——傾向「一律 UTC、不做 DST 調整」，於 spec 確認。
- 可選 L2 序列化回應 cache（D6）要進 v1 或延到 v1.1——延到 profiling 顯示 serialise+ETag 成熱點再說。
- `/v1/edge-types` 是否應支援 time-window filter——延到 v1.1。
- `/v1/clusters` 是否應在回應中附 per-cluster pod / node 計數，或維持極簡（僅名稱 + first-seen / last-seen）——延到 spec。
- Fake-fixtures program 形狀：持續 Deployment 穩態 metrics vs YAML 驅動 snapshot replayer——延到 harness spec。
- 要放入 `deploy/grafana/` 的確切 Grafana Node Graph dashboard JSON（含凸顯 cross-cluster edge 的版面）——延到 harness spec。
- `/v1/graph` 上用 `?format=` 是否優於獨立 `/v1/graph/nodegraph` route——延到 spec；目前偏好獨立 route。
- `KSG_EXTERNAL_NAME_PATTERN` 是否演進為 regex（`KSG_EXTERNAL_NAME_REGEX`）或接受多個逗號分隔 pattern——依真實部署回饋延到 v1.x。
- External node 是否應暴露額外 `labels`（例如從 URL 形狀值解析出 scheme）——延後；v1 僅 `labels.pattern`。
