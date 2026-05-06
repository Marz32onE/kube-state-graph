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

本 repo 的驗證分兩層：（1）**`go test ./internal/integration/...`** 以 testcontainers 起單機 VictoriaMetrics，並 ingest 合成 multi-cluster series（含 `traces_service_graph_request_total` 等），在 CI 與本機皆可重現；（2）可選的 **`local/kind/`** 腳本：真實 Kind + kube-state-metrics + VM，用於手動視覺化／拓樸驗證，**不**產生 service-graph 指標——`pod-calls-pod` 仍以（1）為主。兩者皆刻意**不**啟動多個 Kind cluster 或完整 per-cluster scrape pipeline——那屬於部署工作，不在本 repo。

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
- 透過 HTTP `ETag` 提供以內容定址的 conditional GET，讓相同 `(window, filters, upstream-data)` 的重複呼叫者在 body 不變時可以拿 `304 Not Modified` 短路；v1 不附帶 in-process result cache 與 singleflight，每次請求都對 centralised VictoriaMetrics 重新 fan-out。
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
- In-process result cache（含跨 process restart 的持久化）。v1 不附帶任何 server-side build cache 與 singleflight；ETag-based HTTP revalidation 是唯一 cache 層。
- 分散式／共用 cache（Redis、memcached）或背景 materialiser。明確延後——後續另案以「Future cache mechanism」處理。
- 以 graph database（Neo4j、Memgraph、ArangoDB）做 partial / traversal query；v1 以 in-memory adjacency 足夠。
- VictoriaMetrics multi-tenant（vmcluster `accountID:projectID`）routing。v1 以單租戶 centralised VM 搭配 `cluster` external label 為隔離模型；multi-tenancy 留作 v1.1 逃生閥。
- 在 integration-test harness 中啟動多個 Kind cluster或真實 per-cluster scrape pipeline。

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
4. 對新建的 `Graph` 套用 filter（`cluster`、`namespace`、`node`、`edge_type`）與 traversal pruning（`root`、`depth`、`direction`），再序列化成請求格式（Cytoscape.js 或 Grafana Node Graph）回傳。
5. 由序列化 body 計算 `ETag` 並寫回應；HTTP 層僅留 ETag-based conditional GET，body 不變時回 `304 Not Modified`。

無 in-process result cache、無 singleflight、無背景 `Snapshotter`、無 `atomic.Pointer[Graph]`、無固定 refresh interval、無 `POST /admin/refresh`、無 `DELETE /admin/cache`。

- 理由：API 合約是 time-ranged，server 不能特權化單一「當前」snapshot；保留 v1 簡單實作，讓後續分散式部署可選用 cache 機制（Redis、materialised-view tier、graph DB），無須先拆掉 in-process cache 假設。ETag 仍提供呼叫端免費的 conditional GET，未引入 cache 機制前 upstream 成本維持 O(requests)。
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

- `pod-runs-on-node`（僅 intra-cluster）：由時間區間內評估的 `kube_pod_info{node=..., cluster=...}` 衍生。
- `pod-mounts-pvc`（僅 intra-cluster）：由 join `kube_pod_spec_volumes_persistentvolumeclaims_info` 與 pod 所在 node，且限單一 cluster。
- `pod-calls-pod`（intra-cluster **或 cross-cluster**）：由 `rate(traces_service_graph_request_total[<window>]) @ <end>` 非零 rate；client 側以 `(cluster, client_k8s_pod_uid)` join；server 側以**全域 pod-UID index**（K8s pod UID 在實務上跨 cluster 唯一）lookup `server_k8s_pod_uid` 還原其所在 cluster。Edge 帶 `labels.cluster`，值為 client 端 pod 之 cluster（client 端為 external 時則省略）；是否 cross-cluster 由消費端比對 source/target node `labels.cluster`（依 D9 嚴格字串規則，`labels` 內不放 boolean flag）。

每條 edge 帶 `type`、`source`、`target`，以及型別專屬 `attrs`（序列化 JSON 形狀見 D9）。

- 理由：消費端可按 edge type filter；概念上對齊 Tempo `serviceGraph`；跨 cluster 流量為一等概念。
- 替代：無型別 edge + 自由 form attributes map（否決——難驗證與渲染）。
- 新 edge types 僅可 additive；既有 `type` 字串永不改作他用（見 D14）。

### D5. 時間區間語意與 60 s 對齊

`start` 與 `end` 為必填 query 參數，格式為 RFC 3339 或 Unix 秒。Server 強制：

- `end > start`。
- `end - start <= --max-window`（預設 `24h`）。
- `end <= now + --max-skew`（預設 `1m`）。

驗證通過後，兩個時間戳直接 pass through 給上游 PromQL（`<window> = end - start`，`<end>` 為呼叫端送出的 `end`）。**v1 不做** server-side bucketing、alignment 或 60 s grid——舊版的 `floor`/`ceil` 邏輯隨 in-process cache 一併移除，因為對 ETag hit-rate 不再有意義（`last_over_time` / `rate` 的 lookback 以分鐘計，次秒 `@end` 漂移不影響 upstream 評估）。

**沒有** server-side bucketing、**沒有** time-class TTL 階梯、**沒有** cache key。

- 理由：簡單、純函式、可測；time-class 階梯只在 server-side cache 重新出現時才有意義，留待後續分散式 cache 機制再評估。
- 已捨棄方案：
  - Per-class TTL 階梯（延後——僅當 server-side cache 重新出現才需要；後續分散式 cache 機制再 revisit）。
  - Filter／format 進 cache key（v1 不存在 cache key，無此議題）。
- 已捨棄的緩解（仍適用）：可選 `--clusters-allowlist` 在所有 PromQL 注入 `cluster=~"a|b|c"`，無論 VM 內有多少 cluster 都可限制 upstream 成本。

### D6. HTTP-layer caching only（ETag）；無 in-process result cache

v1 **僅**保留 HTTP 層 `ETag`。先前設計的三層堆疊（Ristretto + singleflight + ETag）已被移除。沒有 in-process result cache、沒有請求合併（singleflight）、沒有 `/admin/cache` 路由。每次請求都重新對 upstream fan-out。

每個請求的處理流程：

1. 經 `errgroup.WithContext` 對 centralised VictoriaMetrics parallel 執行所需 PromQL，在記憶體內 join 各 cluster 結果集，得到全新 `*Graph`。
2. 對該 `*Graph` 套用 filter spec（`cluster`、`namespace`、`node`、`edge_type`）與 traversal pruning（`root`、`depth`、`direction`）。Filter+prune 回傳輕量 view，非複本。
3. 將 view 序列成請求的 `format`（Cytoscape.js 或 Grafana Node Graph）。
4. 由序列化 body 計算 `ETag = sha256(body)` 並寫回應。呼叫端帶 `If-None-Match: "<etag>"` 重訪同視窗時，server 仍跑完 build／序列化，但比對 ETag 後回 `304 Not Modified` 並省去 body 傳輸。

**ETag 決定論為合約一部分。** `sha256(body)` 對相同 `(window, filters, upstream-data)` 必須穩定；序列化器必須對 node／edge slice 排序（`graph.SortNodes` / `SortEdges`），`Graph.ClusterNames()` 必須排序，回應 body shape 固定為 `{apiVersion, clusters, elements}`，不得加入 time-varying 或 echo-of-input 欄位。`internal/integration/` 的 `TestRepeatedRequestsReturnSameETag` 守住此性質。

**無 singleflight。** 併發的相同請求各自獨立跑 upstream fan-out。在 v1 / 分散式部署前的流量規模可接受；跨節點請求合併屬於後續分散式 cache 機制範疇，不在 v1。

**Future cache mechanism。** 不在 v1 範圍但已預期。具體形狀（另案處理）可能為下列之一：

- **背景 materialiser tier** — 一支獨立 worker 對熱門視窗排程建圖並寫入共享 store（Redis cluster、graph DB、object store JSON），API server 退化為查詢前端。最佳承載分散式部署；最重的 ops。
- **Per-replica L1 + 共享 L2 (Redis)** — Ristretto 重新出現於網路共享 encoded `*Graph` 之前。較易加入但無法解決百萬 node 規模下的 heap 壓力。
- **Pluggable graph DB**（如 Neo4j、Memgraph、ArangoDB）— traversal 從 `internal/graph` 移到 backing store；對 traversal-heavy 工作負載最具擴展性，最大的合約變更面。

實際選擇的形狀需要重新檢視 D5（time-class TTL 階梯）、D11（cache-key hashing）、D12（cache metrics）、D14（cache contract）。v1 刻意把這些洞留空，避免綁死後續可能不適合分散式拓撲的實作。

### D7. Filtering、cluster scoping、partial-graph traversal

`GET /v1/graph` 除必填 `start` / `end` 外接受：

- `?cluster=<name>`——可重複；僅保留 `cluster` 在集合內的 node。對 **跨叢集 `pod-calls-pod`**（源端 pod 與目的端 pod 解析後落在不同 cluster），若僅一端之叢集落在 filter 內，實作會**保留該邊並把缺漏的另一端 pod 節點拉回 view**（仍受 `namespace`／`node` filter 約束）；遠端 K8s node 不因「僅作為跨叢集邊的伴點」而自動保留。跨叢集語意由消費端比對 source/target node 之 `labels.cluster` 推得（edge 自身僅帶 `labels.cluster`＝trace 來源／client 端 cluster）。`cluster` 設成未知值不算錯誤——該名稱僅得到空結果。
- `?namespace=<ns>`——可重複；限制 pod / PVC node 的 `namespace` 在集合內。namespace 值可跨 cluster 比對；與 `?cluster=` 併用可縮到單一 cluster 的 namespace。
- `?node=<node-name>`——可重複；限制 K8s node 名。若名稱跨 cluster 不唯一，請與 `?cluster=` 併用。
- `?edge_type=<type>`——可重複；僅保留該 edge types。若某型別在目前 `Graph` 無 edge，靜默略過（無錯誤、僅空）。
- `?root=<id>&depth=<n>&direction=in|out|both`——partial-graph traversal：自複合 ID（`<cluster>/<pod-uid>` 或 `<cluster>/<node-name>`）做 BFS，以 `depth` 為界（預設 2，最大 6）。

Filtering **在回應階段**對新建的 `*Graph` 套用，不重查 upstream。PromQL 永遠抓取範圍內所有 cluster 的完整視窗（受 `--clusters-allowlist` 限制）；該 build 的 `*Graph` 為所有 filter view 的共用基底。

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
- 本機 Kind rig（`local/kind/`）為**獨立**鏈路，使用**真實**的 `kube-state-metrics` 抓取 Kind cluster——以真 series 驗證 topology code path。不產生 `traces_service_graph_*`（無 Tempo）；service-graph code path 僅由 `internal/integration/` 驗證。

**否決：獨立 fixtures binary（`cmd/vm-fixtures/`）+ YAML 設定**——早期草案，已由「測試內直接攝入 exposition」取代。獨立 binary 帶來 build / 部署面 / YAML schema，對測試辨別力毫無增益；測試以 Go 內聯產生 series 即可。
**否決：** 多 Kind + 真實 `kube-state-metrics`（成本雙倍、筆電資源、驗證與直接攝入相同合約）；synthetic OTLP + collector（完整 pipeline 在生產上游）；`telemetrygen`（無 parent/child，servicegraph 無法配對）；OpenTelemetry Demo（過重）。

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
| Edge | `labels` | `map[string]string` | `pod-calls-pod`：`cluster`（trace 來源／client 端 cluster；client 端為 external 時省略）。`pod-mounts-pvc`：`claim_name`、`storage_class`。`pod-runs-on-node`：`scheduled_at`。新 key additive。 |

**嚴格字串型別。** Node 與 edge 的 `labels` 皆為 `map[string]string`。非字串資料（數值 edge metrics 如 `rate`、`p99_ms`、`error_rate`；boolean 如 `cross_cluster`、`ghost`）**延後**到未來 typed struct field。v1 不在 `labels` 內用 `"true"`/`"false"` 字串編 boolean；`pod-calls-pod` 的跨 cluster 狀態由消費端比對該 edge 之 source-node 與 target-node 之 `labels.cluster`（兩端 node 必同時出現於同一 response）推得。

主要 `GET /v1/graph` 回應為 **Cytoscape.js** 形狀 JSON（結構同英文 `design.md` 內範例）。

第二條 route `GET /v1/graph/nodegraph` 將相同資料投影成 **Grafana Node Graph** API datasource 形狀（平行 `nodes_fields`/`nodes` 與 `edges_fields`/`edges`）。對應：Node `name` → `title`；`labels.cluster` ` · ` `labels.namespace`（或僅 cluster）→ `subTitle`；Node `type` → `mainStat`；Edge `type` → `mainStat`；Edge `secondaryStat` v1 留空。

- 理由：單一 canonical schema 驅動兩種格式；未來欄位可加在 `labels` 以維持非破壞性。
- Edge `id` 用 UUIDv5：確定性、golden test 穩定、與可讀 `(source, target, type)` 解耦。

### D10. 以 `log/slog`、JSON handler 記錄 log

`slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: ...}))` 為預設；等級可設定。每個 HTTP 請求一行 structured log：method、path、status、duration、request ID、套用之 `cluster` filter。v1 無 cache，故無 `cache_status` 欄位。

每次 build 另有一行：`slog.Info("graph built", ...)`。

### D11. 實作要點（v1 必須）

- **封閉 graph node types**：Go interface `GraphNode` 與具體 `PodNode`、`K8sNode`、`PVCNode`、`ExternalNode`；canonical 欄位供 D9 serializer 使用。`cluster` 放在 `Labels()["cluster"]`（external 節點除外），非 wire 上獨立欄位。
- **Join／建圖路徑**：`internal/build` 的 `Builder.Build` 讀 topology + service graph（經 `promql.Client`），組裝為該時間桶內完整、未套用 HTTP filter 的多叢集 `*graph.Graph`；單元測試覆蓋 join、probe、projection，與 Prometheus 互動在 component／integration 層 mock 或真實 VM。
- **Pure projection layer**：`graph.Project(g *Graph, scope Scope) View` 對不可變 `*Graph` 套用 traversal、`cluster`／`namespace`／`node`／`edge_type` 與跨叢集 `pod-calls-pod` 端點保留（見 `internal/graph/project.go`），回傳唯讀 view；僅 pointer slice，不複製 node／edge struct。
- **Query registry**：PromQL 字串為具名常數，參數化 `<window>`、`<end>`、可選 `<clusters_allowlist>` fragment；parser 將 Prometheus `model.Vector` 映到 typed Go struct。
- **每個 metric family 一條 PromQL instant query**，在 bucket 化 `end` 以 `last_over_time` / `rate` 評估；query **不含** filter-derived selector，僅靜態 `--clusters-allowlist`。Client 端 parse Vector。
- **Parallel upstream fan-out**：`errgroup.WithContext`。Wall-clock ≈ 最慢查詢。
- **Per-build context timeout**（預設 15 s，可設定）。任一 sub-query 失敗則整次 build 中止，回 `503` 與 `Retry-After: 1`。
- **併發上限**：`golang.org/x/sync/semaphore`（預設 8 個併發 build），超出回 `503`。
- **Adjacency maps**：`Build()` 內建 forward/reverse `map[NodeID][]*Edge`；`Project()` traversal 重用。

### D12. Self-metrics 與可操作性

Server 暴露 `/metrics`（Prometheus exposition），至少含英文 `design.md` D12 所列 histogram/counter/gauge 系列名稱與 label（`kube_state_graph_*`）。

Health：`GET /livez` 行程活著即 200；`GET /readyz` 僅當便宜 upstream probe（`up{}` instant query，1 s timeout）成功為 200。

Operator：v1 無 result cache，故 **無** `DELETE /admin/cache` 路由。`GET /debug/last-queries` 在 `--enable-debug` 下已註冊路由；**v1 實作**目前回傳 **501 Not Implemented**（JSON `reason: not_implemented`），與 OpenSpec「回傳原始查詢字串＋ redacted 摘要」的完整敘述尚未對齊——須改 spec 標為遞延或於後續版本實作 capture。

### D13. 測試層級

五層皆須在 archive 變更前存在：Unit（純 join/parse/project）、Component（`httptest.Server` mock Prometheus API）、Golden（`testdata/golden`）、Property（invariants）、Integration（testcontainers VictoriaMetrics + ingest，見 `internal/integration`）。PR 上 CI 以 `go test ./...` 涵蓋含 `-race` 的整合測試；可選的 `local/kind` smoke 留在手動／本機流程。理由與英文版相同。

### D14. 版本化

- 所有 HTTP route 前綴 `/v1/`。
- Body 頂層 `apiVersion: "v1"`。
- 新 edge types 與新 `attrs` 僅 additive；移除欄位為 v2 break。
- Producer 的 `connection_type` 映到穩定內部 enum。
- `cluster` label 值透傳為不透明字串；上游改名為呼叫端可見變更，非 API break。

### D15. Edge-type discovery API

`GET /v1/edge-types` 回傳靜態 catalogue（結構同英文 `design.md` 內 JSON 範例）。無 upstream；不受時間或 filter 參數化。`Cache-Control: public, max-age=3600`；`ETag` 來自 registry compile-time hash。

### D16. v1 不用 graph database、不用 client-go informer

理由與「revisit triggers」同英文版：in-memory adjacency 在 v1 規模為微秒級；informer 無歷史 time-range。

### D17. Multi-cluster routing、discovery、cross-cluster edges

**Routing：** 皆以 query parameter 多選 `cluster`，非 path segment。理由：cross-cluster edge 自然跨多 cluster，單 cluster path 會導致語意矛盾。

**Discovery：** `GET /v1/clusters` 由 `group by (cluster) (kube_node_info)` 與可設定 lookback（`--cluster-discovery-lookback`，預設 `1h`）即時導出；每次請求都打 VictoriaMetrics，v1 不附帶 in-process discovery cache，呼叫端透過 `ETag` revalidate。若有 `--clusters-allowlist` 則與 allowlist 取交集。

**Cross-cluster edges：** 源端與目的端 pod 解析後落在不同 cluster 的 `pod-calls-pod`，與兩端 node 皆在當次 build 的全域 multi-cluster graph 中；部分 cluster scope 時保留觸及選集之 edge 與兩端 node。Edge 帶 `labels.cluster`＝client 端 cluster；跨 cluster 由消費端比對 source/target node `labels.cluster` 推得。

**Cluster 名稱：** 不透明字串透傳；無 canonicalisation；未知 `?cluster=` 名稱僅無 node，非錯誤。

### D18. External-endpoint substitution

Service-graph metrics 帶 Tempo 風格 `client` / `server` 人類可讀 label。預設以 `(cluster, client_k8s_pod_uid)` 解析 client 端 pod；server 端則以 `server_k8s_pod_uid` lookup 全域 topology pod-UID index 還原。非 pod 遠端（外部 HTTP、託管 DB、queue、SaaS）可經環境變數 `KSG_EXTERNAL_NAME_PATTERN`（或 flag `--external-name-pattern`）以 **substring contains** 判斷：若 label 值包含 pattern，則該端為 `external` node（`id = "external/<label_value>"` 等）。Client/server 兩側獨立；edge `type` 仍為 `pod-calls-pod`。Edge 之 `labels.cluster` 僅當 **client** 端為 pod 時才設值；當 client 端為 external 時，edge `labels` map 中省略 `cluster` key（external 端無 cluster scope）。

空 pattern 則關閉規則，回到純 pod UID 解析。

### D19. Allowlist 與 bounded upstream cost

- `--clusters-allowlist`：設則所有 PromQL 與 discovery 注入 `{cluster=~"a|b|c"}`；allowlist 外 cluster 對本 server 不可見。
- `--max-pods <n>`：範圍內 distinct `kube_pod_info` series 超過上限則 `503`、`reason: "cluster_too_large"`（預設 5000）。

## 風險 / 取捨

- [Per-request build cost] → 每次 `/v1/graph` 請求都會做完整 upstream PromQL fan-out（目標：範圍內 ≤ 5k pods 聚合 ≤ 3 s）。v1 無 in-process result cache，upstream 負載與 HTTP 流量線性相關；`--build-concurrency` 限制 in-flight build（超出回 `503 capacity`），`--build-timeout` 限制 tail latency（超出回 `503 timeout`）。後續 cache 機制預期吸收此成本；在那之前，ETag-based revalidation 為唯一攤平機制。
- [Pod UID 在重啟下於長 lookback 視窗內混雜] → 若 `last_over_time(kube_pod_info)` 在同一視窗內對相同 `(cluster, namespace, name)` 回傳多個 UID，**只保留**最新 UID 並丟棄舊 UID。kubelet 不再回報已刪除 UID 後，舊 pod 與新 pod 之間沒有可靠的 identity 鏈接（KSM series 直接消失，controller 為新 pod 配發全新 UUID 而無 back-reference），因此原本要發 `pod-replaced-by` synthetic edge 的構想被否決——它會暗示資料來源不支援的 identity mapping。於 spec 中文件化。
- [Service-graph metrics 缺失或稀疏] → 僅 topology 的 graph 仍有效；缺 series 則零條 `pod-calls-pod` edge，不視為 build 失敗。
- [多 cluster 時 PromQL fan-out 大] → `--clusters-allowlist` 限制 upstream 成本；超過 `--max-pods` 回 `503`、`reason: cluster_too_large`。v1 無 cache，跨呼叫者成本未攤平；後續 cache 機制再處理。
- [無 result cache → upstream 負載與流量線性相關] → 在分散式部署前的 dev 階段可接受。後續 cache 機制（Redis L2、materialiser tier、graph DB）為計畫中的緩解；設計空間刻意留空，等部署拓撲明朗再選。
- [各 scrape pipeline 的 `cluster` external label 不一致] → 缺 `cluster` label 的 series 歸入 `cluster="unknown"`，並經 `kube_state_graph_clusters_observed` 浮出；文件要求營運方一致設定 label。
- [跨 cluster edge 有一端缺 topology 資料] → 若 producer 發出 `traces_service_graph_request_total` 但某端 `client_k8s_pod_uid` 或 `server_k8s_pod_uid` 在視窗內任何 cluster 的 `kube_pod_info` 都不存在，缺的一端渲染為 synthetic ghost pod node（`attrs.ghost=true`），僅帶 `cluster` 與 `pod_uid`，而非捨棄該 edge。
- [VictoriaMetrics 中 `kube-state-metrics` retention 短於請求視窗] → `last_over_time` 回空；當上游 `up{}` 有資料但 topology 列為零時，回 `400 Bad Request`、`reason: "outside retention"`。
- [Harness 與真實 producer 漂移] → 本機 rig 使用真實 `kube-state-metrics`，已是真實 producer。整合測試以 `testcontainers-go` 直接攝入 exposition format，測試本身擁有 series 內容；只要測試的 label set 對齊 D8 合約，換成真實 producer 即為設定變更而非程式變更。
- [API 無 auth] → 文件說明本服務預期置於 reverse proxy 之後（HTTP 層另以 `X-API-Key` middleware 提供基本 key auth；見 spec）。
- [Self-metrics 上 multi-cluster cardinality] → `cluster` label 僅出現在觀測用 gauge（`graph_node_count`、`graph_edge_count`）；文件說明預期 `cluster` cardinality（v1 ≤ 20），若超過預算建議在 scrape 層捨棄該 label。

## 遷移計畫

Greenfield repository——無遷移。Rollback 為對 merge commit 做 `git revert`。JSON 合約以頂層 `apiVersion: "v1"` 版本化。

## 待決問題

- D4 所列三種以外最終 edge type 清單（例如 `pod-shares-node`、`pod-shares-namespace`）——在撰寫 spec 時定案；v1 若納入須同時出現在 `Build()` 與靜態 `/v1/edge-types` registry。v1 出貨僅含三種：`pod-runs-on-node`、`pod-mounts-pvc`、`pod-calls-pod`。
- `--max-window` 預設值（目前提案 `24h`）以及各 time class 是否應有不同上限。
- DST 或 leap second 的 bucket 邊界政策——傾向「一律 UTC、不做 DST 調整」，於 spec 確認。
- 後續分散式部署的 cache 機制形狀（Redis L2、背景 materialiser、graph DB）——以另案處理，等部署拓撲明朗再選。
- `/v1/edge-types` 是否應支援 time-window filter——延到 v1.1。
- `/v1/clusters` 是否應在回應中附 per-cluster pod / node 計數，或維持極簡（僅名稱 + first-seen / last-seen）——延到 spec。
- ~~Fake-fixtures program 形狀：持續 Deployment 穩態 metrics vs YAML 驅動 snapshot replayer~~——已決：無 fixtures 程式。本機 rig 使用真實 `kube-state-metrics`；整合測試（`internal/integration/`）以 `POST /api/v1/import/prometheus` 直接攝入 series 至 `testcontainers-go` 啟動的 VictoriaMetrics 容器。
- 要放入 `deploy/grafana/` 的確切 Grafana Node Graph dashboard JSON（含凸顯 cross-cluster edge 的版面）——延到 harness spec。
- `/v1/graph` 上用 `?format=` 是否優於獨立 `/v1/graph/nodegraph` route——延到 spec；目前偏好獨立 route。
- `KSG_EXTERNAL_NAME_PATTERN` 是否演進為 regex（`KSG_EXTERNAL_NAME_REGEX`）或接受多個逗號分隔 pattern——依真實部署回饋延到 v1.x。
- External node 是否應暴露額外 `labels`（例如從 URL 形狀值解析出 scheme）——延後；v1 僅 `labels.pattern`。
