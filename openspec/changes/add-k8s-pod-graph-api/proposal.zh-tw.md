## 動機

營運大量 Kubernetes cluster 的團隊需要一份單一的 JSON，能同時呈現 pod 對 pod、pod 對 node，以及**跨 cluster** 的 pod 對 pod 關係。現有工具多半停在 Service 抽象層、只涵蓋單一 cluster，或得靠臨時 PromQL 查 `kube-state-metrics` 與 service graph metrics endpoint，對互動式 UI 太慢，也不容易自然畫出跨 cluster 的 edge。

現代 service mesh 與 OTLP collector 已會輸出以 pod UID 解析的 trace metrics：每條 series 帶兩端的 pod UID（`client_k8s_pod_uid`、`server_k8s_pod_uid`…）與單一 trace 來源 cluster label（即執行 Tempo metrics-generator 的 cluster）。當所有 cluster 的 metrics 都進同一套 centralised VictoriaMetrics 時，只要有人把每條 series 用 pod UID 與 topology join（並從 topology pod-UID index 還原 server 端 cluster），就足以畫出統一的、跨 cluster 的 pod 層級 service graph。

## 變更內容

- 新增 Go（Gin）REST API server `kube-state-graph`，回傳統一的 nodes-and-edges JSON，涵蓋**一或多個 Kubernetes cluster** 的 pod、node、PVC topology，以及以 pod UID 解析的 RPC edge（含**跨越 cluster 邊界**的 edge）。
- 所有輸入從單一 centralised VictoriaMetrics，經 Prometheus HTTP API 讀取。每條 upstream series（無論 `kube-state-metrics` 或 `traces_service_graph_*`）皆預期帶單一 `cluster` external label，標示其來源 cluster（service-graph metrics 即執行 trace producer 的 cluster；server 端 cluster 由 build 時用 topology pod-UID index 還原）。
- 依呼叫端指定的 `[start, end]` 時間區間**即時**建 graph，使用 PromQL `@` timestamp modifier 與 range-aware 函式（`last_over_time`、`rate`）。
- **每次請求都重新建構 graph**（v1 不附帶 in-process result cache，也不附帶 singleflight）。每次請求都對 centralised VictoriaMetrics 執行一次完整 PromQL fan-out。Filter、cluster 選擇、traversal 仍在回應階段對新建的 graph 做 projection。回應帶 HTTP `ETag` strong validator（body 的 sha256），用於 RFC 9110 §13.1 的 **conditional GET**：呼叫端送 `If-None-Match` 時 server 仍跑完整 build 並重算 sha256，只在 body byte-identical 時回 `304 Not Modified` 省下 body 傳輸與 client 端反序列化成本——但**不**省 upstream PromQL 評估，這是 v1 不附 server-side cache 的代價。Validator 為 content-addressed，server 端不需保留 request 之間的狀態。針對分散式部署的水平擴展 cache 機制留待之後另案處理（design.md 的「Future cache mechanism」）。
- 在 `GET /v1/graph` 以 **Cytoscape.js** JSON 形狀暴露 graph，在 `GET /v1/graph/nodegraph` 以 **Grafana Node Graph** datasource 形狀暴露，方便用 Grafana dashboard 做視覺驗證。
- 提供 cluster discovery（`GET /v1/clusters`）與靜態 edge-type catalogue（`GET /v1/edge-types`），讓呼叫端可填 filter dropdown 而不必讀文件。
- 全 server 使用 Go 標準庫 `log/slog` 做 structured logging。
- 附帶兩條獨立驗證路徑：(1) **本機 Kind 視覺驗證 rig**（`local/kind/`）— 單一 Kind cluster、內建 VictoriaMetrics、真實 `kube-state-metrics` 抓取 Kind cluster（scrape config 注入 `cluster=kind-local` external label），加上 Grafana Pod 與儀表板，由人工開啟瀏覽；單 cluster、不產生 service-graph metrics。(2) **CI 整合測試**（`internal/integration/`）— 以 `testcontainers-go` 啟動短生命 VictoriaMetrics 容器，由 Go 測試直接以 Prometheus exposition format 推入手刻多 cluster `kube_*` 與 `traces_service_graph_*` series，驗證跨 cluster、external substitution 等行為。兩條路徑分工明確：本機 rig 驗證真實 topology 路徑，整合測試驗證多 cluster／service-graph 路徑。**不**包含 OTLP collector、Alloy、或獨立 fixtures binary。

## 能力範圍

### 新增能力

- `graph-api`：HTTP API 表面（Gin），回傳合併後的跨 cluster pod / node / PVC graph（Cytoscape.js JSON）、Grafana Node Graph 相容 route、時間區間參數、filtering、partial-graph traversal、edge-type discovery、cluster discovery。
- `cluster-topology-source`：對 centralised VictoriaMetrics 發 PromQL，讀取 `kube_pod_info`、`kube_node_info`、`kube_node_status_addresses`、`kube_pod_spec_volumes_persistentvolumeclaims_info`、`kube_node_labels` 等，遵守每個來源的 `cluster` external label，組出以 `(cluster, pod-uid)` 與 `(cluster, node-name)` 為 key 的 per-cluster pod / node / PVC entity。
- `pod-service-graph`：以 pod UID 為範圍的 service graph reader：對 centralised VictoriaMetrics 發 PromQL 讀 `traces_service_graph_*` series（帶 `cluster`＝trace 來源／client 端 cluster、`client_k8s_pod_uid`、`server_k8s_pod_uid`）。Reader 以 `(cluster, client_k8s_pod_uid)` 對 topology join client 端，以全域 topology pod-UID index lookup `server_k8s_pod_uid` 還原 server 端，再產生 pod 與 node graph node 之間的 typed edge——含源端與目的端 pod 解析後落在不同 cluster 的跨 cluster edge。
- `verification-harness`：單一 Kind cluster **僅供人工** 操作的 rig，cluster 內安裝 VictoriaMetrics，並由真實 `kube-state-metrics` 抓取 Kind cluster（scrape config 注入 `cluster=kind-local`），加上預先佈建的 Grafana 與 Node Graph 儀表板。單 cluster、無 `traces_service_graph_*`；多 cluster／跨 cluster／service-graph code path 由 `container-integration` 在 CI 中以 testcontainers-go 驗證。Smoke script 為本地端到端 sanity check，CI **不**執行。

### 修改能力

（無——本 repository 尚無既有 spec。）

## 影響

- 新 Go module，主要依賴：Gin、Prometheus Go client、`golang.org/x/sync/{errgroup,semaphore}`、`log/slog`。VictoriaMetrics 經 HTTP 以 Prometheus query API 使用，不 vendor。無 `client-go`、無 informer、不存取 Kubernetes API。
- 新 HTTP API 表面（`/v1/graph`、`/v1/graph/nodegraph`、`/v1/clusters`、`/v1/edge-types`、`/v1/livez`、`/v1/readyz`、`/metrics`、可選 `/debug/last-queries`），下游 UI 與 script 會依賴。
- repository 內新增驗證產物：本機 rig（Kind config、in-cluster VictoriaMetrics manifest、真實 `kube-state-metrics` Deployment manifest 含 scrape-side `cluster=kind-local` relabel、Grafana 與儀表板、`local/kind/smoke.sh`）僅供人工執行；CI 整合測試在 `internal/integration/` 用 `testcontainers-go` 跑。無獨立 fixtures binary。
- 各 upstream cluster 營運方需自行確保 scrape pipeline 對 `kube-state-metrics` 與 service-graph metrics 一致套用 `cluster` external label；文件會說明，程式不強制。
- 不修改既有程式路徑或 spec。
