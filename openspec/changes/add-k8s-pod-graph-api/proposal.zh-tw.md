## 動機

營運大量 Kubernetes cluster 的團隊需要一份單一的 JSON，能同時呈現 pod 對 pod、pod 對 node，以及**跨 cluster** 的 pod 對 pod 關係。現有工具多半停在 Service 抽象層、只涵蓋單一 cluster，或得靠臨時 PromQL 查 `kube-state-metrics` 與 service graph metrics endpoint，對互動式 UI 太慢，也不容易自然畫出跨 cluster 的 edge。

現代 service mesh 與 OTLP collector 已會輸出以 pod UID 解析的 trace metrics：每條 series 帶兩端的 pod UID（`client_k8s_pod_uid`、`server_k8s_pod_uid`…）與單一 trace 來源 cluster label（即執行 Tempo metrics-generator 的 cluster）。當所有 cluster 的 metrics 都進同一套 centralised VictoriaMetrics 時，只要有人把每條 series 用 pod UID 與 topology join（並從 topology pod-UID index 還原 server 端 cluster），就足以畫出統一的、跨 cluster 的 pod 層級 service graph。

## 變更內容

- 新增 Go（Gin）REST API server `kube-state-graph`，回傳統一的 nodes-and-edges JSON，涵蓋**一或多個 Kubernetes cluster** 的 pod、node、PVC topology，以及以 pod UID 解析的 RPC edge（含**跨越 cluster 邊界**的 edge）。
- 所有輸入從單一 centralised VictoriaMetrics，經 Prometheus HTTP API 讀取。每條 upstream series（無論 `kube-state-metrics` 或 `traces_service_graph_*`）皆預期帶單一 `cluster` external label，標示其來源 cluster（service-graph metrics 即執行 trace producer 的 cluster；server 端 cluster 由 build 時用 topology pod-UID index 還原）。
- 依呼叫端指定的 `[start, end]` 時間區間**即時**建 graph，使用 PromQL `@` timestamp modifier 與 range-aware 函式（`last_over_time`、`rate`）。
- **每次請求都重新建構 graph**（v1 不附帶 in-process result cache，也不附帶 singleflight）。每次請求都對 centralised VictoriaMetrics 執行一次完整 PromQL fan-out。Filter、cluster 選擇、traversal 仍在回應階段對新建的 graph 做 projection。針對分散式部署的水平擴展 cache 機制留待之後另案處理（design.md 的「Future cache mechanism」）。
- 解析 label 為**連線字串**（connection string）的 service-graph endpoint（以內建 `"://"` 子字串檢測，僅在該 endpoint 的 pod UID 為空時逐端評估）。將 host 當成 Kubernetes `.svc` DNS 名稱解析，並對 topology 解析：headless-service 字串（`<pod-hostname>.<service>.<namespace>`）解析為**真實後端 pod** node（此時 edge 帶 client 端 cluster）；ClusterIP／service 層級字串（`<service>.<namespace>`）解析為新的 `type="service"` node，並按需 materialise 出指向各後端 pod 的 `service-selects-pod` edge。兩者皆來源於 `kube_service_info` 與 `kube_endpointslice_endpoints` 的 topology index。當 host 不是可解析的 in-cluster `.svc` 名稱、或該 service／pod 不在 topology 中、或跨 cluster 配對不唯一時，該 endpoint 退回為 `others/<label>` node（`type="others"`、`name` 為原樣字串、`labels={}`），讓真正的外部 URL 與未知名稱仍可見。
- 完全移除 `KSG_OTHERS_NAME_PATTERN` 旋鈕（env var、`--others-name-pattern` flag 與 config 欄位），改用上述內建 `"://"` 連線字串檢測。`type="others"` 現在表示一個被識別為 `"://"` 連線字串、但未解析到 in-cluster pod 或 service 的 endpoint（已宣告的外部相依）；`type="external"` 仍是 pod UID 為空且其 human label **不是** URL 的 endpoint（producer-regression 訊號）。兩個 ID namespace（`others/<label>` 與 `external/<label>`）維持互斥。
- 在每個 node 的回應載荷加上 typed top-level 屬性 `ipaddress`（`string[]`，`omitempty`）。`type="pod"` 帶 `kube_pod_info.pod_ip`；`type="node"` 帶 `kube_node_status_addresses` 中的 `ExternalIP`；`type="service"` 帶 `kube_service_info.cluster_ip`（headless service 即 `cluster_ip="None"` 時不帶）；`type="pvc"`、`type="external"` 與 `type="others"` 不帶。`labels.pod_ip` / `labels.host_ip` / `labels.external_ip` 一律不輸出 — `labels` 嚴格保持為純類型/拓撲 metadata。
- 在 `GET /v1/graph` 以 **Cytoscape.js** JSON 形狀暴露 graph。
- 提供 cluster discovery（`GET /v1/clusters`）與靜態 edge-type catalogue（`GET /v1/edge-types`），讓呼叫端可填 filter dropdown 而不必讀文件。
- 全 server 使用 Go 標準庫 `log/slog` 做 structured logging。
- 附帶 **CI 整合測試**（`internal/integration/`）作為唯一驗證路徑 — 以 `testcontainers-go` 啟動短生命 VictoriaMetrics 容器，由 Go 測試直接以 Prometheus exposition format 推入手刻多 cluster `kube_*` 與 `traces_service_graph_*` series，驗證真實 topology、跨 cluster、external substitution、service-graph 等所有行為。**不**包含 OTLP collector、Alloy、或獨立 fixtures binary。

## 能力範圍

### 新增能力

- `graph-api`：HTTP API 表面（Gin），回傳合併後的跨 cluster pod / node / PVC / service graph（Cytoscape.js JSON）、時間區間參數、filtering、partial-graph traversal、edge-type discovery、cluster discovery。node 類型集合包含 `type="service"` node（ClusterIP／service 層級連線字串的目標），edge-type catalogue 包含 `service-selects-pod` edge（有向 service→pod、intra-cluster）。
- `cluster-topology-source`：對 centralised VictoriaMetrics 發 PromQL，讀取 `kube_pod_info`、`kube_node_info`、`kube_node_status_addresses`、`kube_pod_spec_volumes_persistentvolumeclaims_info`、`kube_node_labels`、`kube_service_info`、`kube_endpointslice_endpoints`、`kube_endpointslice_labels` 等，遵守每個來源的 `cluster` external label，組出以 `(cluster, pod-uid)` 與 `(cluster, node-name)` 為 key 的 per-cluster pod / node / PVC entity。並額外建立 service／endpoint index（`ServicesByNameNS`、把 endpointslice join 到 topology pod 的 `EndpointsByService`、`PodsByNameNS`），供連線字串 service-graph endpoint 解析使用；service node 與 `service-selects-pod` edge 由 service-graph reader 按需 materialise，而非整批輸出。
- `pod-service-graph`：以 pod UID 為範圍的 service graph reader：對 centralised VictoriaMetrics 發 PromQL 讀 `traces_service_graph_*` series（帶 `cluster`＝trace 來源／client 端 cluster、`client_k8s_pod_uid`、`server_k8s_pod_uid`）。Reader 以 `(cluster, client_k8s_pod_uid)` 對 topology join client 端，以全域 topology pod-UID index lookup `server_k8s_pod_uid` 還原 server 端，再產生 pod 與 node graph node 之間的 typed edge——含源端與目的端 pod 解析後落在不同 cluster 的跨 cluster edge。當某 endpoint 的 pod UID 為空且 label 為 `"://"` 連線字串時，reader 會對 topology service／endpoint index 執行連線字串解析：headless-service 字串解析為真實後端 pod、ClusterIP／service 層級字串解析為 `type="service"` node 並按需產生指向各後端 pod 的 `service-selects-pod` edge、無法解析者退回為 `others/<label>` node。

### 修改能力

（無——本 repository 尚無既有 spec。）

## 影響

- 新 Go module，主要依賴：Gin、Prometheus Go client、`golang.org/x/sync/{errgroup,semaphore}`、`log/slog`。VictoriaMetrics 經 HTTP 以 Prometheus query API 使用，不 vendor。無 `client-go`、無 informer、不存取 Kubernetes API。
- 新 HTTP API 表面（`/v1/graph`、`/v1/clusters`、`/v1/edge-types`、`/v1/livez`、`/v1/readyz`、`/metrics`、可選 `/debug/last-queries`），下游 UI 與 script 會依賴。
- repository 內新增驗證產物：CI 整合測試在 `internal/integration/` 用 `testcontainers-go` 跑。無獨立 fixtures binary。
- 各 upstream cluster 營運方需自行確保 scrape pipeline 對 `kube-state-metrics` 與 service-graph metrics 一致套用 `cluster` external label；文件會說明，程式不強制。
- 不修改既有程式路徑或 spec。
