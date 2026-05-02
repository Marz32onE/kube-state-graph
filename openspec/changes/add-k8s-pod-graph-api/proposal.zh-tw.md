## 動機

營運大量 Kubernetes cluster 的團隊需要一份單一的 JSON，能同時呈現 pod 對 pod、pod 對 node，以及**跨 cluster** 的 pod 對 pod 關係。現有工具多半停在 Service 抽象層、只涵蓋單一 cluster，或得靠臨時 PromQL 查 `kube-state-metrics` 與 service graph metrics endpoint，對互動式 UI 太慢，也不容易自然畫出跨 cluster 的 edge。

現代 service mesh 與 OTLP collector 已會輸出以 pod UID 解析的 trace metrics，每次呼叫兩端都有（`client_cluster`、`client_k8s_pod_uid`、`server_cluster`、`server_k8s_pod_uid`…）。當所有 cluster 的 metrics 都進同一套 centralised VictoriaMetrics 時，只要有人把資料與 topology join 起來並高效提供，就足以畫出統一的、跨 cluster 的 pod 層級 service graph。

## 變更內容

- 新增 Go（Gin）REST API server `kube-state-graph`，回傳統一的 nodes-and-edges JSON，涵蓋**一或多個 Kubernetes cluster** 的 pod、node、PVC topology，以及以 pod UID 解析的 RPC edge（含**跨越 cluster 邊界**的 edge）。
- 所有輸入從單一 centralised VictoriaMetrics，經 Prometheus HTTP API 讀取。每條 upstream series 預期帶有 `cluster` external label（`kube-state-metrics` series）或 `client_cluster` / `server_cluster`（service-graph series）。
- 依呼叫端指定的 `[start, end]` 時間區間**即時**建 graph，使用 PromQL `@` timestamp modifier 與 range-aware 函式（`last_over_time`、`rate`）。
- 針對多使用者 dashboard 模式優化：分層 cache——HTTP `ETag` / `Cache-Control`、以 `singleflight` 合併請求、以 in-process Ristretto cache 僅以 time bucket 為 key（filter 與 cluster 選擇在回應階段對 cached graph 套用，不同 filter 的併發使用者可共用同一 cache entry）。
- 在 `GET /v1/graph` 以 **Cytoscape.js** JSON 形狀暴露 graph，在 `GET /v1/graph/nodegraph` 以 **Grafana Node Graph** datasource 形狀暴露，方便用 Grafana dashboard 做視覺驗證。
- 提供 cluster discovery（`GET /v1/clusters`）與靜態 edge-type catalogue（`GET /v1/edge-types`），讓呼叫端可填 filter dropdown 而不必讀文件。
- 全 server 使用 Go 標準庫 `log/slog` 做 structured logging。
- 附帶 Kind 為主的 integration-test harness：單一 Kind cluster、內建 VictoriaMetrics、**fake fixtures producer** 將 synthetic multi-cluster、`kube-state-metrics` 形狀與 service-graph 形狀的 series 直接寫入 VictoriaMetrics。Harness 明確不包含真實 `kube-state-metrics` 與真實 OTLP / Alloy collector；fake fixtures 讓測試聚焦 API server，且每次執行結果可重現。

## 能力範圍

### 新增能力

- `graph-api`：HTTP API 表面（Gin），回傳合併後的跨 cluster pod / node / PVC graph（Cytoscape.js JSON）、Grafana Node Graph 相容 route、時間區間參數、filtering、partial-graph traversal、edge-type discovery、cluster discovery。
- `cluster-topology-source`：對 centralised VictoriaMetrics 發 PromQL，讀取 `kube_pod_info`、`kube_node_info`、`kube_node_status_addresses`、`kube_pod_spec_volumes_persistentvolumeclaims_info`、`kube_node_labels` 等，遵守每個來源的 `cluster` external label，組出以 `(cluster, pod-uid)` 與 `(cluster, node-name)` 為 key 的 per-cluster pod / node / PVC entity。
- `pod-service-graph`：以 pod UID 為範圍的 service graph reader：對 centralised VictoriaMetrics 發 PromQL 讀 `traces_service_graph_*` series（帶 `client_cluster`、`server_cluster`、`client_k8s_pod_uid`、`server_k8s_pod_uid`），與 topology join 後產生 pod 與 node graph node 之間的 typed edge——含 `client_cluster != server_cluster` 的跨 cluster edge。
- `verification-harness`：單一 Kind cluster harness，cluster 內安裝 VictoriaMetrics，fake fixtures producer 直接向 VictoriaMetrics 送出 multi-cluster `kube_*` 與 `traces_service_graph_*` series。Smoke script 端到端驗證 API server 在單 cluster 與跨 cluster 情境下的回應。

### 修改能力

（無——本 repository 尚無既有 spec。）

## 影響

- 新 Go module，主要依賴：Gin、Prometheus Go client、`github.com/dgraph-io/ristretto/v2`、`golang.org/x/sync/{singleflight,errgroup,semaphore}`、`log/slog`。VictoriaMetrics 經 HTTP 以 Prometheus query API 使用，不 vendor。無 `client-go`、無 informer、不存取 Kubernetes API。
- 新 HTTP API 表面（`/v1/graph`、`/v1/graph/nodegraph`、`/v1/clusters`、`/v1/edge-types`、`/v1/livez`、`/v1/readyz`、`/metrics`、可選 `/admin/cache`、`/debug/last-queries`），下游 UI 與 script 會依賴。
- repository 內新增驗證產物（Kind config、in-cluster VictoriaMetrics manifest、fake fixtures producer、smoke scripts、可選 Grafana dashboard），供 CI / 本機驗證。
- 各 upstream cluster 營運方需自行確保 scrape pipeline 對 `kube-state-metrics` 與 service-graph metrics 一致套用 `cluster` external label；文件會說明，程式不強制。
- 不修改既有程式路徑或 spec。
