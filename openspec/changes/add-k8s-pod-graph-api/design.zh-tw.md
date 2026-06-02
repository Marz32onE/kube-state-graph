> **翻譯狀態**：v1 已移除 in-process result cache 與 singleflight；本譯本已同步更新。英文 `design.md` 仍為唯一權威來源；若兩者不一致以英文版為準。後續分散式部署的 cache 機制將以另案處理。

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
- 獨立的（repo 外）service-graph producer（通常是各 source cluster 內的 Tempo metrics-generator）會輸出帶 pod UID label 的 metrics，每條 series **僅帶單一 `cluster` external label**，代表追蹤資料來源的 cluster（即 client 端 cluster）。Server 端的 cluster **不會**被打進 metric label；跨 cluster 的解析改由 API server 在 build 階段，將 `server_k8s_pod_uid` 對全域 topology pod-UID index 進行 join 來還原：
  - `traces_service_graph_request_total{cluster, client_k8s_pod_uid, server_k8s_pod_uid, client_k8s_namespace_name, server_k8s_namespace_name, connection_type}`。
- API server 經 Prometheus HTTP API 從 VictoriaMetrics **依請求即時**讀取所需資料，範圍為呼叫端指定的時間區間。它不與任何 cluster 的 Kubernetes API server 通訊、不直接 scrape `kube-state-metrics`、也不連到 service-graph producer。

本 repo 的驗證以 **`go test ./internal/integration/...`** 為唯一路徑：以 testcontainers 起單機 VictoriaMetrics，並 ingest 合成 multi-cluster series（含 `traces_service_graph_request_total` 等），在 CI 與本機皆可重現；`pod-calls-pod`、跨 cluster 與 service-graph 情境皆由此覆蓋。它刻意**不**啟動多個 Kind cluster 或完整 per-cluster scrape pipeline——那屬於部署工作，不在本 repo。

對 API server 的約束：

- Go 版本以根目錄 `go.mod` 為準（實作使用現行 Go 與標準庫）；`log/slog` 記錄 log。
- Gin 做 HTTP routing。
- `github.com/prometheus/client_golang/api` 與 `.../api/v1` 做對外 query。
- `golang.org/x/sync/errgroup` 與 `.../semaphore` 做 parallel fan-out 與併發上限。
- 無 Kubernetes client-go、無 informer、無直接 VictoriaMetrics SDK。
- 單一可設定的 upstream URL（centralised VictoriaMetrics 的 Prometheus 相容 endpoint）。

## 目標 / 非目標

**目標：**

- 交付 Go（Gin）HTTP server，在呼叫端指定的 `[start, end]` 時間區間內，對一或多個 Kubernetes cluster 回傳統一的 nodes-and-edges JSON，由 VictoriaMetrics 即時計算。
- 將**跨 cluster** RPC edge（`pod-calls-pod`，源端與目的端 pod 透過全域 pod-UID index 解析後落在不同 cluster）視為一等 graph element。
- 對 centralised VictoriaMetrics 發帶 `@` timestamp modifier 與 range-aware 函式（`last_over_time`、`rate`）的 PromQL，在記憶體內 join 範圍內所有 cluster 的結果集，組出 graph。
- v1 不附帶 in-process result cache 與 singleflight，每次請求都對 centralised VictoriaMetrics 重新 fan-out。後續另案再以分散式 cache 機制處理。
- 以 `(cluster, pod-uid)` 複合鍵作為 pod node 的穩定 identity 與 pod-pod edge 的 join key；node 與 PVC ID 同樣為 cluster-scoped。
- 回應為 Cytoscape.js 形狀的 JSON。
- 提供 cluster discovery（`GET /v1/clusters`），資料即時來自 VictoriaMetrics；以及靜態 edge-type catalogue（`GET /v1/edge-types`）。
- 提供 integration-test harness（`internal/integration/`：testcontainers-go 啟動 VictoriaMetrics 容器，直接攝入手刻的 multi-cluster `kube_*` 與 `traces_service_graph_*` series），證明 API server 回傳非空、格式正確的 multi-cluster graph，且含跨 cluster edge。

**非目標：**

- 實作客製 service-graph collector（Alloy / OTLP collector）。Harness 使用 fake-fixtures producer 直接寫入合約要求的 metrics。
- 營運、設定或強化 `kube-state-metrics` 或 VictoriaMetrics；它們是依賴，不是交付物。
- 直接對任何 cluster 的 Kubernetes API 通話；所有 cluster 事實經 metrics 讀取。
- HTTP API 上的 authentication、authorisation、multi-tenant isolation 或 TLS termination（假設由 reverse proxy 處理）。Per-cluster RBAC 亦不在範圍——經本 server 可讀到的每個 reachable cluster 一律同等可讀。
- Ingest traces；trace-derived metrics 在上游產生，API server 只讀結果 series。
- 即時 streaming 或 WebSocket API。
- In-process result cache（含跨 process restart 的持久化）。v1 不附帶任何 server-side build cache 與 singleflight。
- 分散式／共用 cache（Redis、memcached）或背景 materialiser。明確延後——後續另案以「Future cache mechanism」處理。
- 以 graph database（Neo4j、Memgraph、ArangoDB）做 partial / traversal query；v1 以 in-memory adjacency 足夠。
- VictoriaMetrics multi-tenant（vmcluster `accountID:projectID`）routing。v1 以單租戶 centralised VM 搭配 `cluster` external label 為隔離模型；multi-tenancy 留作 v1.1 逃生閥。
- 在 integration-test harness 中啟動真實 Kubernetes cluster 或完整 per-cluster scrape pipeline。

## 決策

### D1. 單一 upstream：經 Prometheus API 的 centralised VictoriaMetrics

Server 接受一個 upstream URL（`--prom-url`，預設 `http://localhost:8428`）指向 centralised VictoriaMetrics 的 Prometheus 相容 endpoint。所有輸入（任意 cluster 的 kube-state-metrics series 與 service-graph series）都從這一個 backend 查詢。

Multi-cluster 以 **label** 區辨：所有 series（topology 與 service-graph 均同）皆帶 `cluster=<name>`。Service-graph series 僅帶 trace 來源的 cluster（client 端）；server 端的 cluster 由 build 時用 server pod UID 對全域 topology pod-UID index join 還原。API server 不知道 per-cluster URL。

- 理由：符合既有 centralised observability 部署；N 個 reader 收斂成單一 client；單次 PromQL 可涵蓋所有 cluster。
- 曾考慮的替代方案：
  - 每 cluster 一個 upstream、由 API server fan-out（否決——重複連線邏輯，且跨 cluster edge 兩端會落在兩份 query 結果，難以解析）。
  - VictoriaMetrics multi-tenant（每 cluster `accountID:projectID`）（否決——需 vmcluster、營運較重，且單一 PromQL 跨 cluster edge 較難；v1.1 逃生閥）。
  - 經 client-go informer 直接存取 Kubernetes API（否決——informer 只知所 watch cluster 的*當前*狀態，無法回答歷史 time-range，且帶回 N 路 watch 與 per-cluster RBAC）。

### D2. 依時間區間即時建置，無 server-side snapshot 與無 result cache

每次 `GET /v1/graph?start=...&end=...` 都為所給視窗重新建置 multi-cluster graph：

1. 解析並驗證 `start` / `end`。
2. 將 `start` / `end` 對齊到 60 s grid（`floor` / `ceil`，並對未來邊界 clamp 至 `floor(now, 60s)`，見 D5）。
3. 經 `errgroup.WithContext` 對 centralised VictoriaMetrics parallel 執行所需 PromQL，在記憶體內 join 各 cluster 結果集，產出全域 multi-cluster `Graph`。
4. 對新建的 `Graph` 套用 filter（`cluster`、`namespace`、`node`、`edge_type`）與 traversal pruning（`root`、`depth`、`direction`），再序列化成 Cytoscape.js 形狀回傳。

無 in-process result cache、無 singleflight、無背景 `Snapshotter`、無 `atomic.Pointer[Graph]`、無固定 refresh interval、無 `POST /admin/refresh`、無 `DELETE /admin/cache`。

- 理由：API 合約是 time-ranged，server 不能特權化單一「當前」snapshot；保留 v1 簡單實作，讓後續分散式部署可選用 cache 機制（Redis、materialised-view tier、graph DB），無須先拆掉 in-process cache 假設。未引入 cache 機制前 upstream 成本維持 O(requests)。
- 替代方案：In-process Ristretto + singleflight（前一版設計，已移出 v1，分散式部署上案時再評估）；定期 snapshot（否決——與 time-travel 查詢不相容）；背景 materialiser 寫入共用 store（延後，納入「Future cache mechanism」）。

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

- `pod-mounts-pvc`（僅 intra-cluster）：由 join `kube_pod_spec_volumes_persistentvolumeclaims_info` 與 pod 所在 node，且限單一 cluster。
- `pod-calls-pod`（intra-cluster **或 cross-cluster**）：由 `rate(traces_service_graph_request_total[<window>]) @ <end>` 非零 rate；client 側以 `(cluster, client_k8s_pod_uid)` join；server 側以**全域 pod-UID index**（K8s pod UID 在實務上跨 cluster 唯一）lookup `server_k8s_pod_uid` 還原其所在 cluster。端點亦可為 `service`、`others` 或 `external` node（見 D18 / D27）。Edge 帶 `labels.cluster`，值為 client 端 pod 之 cluster（client 端為非 pod 端點時則省略）；是否 cross-cluster 由消費端比對 source/target node `labels.cluster`（依 D9 嚴格字串規則，`labels` 內不放 boolean flag）。
- `service-selects-pod`（僅 intra-cluster，directed `service`→`pod`，`may_cross_cluster=false`）：由 service-graph reader **按需物化**，當某連線字串端點解析為 in-cluster service node 時，自該 service node 連往 topology index `EndpointsByService` 內每個後端 pod（見 D18）。Labels：可選 `namespace`，無必填 key。

每條 edge 帶 `type`、`source`、`target`，以及型別專屬 `attrs`（序列化 JSON 形狀見 D9）。

**Pod→node 關係不是 edge。** Pod 與其 host K8s node 之間**沒有**任何 edge type；該關係僅由 Cytoscape compound 巢狀表達——pod 的 `data.parent` 由其 `labels.node`（cluster-scoped node ID）導出，形成 `cluster > node > pod` 的容器階層。因此 K8s `node` 節點為**無邊**的 graph node，純粹作為 compound 容器（仍於 `ipaddress` 屬性帶 `external_ip`）。其行為後果：以 pod 為 `name` 錨點或 `root` traversal 起點**不會**再連帶拉入其 host K8s node（兩者間無 edge）；`?namespace=` filter 會**捨棄** K8s node（其無 namespace label 亦無 edge），故 namespace 收斂後的 view 僅含 namespaced 實體。

- 理由：消費端可按 edge type filter；概念上對齊 Tempo `serviceGraph`；跨 cluster 流量為一等概念。
- 替代：無型別 edge + 自由 form attributes map（否決——難驗證與渲染）。
- 新 edge types 僅可 additive；既有 `type` 字串永不改作他用（見 D14）。

### D5. 時間區間語意與 60 s 對齊

`start` 與 `end` 為必填 query 參數，格式為 RFC 3339 或 Unix 秒。Server 唯一驗證：

- `end > start`。

不再有 `--max-window` 視窗上限與 `--max-skew` 未來時間擋板。Bounded query cost 由上游 VictoriaMetrics search limits（`-search.maxQueryDuration`、`-search.maxPointsPerTimeseries`、`-search.maxSamplesPerQuery`）負責；NTP 漂移屬部署層問題，未來時間查詢會自然回傳空集合。

驗證通過後，兩個時間戳直接 pass through 給上游 PromQL（`<window> = end - start`，`<end>` 為呼叫端送出的 `end`）。**v1 不做** server-side bucketing、alignment 或 60 s grid——舊版的 `floor`/`ceil` 邏輯隨 in-process cache 一併移除（`last_over_time` / `rate` 的 lookback 以分鐘計，次秒 `@end` 漂移不影響 upstream 評估）。

**沒有** server-side bucketing、**沒有** time-class TTL 階梯、**沒有** cache key。

- 理由：簡單、純函式、可測；time-class 階梯只在 server-side cache 重新出現時才有意義，留待後續分散式 cache 機制再評估。
- 已捨棄方案：
  - Per-class TTL 階梯（延後——僅當 server-side cache 重新出現才需要；後續分散式 cache 機制再 revisit）。
  - Filter／format 進 cache key（v1 不存在 cache key，無此議題）。
- Bounded query cost 由上游 VictoriaMetrics search limits 完全負責。

### D6. 回應 shape 與 body 決定論；無 in-process result cache

v1 沒有 in-process result cache、沒有請求合併（singleflight）、沒有 `/admin/cache` 路由。每次請求都重新對 upstream fan-out 並重算 body。Server 也**不**發出任何 HTTP cache validator（沒有 `ETag`、沒有 `Last-Modified`）——cache 是後續另案議題（見下方「Future cache mechanism」）。

每個請求的處理流程：

1. 經 `errgroup.WithContext` 對 centralised VictoriaMetrics parallel 執行所需 PromQL，在記憶體內 join 各 cluster 結果集，得到全新 `*Graph`。
2. 對該 `*Graph` 套用 filter spec（`cluster`、`namespace`、`name`、`edge_type`）與 traversal pruning（`root`、`depth`、`direction`）。Filter+prune 回傳輕量 view，非複本。
3. 將 view 序列成 Cytoscape.js 形狀並回應。

**Body 決定論為合約一部分。** 對相同 `(window, filters, upstream-data)` 序列化結果必須 byte-identical（golden test 與下游比較工具依賴此性質）；序列化器必須對 node／edge slice 排序（`graph.SortNodes` / `SortEdges`），`Graph.ClusterNames()` 必須排序，`IPAddress` slice 在 build 端排序去重，回應 body shape 固定為 `{apiVersion, clusters, elements}`，不得加入 time-varying 或 echo-of-input 欄位。

**Response cache headers。** 內容穩定的 route 明示 `Cache-Control`（`/v1/edge-types`：3600 s、`/openapi.{yaml,json}`：3600 s、`/docs/assets/*`：86400 s、`/docs`：300 s）以利 browser 與 reverse proxy 之 client-side cache。`/v1/graph`、`/v1/clusters` 不發 `Cache-Control`——每次 fresh build 之 body 視為權威。

**無 singleflight。** 併發的相同請求各自獨立跑 upstream fan-out。在 v1 / 分散式部署前的流量規模可接受；跨節點請求合併屬於後續分散式 cache 機制範疇，不在 v1。

**Future cache mechanism。** 不在 v1 範圍但已預期。具體形狀（另案處理）可能為下列之一：

- **背景 materialiser tier** — 一支獨立 worker 對熱門視窗排程建圖並寫入共享 store（Redis cluster、graph DB、object store JSON），API server 退化為查詢前端。最佳承載分散式部署；最重的 ops。
- **Per-replica L1 + 共享 L2 (Redis)** — Ristretto 重新出現於網路共享 encoded `*Graph` 之前。較易加入但無法解決百萬 node 規模下的 heap 壓力。
- **Pluggable graph DB**（如 Neo4j、Memgraph、ArangoDB）— traversal 從 `internal/graph` 移到 backing store；對 traversal-heavy 工作負載最具擴展性，最大的合約變更面。

實際選擇的形狀需要重新檢視 D5（time-class TTL 階梯）、D11（cache-key hashing）、D12（cache metrics）、D14（cache contract）。v1 刻意把這些洞留空，避免綁死後續可能不適合分散式拓撲的實作。

### D7. Filtering、cluster scoping、partial-graph traversal

`GET /v1/graph` 除必填 `start` / `end` 外接受：

- `?cluster=<name>`——可重複；僅保留 `cluster` 在集合內的 node。對 **跨叢集 `pod-calls-pod`**（源端 pod 與目的端 pod 解析後落在不同 cluster），若僅一端之叢集落在 filter 內，實作會**保留該邊並把缺漏的另一端 pod 節點拉回 view**（仍受 `namespace` filter 約束）；遠端 K8s node 不因「僅作為跨叢集邊的伴點」而自動保留。跨叢集語意由消費端比對 source/target node 之 `labels.cluster` 推得（edge 自身僅帶 `labels.cluster`＝trace 來源／client 端 cluster）。`cluster` 設成未知值不算錯誤——該名稱僅得到空結果。
- `?namespace=<ns>`——可重複；限制 pod / PVC node 的 `namespace` 在集合內。namespace 值可跨 cluster 比對；與 `?cluster=` 併用可縮到單一 cluster 的 namespace。
- `?name=<value>`——可重複；對 `n.Name()` 做精確字串比對，**跨所有 node 型別**（`PodNode`、`K8sNode`、`PVCNode`、`ExternalNode`）。可拿來把 view 錨定到任意一個 node——pod、host node、PVC、或 external endpoint，呼叫端不需要先知道型別。Names 並非全域唯一（pod 與 K8s node 可同名，PVC 名稱可跨 namespace 重複）；所有命中皆回傳。與 `?cluster=` / `?namespace=` 併用可消歧。
- `?edge_type=<type>`——可重複；僅保留該 edge types。若某型別在目前 `Graph` 無 edge，靜默略過（無錯誤、僅空）。
- `?root=<id>&depth=<n>&direction=in|out|both`——partial-graph traversal：自複合 ID（`<cluster>/<pod-uid>` 或 `<cluster>/<node-name>`）做 BFS，以 `depth` 為界（預設 2，最大 6）。

**Edge 端點 re-add 統一規則（跨所有 filter 一致）**：edge 在至少一端在 scope 內時保留；恰好一端在 scope 內時，由 `g.NodesByID` 將缺漏端拉回 view，前提是該端點通過非 cluster 類 filter（namespace）。此規則同時涵蓋僅以 `cluster` 縮 scope 的跨叢集伴點 re-hydration、`pod-mounts-pvc` 邊在 in-scope pod 上的非 pod 端點回補，與以 `name` 錨定 node 後渲染其關聯邊與伴點的需求。`name` 過濾**不**附加額外抑制——錨定到具名 node 的本意就是要呈現它與鄰域之間的邊，否則 graph 會有懸空端點。

`pod_uid` filter 經評估後否決：pod UID 為內部 opaque 識別碼，呼叫端必須先發 `/v1/graph` 才能取得，違反「filter 應為使用者可獨立構造」的設計原則。改以 `cluster` + `name` 縮 scope，並接受 names 並非全域唯一的事實。

Filtering **在回應階段**對新建的 `*Graph` 套用，不重查 upstream。PromQL 永遠抓取上游 VictoriaMetrics 中所有 cluster 的完整視窗；該 build 的 `*Graph` 為所有 filter view 的共用基底。

- 理由：filter＋序列化在典型 graph 大小為微秒級；把 filter push 到 PromQL 會讓每組 filter 組合都重評上游成本。後續 cache 機制上案時，「對 graph 做 projection」的合約能讓同一 cache entry 跨 filter 共用。
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
kube_service_info{cluster, namespace, service, cluster_ip, ...}
kube_endpointslice_endpoints{cluster, namespace, endpointslice, address, targetref_kind, targetref_name, targetref_namespace, ...}
kube_endpointslice_labels{cluster, namespace, endpointslice, label_kubernetes_io_service_name, ...}

# Service graph（每條 series 僅帶單一 source cluster；跨 cluster 由 build 時 UID index 還原）
traces_service_graph_request_total{
  client, server,
  cluster,                         # 單一 trace 來源 cluster（client 端）
  client_k8s_pod_uid, server_k8s_pod_uid,
  client_k8s_namespace_name, server_k8s_namespace_name,
  connection_type="virtual_node|messaging_system|database"
}
traces_service_graph_request_failed_total{ ...same labels... }
traces_service_graph_request_server_seconds_bucket{ ...same labels..., le="..." }
```

`cluster` external label 由各 cluster 的 scrape pipeline（`vmagent` / Prometheus `external_labels`）套用——對 `kube-state-metrics` 與 service-graph series 一致。Service-graph metrics 由各 source cluster 內的 Tempo metrics-generator（或等價的 `servicegraph` connector）產生，producer 僅知道自身所在的 cluster 並把它打為 `cluster`；server 端 cluster **不**被打進 metric label，由 API server 在 build 時用 `server_k8s_pod_uid` 對全域 topology pod-UID index lookup 還原。Producer 端的儀器化需求降為：每條 series 帶 `cluster`（通常已是 external label）與兩端的 pod UID dimension。

**Integration-test fixture 攝入——直接以 exposition format 推入：**

`internal/integration/` 的測試以 [`testcontainers-go`](https://golang.testcontainers.org/) 在每個 suite 啟動真正的 VictoriaMetrics 容器，再透過 VictoriaMetrics 的 `POST /api/v1/import/prometheus` endpoint（Prometheus 純文字 exposition format）把手刻的多 cluster series 推進去。沒有獨立 fixture binary、沒有 YAML、沒有 `/metrics` endpoint、沒有 SIGHUP reload——series 內容與時間戳由測試本身擁有。每個測試 seed：

- 多個 synthetic cluster（例如 `cluster-alpha`、`cluster-beta`）的 `kube_pod_info` / `kube_node_info` / `kube_node_status_addresses`。
- 至少一條**跨 cluster** 的 `traces_service_graph_request_total`：series 帶 `cluster="cluster-alpha"`，但其 `server_k8s_pod_uid` 對應的 `kube_pod_info` entry 落在 `cluster-beta`，供測試驗證 UID-index 還原跨 cluster 的行為。

Service-graph counter 以兩個單調樣本（`t0` 與 `t1 = t0 + 60s`）攝入，使 `rate(...[w]) @ t1` 可還原非零 per-second rate。測試以固定時間錨（`fixedNow = 2026-05-01T12:00:00Z`）保時間桶對齊確定性——見 D20。

- 理由：被測單位是 API server；直接在 Go 測試內合成 metric contract 讓測試聚焦 join / build / HTTP；multi-cluster 僅需不同 `cluster` label；省去 collector + tracing 依賴、fixture 程式、YAML schema 與 reload 協定。
- 測試 **必須**輸出生產合約所定義的精確 label set，生產換成真實 producer 時僅為設定變更。
- `internal/integration/` 為 topology 與 service-graph code path 的唯一驗證路徑；它同時涵蓋 `kube_*` topology 與 `traces_service_graph_*` 兩條鏈路。

**否決：獨立 fixtures binary（`cmd/vm-fixtures/`）+ YAML 設定**——早期草案，已由「測試內直接攝入 exposition」取代。獨立 binary 帶來 build / 部署面 / YAML schema，對測試辨別力毫無增益；測試以 Go 內聯產生 series 即可。
**否決：** 多 Kind + 真實 `kube-state-metrics`（成本雙倍、筆電資源、驗證與直接攝入相同合約）；synthetic OTLP + collector（完整 pipeline 在生產上游）；`telemetrygen`（無 parent/child，servicegraph 無法配對）；OpenTelemetry Demo（過重）。

### D9. 輸出格式：Cytoscape.js JSON

**Node 與 edge schema（canonical）：**

| Object | Field | Type | 來源 / 說明 |
|---|---|---|---|
| Node | `id` | string | Cluster-scoped 複合。Pod：`<cluster>/<pod-uid>`。Node：`<cluster>/<node-name>`。PVC：`<cluster>/<namespace>/<claim>`。**Service**：`<cluster>/<namespace>/<service>`。**Others**（連線字串未解析）：`others/<label-value>`（無 cluster）。**External**（missing-UID fallback）：`external/<label-value>`（無 cluster）。 |
| Node | `name` | string | Pod 名 / node 名 / PVC claim 名 / service 名。Others / external node 為 `client` 或 `server` label 原文（連線字串）。供前端顯示用。 |
| Node | `type` | string | `"pod"`、`"node"`、`"pvc"`、`"service"`、`"others"`、`"external"` 之一。 |
| Node | `labels` | `map[string]string` | 僅字串 key/value。Pod/node/PVC 必含 `cluster`、pods/PVC 含 `namespace`、pods 含 `node`（cluster-scoped node ID）。K8s pod/node label 原文攤平。**Service** 含 `cluster`、`namespace`。IP 位址**不**放 `labels`——改走 typed `ipaddress` 屬性（詳見英文 `design.md` D28）。**Others** 與 **external** 皆帶空 `labels`（`{}`）——無 `pattern`、無 `cluster`（`pattern` key 隨 `KSG_OTHERS_NAME_PATTERN` 旋鈕一併移除，見 D18）。新 key 僅 additive。 |
| Edge | `id` | string | 自固定 namespace UUID 與 canonical tuple `(type, source, target)` 導出之 UUIDv5。同 edge 重建 ID 穩定；符合 RFC 4122。 |
| Edge | `type` | string | `/v1/edge-types` 註冊型別之一（如 `"pod-mounts-pvc"`、`"pod-calls-pod"`、`"service-selects-pod"`）。 |
| Edge | `source` / `target` | string | 同回應內存在之 node `id`。 |
| Edge | `labels` | `map[string]string` | `pod-calls-pod`：`cluster`（trace 來源／client 端 cluster；client 端為非 pod 端點—`service`、`others` 或 `external`—時省略）。`pod-mounts-pvc`：`claim_name`、`storage_class`。`service-selects-pod`：可選 `namespace`（無必填 key）。新 key additive。 |

**嚴格字串型別。** Node 與 edge 的 `labels` 皆為 `map[string]string`。非字串資料（數值 edge metrics 如 `rate`、`p99_ms`、`error_rate`；boolean 如 `cross_cluster`、`ghost`）**延後**到未來 typed struct field。v1 不在 `labels` 內用 `"true"`/`"false"` 字串編 boolean；`pod-calls-pod` 的跨 cluster 狀態由消費端比對該 edge 之 source-node 與 target-node 之 `labels.cluster`（兩端 node 必同時出現於同一 response）推得。

`GET /v1/graph` 回應為 **Cytoscape.js** 形狀 JSON（結構同英文 `design.md` 內範例）。

- 理由：單一 canonical schema 驅動序列化；未來欄位可加在 `labels` 以維持非破壞性。
- Edge `id` 用 UUIDv5：確定性、golden test 穩定、與可讀 `(source, target, type)` 解耦。

### D10. 以 `log/slog`、JSON handler 記錄 log

`slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: ...}))` 為預設；等級可設定。每個 HTTP 請求一行 structured log：method、path、status、duration、request ID、套用之 `cluster` filter。v1 無 cache，故無 `cache_status` 欄位。

每次 build 另有一行：`slog.Info("graph built", ...)`。

### D11. 實作要點（v1 必須）

- **封閉 graph node types**：Go interface `GraphNode` 與具體 `PodNode`、`K8sNode`、`PVCNode`、`ExternalNode`；canonical 欄位供 D9 serializer 使用。`cluster` 放在 `Labels()["cluster"]`（external 節點除外），非 wire 上獨立欄位。
- **Join／建圖路徑**：`internal/build` 的 `Builder.Build` 讀 topology + service graph（經 `promql.Client`），組裝為該時間桶內完整、未套用 HTTP filter 的多叢集 `*graph.Graph`；單元測試覆蓋 join、probe、projection，與 Prometheus 互動在 component／integration 層 mock 或真實 VM。
- **Pure projection layer**：`graph.Project(g *Graph, scope Scope) View` 對不可變 `*Graph` 套用 traversal、`cluster`／`namespace`／`node`／`edge_type` 與跨叢集 `pod-calls-pod` 端點保留（見 `internal/graph/project.go`），回傳唯讀 view；僅 pointer slice，不複製 node／edge struct。
- **Query registry**：PromQL 字串為具名常數，參數化 `<window>` 與 `<end>`；parser 將 Prometheus `model.Vector` 映到 typed Go struct。
- **每個 metric family 一條 PromQL instant query**，在 bucket 化 `end` 以 `last_over_time` / `rate` 評估；query **不含** filter-derived selector。Client 端 parse Vector。
- **Parallel upstream fan-out**：`errgroup.WithContext`。Wall-clock ≈ 最慢查詢。
- **Per-build context timeout**（預設 15 s，可設定）。任一 sub-query 失敗則整次 build 中止，回 `503` 與 `Retry-After: 1`。
- **併發上限**：`golang.org/x/sync/semaphore`（預設 8 個併發 build），超出回 `503`。
- **Adjacency maps**：`Build()` 內建 forward/reverse `map[NodeID][]*Edge`；`Project()` traversal 重用。

### D12. Self-metrics 與可操作性

Server 暴露 `/metrics`（Prometheus exposition），至少含英文 `design.md` D12 所列 histogram/counter/gauge 系列名稱與 label（`kube_state_graph_*`）。

Health：`GET /livez` 行程活著即 200；`GET /readyz` 僅當便宜 upstream probe（`up{}` instant query，1 s timeout）成功為 200。

Operator：v1 無 result cache，故 **無** `DELETE /admin/cache` 路由；亦無 `GET /debug/last-queries` 與 `--enable-debug` flag。診斷靠 `kube_state_graph_*` metrics 與結構化請求 log。

### D13. 測試層級

五層皆須在 archive 變更前存在：Unit（純 join/parse/project）、Component（`httptest.Server` mock Prometheus API）、Golden（`testdata/golden`）、Property（invariants）、Integration（testcontainers VictoriaMetrics + ingest，見 `internal/integration`）。PR 上 CI 以 `go test ./...` 涵蓋含 `-race` 的整合測試。理由與英文版相同。

### D14. 版本化

- 所有 HTTP route 前綴 `/v1/`。
- Body 頂層 `apiVersion: "v1"`。
- 新 edge types 與新 `attrs` 僅 additive；移除欄位為 v2 break。
- Producer 的 `connection_type` 映到穩定內部 enum。
- `cluster` label 值透傳為不透明字串；上游改名為呼叫端可見變更，非 API break。

### D15. Edge-type discovery API

`GET /v1/edge-types` 回傳靜態 catalogue（結構同英文 `design.md` 內 JSON 範例）。無 upstream；不受時間或 filter 參數化。`Cache-Control: public, max-age=3600`。

### D16. v1 不用 graph database、不用 client-go informer

理由與「revisit triggers」同英文版：in-memory adjacency 在 v1 規模為微秒級；informer 無歷史 time-range。

### D17. Multi-cluster routing、discovery、cross-cluster edges

**Routing：** 皆以 query parameter 多選 `cluster`，非 path segment。理由：cross-cluster edge 自然跨多 cluster，單 cluster path 會導致語意矛盾。

**Discovery：** `GET /v1/clusters` 由 `group by (cluster) (last_over_time(kube_node_info[1h]))` 即時導出，lookback 固定寫死為 `1h`（足以吸收暫時性 KSM scrape gap，不開放配置）。每次請求都打 VictoriaMetrics，v1 不附帶 in-process discovery cache。

**Cross-cluster edges：** 源端與目的端 pod 解析後落在不同 cluster 的 `pod-calls-pod`，與兩端 node 皆在當次 build 的全域 multi-cluster graph 中；部分 cluster scope 時保留觸及選集之 edge 與兩端 node。Edge 帶 `labels.cluster`＝client 端 cluster；跨 cluster 由消費端比對 source/target node `labels.cluster` 推得。

**Cluster 名稱：** 不透明字串透傳；無 canonicalisation；未知 `?cluster=` 名稱僅無 node，非錯誤。

### D18. Connection-string 端點解析（hardcode `"://"`）、service 節點與 service-selects-pod 邊

Service-graph metrics 帶 Tempo 風格 `client` / `server` 人類可讀 label。當端點為外部依賴時其 pod UID 為空，而 `client` / `server` label 持有一段連線字串，例如 `mongodb://mongo-0.mongo.db.svc.cluster.local:27017` 或 `https://payments.partner.example/api`。

**移除 `KSG_OTHERS_NAME_PATTERN` 旋鈕。** 早期設計以環境變數 `KSG_OTHERS_NAME_PATTERN`（或 flag `--others-name-pattern`、`config.OthersNamePattern` 欄位、`others_name_pattern_set` 啟動 log 欄位）做 substring contains 比對來判定 `others`。本旋鈕及其全部 threading（`ReadServiceGraph` / `parseServiceGraph` / `resolveClientEndpoint` / `resolveServerEndpoint` 的 `othersPattern` 參數）整個移除——pre-GA 無使用者、無向後相容包袱，且 hardcode 的 `"://"` 偵測幾乎涵蓋全部實務情境。可設定的 substring-match 機制不復存在（程式碼移除由 `tasks.md` 追蹤；spec／design 僅停止引用該旋鈕）。

**Hardcode `"://"` 偵測。** 僅針對 service-graph 之 `client` / `server` label 值求值。Client / server 兩側獨立評估：當該端點 pod UID **為空** 且 label 含 substring `"://"` 時，於 missing-UID 人類 label fallback（下方 Stage 3）**之前**先跑 **連線字串解析（新 Stage 0）**。UID 非空時走原有 pod-UID 解析（連線字串僅在 UID 為空時出現）。

**連線字串解析演算法：**

1. 將 label 當 URL 解析，取 host（去除 scheme、userinfo、port、path/query）。無 host ⇒ unresolvable。
2. 以 K8s DNS 文法比對 host。剝除可選的尾段 `.svc.<cluster-domain>`（如 `.svc.cluster.local`）；亦接受較短的 `<...>.svc` 與裸 `<a>.<b>` 形式。計算 service-relative 部分的點分 label 數，並將**兩種形式**都歸約為所定址的 `(service, namespace)`：
   - 2 個 label `<service>.<namespace>` ⇒ 所定址的 service（一般 ClusterIP，或 headless 的 service-level 名稱）。
   - 3 個 label `<pod-hostname>.<service>.<namespace>`（headless 每-pod DNS 名）⇒ **丟棄**前導 `<pod-hostname>`，解析為相同的 `<service>.<namespace>`。headless 每-pod 位址與裸 service 位址解析方式完全相同——**不再有**特定 pod 解析路徑。
   - 其他 label 數 ⇒ unresolvable。
3. 以 `(cluster, namespace, service)` 對 topology index `ServicesByNameNS`（由 `kube_service_info` 建）解析。HIT ⇒ 端點解析為 **service node**：`id="<cluster>/<namespace>/<service>"`、`type="service"`、`labels={cluster, namespace}`，`ipaddress=[cluster_ip]`（當 `cluster_ip != "None"`；headless 之 `cluster_ip="None"` 時省略）。Reader 並**按需且去重**地，自該 service node 對 topology index `EndpointsByService`（由 `kube_endpointslice_endpoints` 經 `(namespace, targetref_name)`→pod UID join topology pods 建）內每個後端 pod 各物化一條 `service-selects-pod` 邊；已知但無後端 endpoint 的 service 仍會物化 service node，僅無 fan-out 邊。MISS（service 不在 topology）⇒ unresolvable。
4. **CLUSTER 判定：** lookup 使用 trace 來源 cluster label（client 端），因 `.svc.cluster.local` 為 in-cluster DNS（target 與 caller 在同一 k8s cluster）。reader 將缺漏的 `cluster` label bucket 為 `"unknown"`（`bucketCluster`），故 lookup 永遠 cluster-scoped；service 不在該 cluster topology ⇒ resolve 為 `others`。

**unresolvable 之 `"://"` label**（host 非可解析之 k8s `.svc` 名、或 service／pod 不在 topology、或跨 cluster 模糊）⇒ fallback 為 **others node**：`id="others/<label>"`、`name="<label>"`（原文）、`type="others"`、`labels={}`（**空**——`pattern` key 已隨旋鈕移除）。如此可保留真正外部的 URL（如 `https://payments.partner.example/api`）與未知 k8s 名稱的可見性。

**新的每端點解析順序：**

1. 連線字串解析（hardcode `"://"`；僅在 UID 為空且 label 含 `"://"`）：service node（+ `service-selects-pod` 邊）或（miss 時）`others/<label>` 且 `labels={}`。**永不為 pod**。
2. 對 topology 的 pod-UID 解析／synth-pod fallback（僅在 UID 非空）。
3. Missing-UID 人類 label fallback ⇒ `external/<label>`、`labels={}`（僅在 UID 為空且 label 非空且 label **不**含 `"://"`）。
4. Drop（UID 與 label 皆空）。

結論：含 `"://"` 且 UID 為空的 label **一律**走 stage 1，永不抵達 external fallback。`external` 現專指 **非 URL** 的 missing-UID label（producer-regression 訊號）。

**Edge `labels.cluster` 規則（D9）不變：** 僅當 **client** 端解析為 **pod**（來自非空 pod UID）時才帶 `cluster`。Client 之 `"://"` label 一律解析為 service／others（永不為 pod），故此類 edge 永遠**省略** `cluster`，與其他非-pod client 端一致。Service node **不是** pod。

**節點語意：** `others` = 一段被辨識的 `"://"` 連線字串，但**未**解析為 in-cluster pod 或 service（宣告之外部依賴）。`external` = missing-UID 端點且其人類 label 為 **非 URL**（producer-regression 訊號）。兩者仍以 node `type` + id 命名空間 **disjoint**（`others/<label>` vs `external/<label>`），各有獨立 dedupe map；同一 label 字串被兩條 code path 命中時會產生兩個不同節點（intentional）。

### D19. Bounded upstream cost

Per-query cost（執行時間、樣本數、點數）完全交由上游 VictoriaMetrics search limits 負責——KSG 不重複設限。大規模部署 SHALL 在 VM 端設定 `-search.maxQueryDuration`、`-search.maxPointsPerTimeseries`、`-search.maxSamplesPerQuery`，並依賴 `502 Bad Gateway`（由 VM 5xx 映射，`reason: "upstream"`）做為 overflow 訊號。Per-cluster scope 收斂屬呼叫端職責，透過 `/v1/graph` 的 `?cluster=` 參數；server 本身每次 build 都載入上游 VM 中所有 cluster。

### D29. Connection-string 端點解析、service 節點，與移除 `KSG_OTHERS_NAME_PATTERN`

本決策整合 D18 的 hardcode `"://"` 連線字串解析、新的 `service` 節點型別與 `service-selects-pod` 邊，並正式移除 `KSG_OTHERS_NAME_PATTERN` 旋鈕。背景：service-graph metric `traces_service_graph_request_total` 帶 `client`、`server`、`cluster`（trace 來源／client 端）、`client_k8s_pod_uid`、`server_k8s_pod_uid`、`client_k8s_namespace_name`、`server_k8s_namespace_name`、`connection_type`。當端點為外部依賴時其 pod UID 為空，而 `client`／`server` label 持有一段連線字串（如 `mongodb://mongo-0.mongo.db.svc.cluster.local:27017` 或 `https://payments.partner.example/api`）。

**(1) 移除 `KSG_OTHERS_NAME_PATTERN` 旋鈕（整個）。** 環境變數 `KSG_OTHERS_NAME_PATTERN`、flag `--others-name-pattern`、`config.OthersNamePattern` 欄位、builder 參數 threading（`ReadServiceGraph` / `parseServiceGraph` / `resolveClientEndpoint` / `resolveServerEndpoint` 的 `othersPattern` 參數）、以及 `others_name_pattern_set` 啟動 log 欄位全部移除。Pre-GA 無向後相容包袱。可設定的 substring-match 機制不復存在（程式碼移除由 `tasks.md` 追蹤；spec／design 僅停止引用此旋鈕）。

**(2) Hardcode `"://"` 偵測。** 僅針對 service-graph 之 `client`／`server` label 值求值。Client / server 兩側獨立：當端點 pod UID **為空** 且 label 含 substring `"://"` 時，於 missing-UID fallback（Stage 3）**之前**先跑 **連線字串解析（新 Stage 0）**。UID 非空時走原有 pod-UID 解析（連線字串僅在 UID 為空時出現）。

**(3) 連線字串解析演算法。** (a) 將 label 當 URL 解析取 host（去 scheme、userinfo、port、path/query）；無 host ⇒ unresolvable。(b) 以 K8s DNS 文法比對：剝除可選尾段 `.svc.<cluster-domain>`（如 `.svc.cluster.local`），亦接受 `<...>.svc` 與裸 `<a>.<b>`；計算 service-relative 部分點分 label 數並將**兩種形式**歸約為 `(service, namespace)`——2 個 ⇒ `<service>.<namespace>`（一般 ClusterIP 或 headless 之 service-level 名稱）；3 個 ⇒ `<pod-hostname>.<service>.<namespace>`（headless 每-pod 名）**丟棄**前導 pod-hostname 後解析為相同 `<service>.<namespace>`，**不再有**特定 pod 解析；其他 label 數 ⇒ unresolvable。(c) 以 `(cluster, namespace, service)` 對 `ServicesByNameNS` 解析；HIT ⇒ service node 並按需物化 `service-selects-pod` 邊（見點 8；已知但零後端 endpoint 者仍物化 service node、無 fan-out 邊）；MISS ⇒ unresolvable。(d) CLUSTER 判定：lookup 以 trace 來源 cluster（client 端）為範圍，因 `.svc.cluster.local` 為 in-cluster DNS；reader 將缺漏的 `cluster` label bucket 為 `"unknown"`（`bucketCluster`），故 lookup 永遠 cluster-scoped；service 不在該 cluster 之 topology ⇒ resolve 為 `others`。

**(4) unresolvable 之 `"://"` label** ⇒ fallback 為 others node：`id="others/<label>"`、`name="<label>"`（原文）、`type="others"`、`labels={}`（**空**——`pattern` key 隨旋鈕移除）。保留真正外部 URL 與未知 k8s 名稱的可見性。

**(5) 每端點解析順序：**（1）連線字串解析（hardcode `"://"`；僅 UID 空且含 `"://"`）：service node（+ `service-selects-pod` 邊）或（miss）`others/<label>` 且 `labels={}`，**永不為 pod**；（2）pod-UID 解析／synth-pod fallback（僅 UID 非空）；（3）missing-UID 人類 label fallback ⇒ `external/<label>`、`labels={}`（僅 UID 空、label 非空且 label **不**含 `"://"`）；（4）drop（UID 與 label 皆空）。結論：含 `"://"` 且 UID 空之 label **一律**走 stage 1，永不抵達 external fallback；`external` 現專指 **非 URL** 的 missing-UID label。

**(6) Others 節點 labels 改為 `{}`** （移除 `pattern` key）。新語意：`others` = 被辨識之 `"://"` 連線字串但**未**解析為 in-cluster pod 或 service（宣告之外部依賴）；`external` = missing-UID 端點且人類 label 為**非 URL**（producer-regression 訊號）。兩者仍以 node `type` + id 命名空間 disjoint（`others/<label>` vs `external/<label>`）。

**(7) Edge `labels.cluster` 規則（D9）不變：** 僅當 **client** 端解析為 **pod**（來自非空 pod UID）時才帶。Client 之 `"://"` label 一律解析為 service／others（永不為 pod），故此類 edge 永遠**省略** `cluster`，與其他非-pod client 端一致。Service node 不是 pod。

**(8) 新 graph 型別。** NodeType `"service"`；`ServiceNode` struct（`IDValue`、`NameValue`、`LabelsValue`、`IPAddressValue []string`），`IPAddress()` 回 `[cluster_ip]`（`"None"`／不存在時為 nil）；`ServiceID(cluster,namespace,service)="<cluster>/<namespace>/<service>"`。EdgeType `"service-selects-pod"`（directed `service`→`pod`、`may_cross_cluster=false`、intra-cluster；labels：可選 `namespace`，無必填），註冊於 `graph.EdgeTypes` 並由 `/v1/edge-types` 列出。`pod-calls-pod` 的 source_type／target_type 現在亦含 `"service"`（pod 可呼叫 service node），既有已含 `"pod"`、`"others"`、`"external"`。Service node + `service-selects-pod` 邊由 service-graph reader 對被引用的 service **按需物化**（不整批 emit，避免 graph 膨脹）。

**(9) Topology reader 新增消費** `kube_service_info{cluster, namespace, service, cluster_ip, ...}`、`kube_endpointslice_endpoints{cluster, namespace, endpointslice, address, targetref_kind, targetref_name, targetref_namespace, ...}`、`kube_endpointslice_labels{cluster, namespace, endpointslice, label_kubernetes_io_service_name, ...}`（join slice→service 名）。它**只建 index**：`ServicesByNameNS`、`EndpointsByService`（service → 對 topology pods 經 `targetref_name` 解析後之 []pod，即 Service→後端 pod fan-out 的來源）。endpoint 的 `hostname` label **不再消費**——已移除的每-pod headless 解析是它唯一的讀者，故它退出 KSM 合約。**實機驗證待辦**：slice→service join 的精確 KSM label 為 `kube_endpointslice_labels` 上的 `label_kubernetes_io_service_name`，以 `(cluster,namespace,endpointslice)` join；並確認 `kube_endpointslice_endpoints` 帶 `targetref_*`。

**(10) metric-prefix 範圍擴張**（英文 `design.md` D26）至 3 個新 kube_* metric（`kube_service_info`、`kube_endpointslice_endpoints`、`kube_endpointslice_labels`）——`KSG_METRIC_PREFIX` 套用之。Label-name 合約擴充：`service`、`cluster_ip`、`endpointslice`、`address`、`targetref_kind`、`targetref_name`、`targetref_namespace`、`label_kubernetes_io_service_name`。**不**套用於 `traces_service_graph_*` 或 `up{}`。

**(11) KSM 設定要求。** 生產 KSM 需於 `--resources` 與 `--metric-allowlist` 啟用 services + endpointslices，並擴充 RBAC（list/watch services、endpointslices）。`KSG_OTHERS_NAME_PATTERN` env 已整個移除（見點 1），無需於任何部署清單設定。（由 `tasks.md` 追蹤。）

**(12) 決定論。** Service node + `service-selects-pod` 邊皆過 `graph.SortNodes`／`SortEdges`；按需物化對相同 upstream data 為決定性；`ipaddress`／`labels` 依既有排序規則。Body shape `{apiVersion, clusters, elements}` 不變。

## 風險 / 取捨

- [Per-request build cost] → 每次 `/v1/graph` 請求都會做完整 upstream PromQL fan-out（目標：範圍內 ≤ 5k pods 聚合 ≤ 3 s）。v1 無 in-process result cache，upstream 負載與 HTTP 流量線性相關；`--build-timeout` 限制 graph endpoint tail latency（超時回 `504 Gateway Timeout`、`reason: "timeout"`）；`--api-timeout` 限制非 graph endpoint（`/v1/clusters`、`/readyz`）的 upstream 呼叫時間。Concurrency 控制委派給 HPA + Pod resource limits，無 in-process semaphore。後續 cache 機制預期吸收此成本。
- [Pod UID 在重啟下於長 lookback 視窗內混雜] → 若 `last_over_time(kube_pod_info)` 在同一視窗內對相同 `(cluster, namespace, name)` 回傳多個 UID，**只保留**最新 UID 並丟棄舊 UID。kubelet 不再回報已刪除 UID 後，舊 pod 與新 pod 之間沒有可靠的 identity 鏈接（KSM series 直接消失，controller 為新 pod 配發全新 UUID 而無 back-reference），因此原本要發 `pod-replaced-by` synthetic edge 的構想被否決——它會暗示資料來源不支援的 identity mapping。於 spec 中文件化。
- [Service-graph metrics 缺失或稀疏] → 僅 topology 的 graph 仍有效；缺 series 則零條 `pod-calls-pod` edge，不視為 build 失敗。
- [多 cluster 時 PromQL fan-out 大] → Per-query cost 由 VM search limits 負責；KSG 將 VM 5xx 映射為 `502 Bad Gateway`、`reason: "upstream"`。v1 無 cache，跨呼叫者成本未攤平；後續 cache 機制再處理。
- [無 result cache → upstream 負載與流量線性相關] → 在分散式部署前的 dev 階段可接受。後續 cache 機制（Redis L2、materialiser tier、graph DB）為計畫中的緩解；設計空間刻意留空，等部署拓撲明朗再選。
- [各 scrape pipeline 的 `cluster` external label 不一致] → 缺 `cluster` label 的 series 歸入 `cluster="unknown"`，並經 `kube_state_graph_clusters_observed` 浮出；文件要求營運方一致設定 label。
- [跨 cluster edge 有一端缺 topology 資料] → 若 producer 發出 `traces_service_graph_request_total` 但某端 `client_k8s_pod_uid` 或 `server_k8s_pod_uid` 在視窗內任何 cluster 的 `kube_pod_info` 都不存在，缺的一端渲染為 synthetic ghost pod node（`attrs.ghost=true`），僅帶 `cluster` 與 `pod_uid`，而非捨棄該 edge。
- [VictoriaMetrics 中 `kube-state-metrics` retention 短於請求視窗] → `last_over_time` 回空；當上游 `up{}` 有資料但 topology 列為零時，回 `400 Bad Request`、`reason: "outside retention"`。
- [Harness 與真實 producer 漂移] → 整合測試以 `testcontainers-go` 直接攝入 exposition format，測試本身擁有 series 內容；只要測試的 label set 對齊 D8 合約，換成真實 producer 即為設定變更而非程式變更。
- [API 無 auth] → 文件說明本服務預期置於 reverse proxy 之後（HTTP 層另以 `X-API-Key` middleware 提供基本 key auth；見 spec）。
- [Self-metrics 上 multi-cluster cardinality] → `cluster` label 僅出現在觀測用 gauge（`graph_node_count`、`graph_edge_count`）；文件說明預期 `cluster` cardinality（v1 ≤ 20），若超過預算建議在 scrape 層捨棄該 label。
- [missing-UID fallback 於 producer regression 時以 `external/*` 節點灌爆 graph] → Beyla／Alloy 退化而剝除 `k8s.pod.uid` resource attribute 時，現在會浮現一波推論之 `external/<client>` 與 `external/<server>` 節點，而非靜默丟邊。此為刻意設計（替代方案是靜默資料遺失），且現在與宣告之 `others/<label>` 集合在視覺上 disjoint（D18 / D29——others 改為被辨識之 `"://"` 連線字串），故 `type="external"` 節點數突增是調查 trace pipeline（而非 API）的乾淨訊號。注意：含 `"://"` 之 label 一律走連線字串解析（Stage 0），不再落入 external fallback。
- [Service node 按需物化導致 `service-selects-pod` 邊扇出] → 一個被多個 caller 引用的大型 service 會物化通往其每個後端 pod 的 `service-selects-pod` 邊；reader 對被引用的 service 去重物化（非整批 emit），扇出上限為該 service 之 endpoint 數。大規模可經 `?cluster=` / `?namespace=` scope 收斂。
- [KSM 新增 services + endpointslices 之 RBAC 與 cardinality] → 啟用這兩個 resource 需擴充 KSM RBAC（list/watch services、endpointslices），並提高 KSM series cardinality（每個 endpoint 一條 `kube_endpointslice_endpoints`）；文件要求營運方以 `--metric-allowlist` 限縮並評估 KSM 記憶體。`label_kubernetes_io_service_name` 與 `targetref_*` 的精確 label 形狀待實機驗證（見 D29 點 9）。

## 遷移計畫

Greenfield repository——無遷移。Rollback 為對 merge commit 做 `git revert`。JSON 合約以頂層 `apiVersion: "v1"` 版本化。

## 待決問題

- D4 所列三種以外最終 edge type 清單（例如 `pod-shares-node`、`pod-shares-namespace`）——在撰寫 spec 時定案；v1 若納入須同時出現在 `Build()` 與靜態 `/v1/edge-types` registry。v1 出貨僅含三種：`pod-mounts-pvc`、`pod-calls-pod`、`service-selects-pod`。
- DST 或 leap second 的 bucket 邊界政策——傾向「一律 UTC、不做 DST 調整」，於 spec 確認。
- 後續分散式部署的 cache 機制形狀（Redis L2、背景 materialiser、graph DB）——以另案處理，等部署拓撲明朗再選。
- `/v1/edge-types` 是否應支援 time-window filter——延到 v1.1。
- `/v1/clusters` 是否應在回應中附 per-cluster pod / node 計數，或維持極簡（僅名稱 + first-seen / last-seen）——延到 spec。
- ~~Fake-fixtures program 形狀：持續 Deployment 穩態 metrics vs YAML 驅動 snapshot replayer~~——已決：無 fixtures 程式。整合測試（`internal/integration/`）以 `POST /api/v1/import/prometheus` 直接攝入 series 至 `testcontainers-go` 啟動的 VictoriaMetrics 容器。
- ~~`KSG_OTHERS_NAME_PATTERN` 是否演進為 regex（`KSG_OTHERS_NAME_REGEX`）或接受多個逗號分隔 pattern~~——已決：旋鈕整個移除（D18 / D29）。改以 hardcode `"://"` 連線字串解析（service node / `others` fallback）；可設定之 substring-match 機制不復存在。
- ~~Others node 是否應暴露額外 `labels`（例如從 URL 形狀值解析出 scheme）~~——已決：`others` node `labels={}`（`pattern` key 隨旋鈕移除）。In-cluster 之連線字串一律解析為 `service` node（`labels={cluster, namespace}`，並 fan-out 至後端 pod；見 D18 / D29）。
- Connection-string 解析中 cluster-domain 後綴是否需可設定（如非 `cluster.local` 之自訂 domain）——目前接受 `.svc.<cluster-domain>` / `<...>.svc` / 裸 `<a>.<b>`；依真實部署回饋延到 v1.x。
