# kube-state-graph

[English README](README.md)

以 Go 實作的 REST API，回傳一或多個 Kubernetes 叢集上統一的 pod／node／PVC 圖，包含可依 pod UID 對應的 RPC 邊（`pod-calls-pod`），且邊可跨叢集。

```
cluster A: kube-state-metrics ──┐
           service-graph source ┤
                                 │  (vmagent / Prometheus
cluster B: kube-state-metrics ──┤   帶 external_labels:
           service-graph source ┤   { cluster: "<name>" })
                                 │
       ...                       ├──► centralised VictoriaMetrics ◄── kube-state-graph
                                 │                                     （Prometheus HTTP API）
cluster N: kube-state-metrics ──┤
           service-graph source ─┘
```

## 功能概要

- 依呼叫端指定的 `[start, end]`，從**單一**集中式 VictoriaMetrics 讀取 `kube_*` 拓樸與 `traces_service_graph_*` 執行期指標。
- Join 成多叢集圖，節點鍵為帶叢集範圍的 pod UID 與 node 名稱。
- 回傳 Cytoscape.js JSON（`/v1/graph`）或 Grafana Node Graph 資料源形狀（`/v1/graph/nodegraph`）。
- 提供叢集探索（`/v1/clusters`）與靜態邊類型目錄（`/v1/edge-types`）。
- 每次請求都重新建圖——v1 **不附帶 in-process result cache，也不附帶 singleflight**。回應仍帶 HTTP `ETag`（body 的 sha256），呼叫端可透過 `If-None-Match` 取得 `304 Not Modified` 而免去 body 傳輸。後續分散式部署的水平擴展 cache 機制留待之後另案處理。呼叫端送出的 `start` / `end` 在通過 `--max-window` / `--max-skew` 驗證後直接 pass through 給上游 PromQL，**不做** server-side bucketing 或 alignment。回應 body 只包含 `apiVersion`、`clusters`、`elements`。

## 快速開始

```bash
make build
./bin/kube-state-graph \
  --prom-url=http://victoria-metrics.example:8428 \
  --listen-addr=:8080
```

查詢範例（`start`／`end` 為 Unix 秒，下列寫法在 macOS 與 Linux 皆可）：

```bash
curl 'http://localhost:8080/v1/clusters'
end=$(date -u +%s)
start=$((end - 300))
curl "http://localhost:8080/v1/graph?start=${start}&end=${end}" | jq '.elements'
```

## 上游指標

每次請求都會對集中式 VictoriaMetrics 發出 PromQL（v1 無結果快取）。各 series 預期帶有由 `vmagent`／Prometheus `external_labels` 寫入的 `cluster` 標籤。

### 拓樸指標 — 由 [`kube-state-metrics`](https://github.com/kubernetes/kube-state-metrics) 產出

| 指標 | 用途 | 會讀的標籤 | 必填？ |
|---|---|---|---|
| `kube_pod_info` | Pod 節點、`pod-runs-on-node` 邊 | `cluster`, `namespace`, `pod`, `uid`, `node`, `pod_ip`, `host_ip` | **是** |
| `kube_node_info` | K8s node 節點 | `cluster`, `node` | **是** |
| `kube_node_status_addresses{type="ExternalIP"}` | Node 的 `external_ip` 標籤 | `cluster`, `node`, `address` | 選填 |
| `kube_node_labels` | 傳遞 node 標籤（`kubernetes.io/*` 等） | `cluster`, `node`, `label_*` | 選填 |
| `kube_pod_spec_volumes_persistentvolumeclaims_info` | PVC 節點、`pod-mounts-pvc` 邊 | `cluster`, `namespace`, `pod`, `persistentvolumeclaim`, `volume` | 選填（無 PVC 則無相關節點／邊） |

各指標以 `last_over_time(<metric>[<window>]) @ <end>` 包裝，反映請求視窗 `[start, end]` 內最後觀測值。

### Service graph 指標 — 由 [Tempo](https://grafana.com/docs/tempo/latest/metrics-generator/service_graphs/) 或相容產生器產出

| 指標 | 用途 | 會讀的標籤 | 必填？ |
|---|---|---|---|
| `traces_service_graph_request_total` | `pod-calls-pod` 邊（叢集內與跨叢集） | `cluster`, `client`, `server`, `client_k8s_pod_uid`, `server_k8s_pod_uid` 等 | 選填（無 series 則無呼叫邊） |

以 `rate(traces_service_graph_request_total[<window>]) @ <end>` 評估。每條 series 帶單一 `cluster` external label，代表追蹤來源（通常是執行 Tempo metrics-generator 的 cluster），即呼叫的 **client 端** cluster。**Server 端** cluster 由 build 時把 `server_k8s_pod_uid` 對全域 topology pod-UID index join 還原——K8s pod UID 在實務上跨 cluster 唯一，lookup 可明確還原。僅在兩端都能解析（pod UID 已知，或符合設定的 `KSG_EXTERNAL_NAME_PATTERN` 而替換成 `external` 節點）時才輸出邊。

### 探針 — 診斷用，不屬於圖資料

| PromQL | 用途 |
|---|---|
| `group by (cluster) (last_over_time(kube_node_info[1h]))` | 驅動 `GET /v1/clusters` |
| `up` | 區分「視窗內無資料」（`outside_retention`）與「上游正常但視窗為空」 |

### 邊類型 ↔ 指標

| 邊類型 | 來源指標 |
|---|---|
| `pod-runs-on-node` | `kube_pod_info`（pod 的 `node` 標籤） |
| `pod-mounts-pvc` | `kube_pod_spec_volumes_persistentvolumeclaims_info` |
| `pod-calls-pod` | `traces_service_graph_request_total` |

### 本機驗證環境

樹內 **`local/kind/`** 腳本會對真實 Kind 叢集 scrape `kube-state-metrics`（`kube_pod_info`、`kube_node_info`、`kube_node_labels`、`kube_pod_spec_volumes_persistentvolumeclaims_info`），並由 Grafana Beyla DaemonSet 以 eBPF 自動 instrument `kube-state-graph` namespace 內的 pod，將 OTLP spans 送到 Grafana Alloy Deployment；Alloy 的 `otelcol.connector.servicegraph`（設定 `dimensions=["k8s.pod.uid"]`）會把 Beyla 帶入的 per-pod resource attribute 提升為 `client_k8s_pod_uid` 與 `server_k8s_pod_uid`，再 remote-write 到 VictoriaMetrics。`pod-calls-pod` 邊由叢集內既有的 Go 服務流量（`kube-state-graph → VictoriaMetrics → kube-state-metrics`、Grafana → kube-state-graph 等）驅動，不需要額外的合成流量產生器。單一 Kind 無法模擬的跨叢集情境，仍由 **`internal/integration/`** 搭配 testcontainers 起的 VictoriaMetrics 涵蓋。

## 設定

| 旗標 | 環境變數 | 預設值 | 說明 |
|---|---|---|---|
| `--prom-url` | `KSG_PROM_URL` | `http://localhost:8428` | VictoriaMetrics Prometheus 相容 endpoint。 |
| `--listen-addr` | `KSG_LISTEN_ADDR` | `:8080` | HTTP 監聽位址。 |
| `--build-timeout` | `KSG_BUILD_TIMEOUT` | `15s` | `/v1/graph` 與 `/v1/graph/nodegraph` 的單次建圖 context 逾時。 |
| `--api-timeout` | `KSG_API_TIMEOUT` | `5s` | 非 graph 端點的 upstream 呼叫逾時（`/v1/clusters`、`/readyz`）。 |
| `--external-name-pattern` | `KSG_EXTERNAL_NAME_PATTERN` | （空） | 子字串；`client`／`server` 符合時該端成 `external` 節點。 |
| `--api-keys-file` | `KSG_API_KEYS_FILE` | （空） | 接受的 API key 檔案路徑（每行一個，`#` 為註解）。為 K8s `Secret` 掛載而設計，會週期性重新讀取。 |
| `--api-keys` | `KSG_API_KEYS` | （空） | 逗號分隔字面 key；僅 dev 用途，設了 `--api-keys-file` 即忽略。 |
| `--api-keys-reload-interval` | `KSG_API_KEYS_RELOAD_INTERVAL` | `30s` | `--api-keys-file` 重新讀取頻率；`0` 關閉熱重載。 |
| `--log-level` | `KSG_LOG_LEVEL` | `info` | `debug \| info \| warn \| error`。 |
| `--metric-prefix` | `KSG_METRIC_PREFIX` | （空） | 附加在拓樸 reader 查詢的 kube-state-metrics 系列名稱前的前綴（例如 `o11y_` → `o11y_kube_pod_info`）。**不**影響 `traces_service_graph_request_total` 或 `up{}`。詳見 [exporter 相容性合約](docs/operations.md#exporter-compatibility-contract)。 |

## 文件

- [API 參考](docs/api.md)（英文）
- [多叢集部署](docs/multi-cluster.md)
- [External 名稱替換](docs/external-substitution.md)
- [營運](docs/operations.md)

## 開發

### 第一次設定

clone 後**只跑一次**。下載 modules、安裝 host-level 工具（`golangci-lint`、
`govulncheck`）。Mockery 由 go.mod 的 `tool` directive（Go 1.24+）追蹤，透過
`go tool mockery` 呼叫，不需另外安裝。

```bash
make init           # go mod download + dev tools
make doctor         # 檢查工具版本（go、golangci-lint、govulncheck、mockery、docker、kind）
make init-hooks     # （選用）安裝 pre-commit hook（gofmt + go vet）
```

需求：Go 1.25+。`go.mod` 中 pin 的 toolchain（目前 `go1.26.3`）會在第一次 build 時自動下載。

### 日常指令

```bash
make build          # 編譯主程式
make test           # 單元 + 元件 + golden + property + integration（需 Docker）
make lint           # golangci-lint
make vuln           # govulncheck
make check-docs     # OpenAPI 與嵌入靜態檔是否與 swag 產出一致（CI 亦跑）
make local-up       # 本機 Kind + VM + 儀表板腳本（見 local/kind/）
make local-smoke    # 對已啟動環境跑 smoke
make local-down     # 拆除
```

### Mocks（mockery）

production-side 依賴透過小介面（`promql.Querier`、`auth.Validator`、`clock.Clock`）暴露，單元測試用 mockery 生成的 mock 注入，**不再用 `httptest.NewServer` 假冒上游服務**。Mock 放在 `internal/<pkg>/mocks/` 並 commit 進 git，CI 不需安裝 mockery。

```bash
make mocks          # 編輯介面後重新產生 mocks
make verify-mocks   # CI 風格的 freshness 檢查（regen + git diff）
```

`.mockery.yaml` 列出所有設定的介面。**新增或修改介面後**請執行 `make mocks` 並 commit；否則 CI 的 `mocks-drift` job 會擋下 merge。

### 測試分層

| 層級 | 位置 | 真實 I/O？ |
|---|---|---|
| Unit | `internal/{graph,build,promql,config,clock,auth,telemetry}/*_test.go` | 無 — 純 Go。 |
| Component | `internal/api/*_test.go` | 無 — 透過介面注入 `MockQuerier`；`httptest.NewServer` 只用於包裹 server-under-test，不假冒上游。 |
| Golden | `internal/api/golden_test.go` + `testdata/golden/*.json` | 無。執行 `-update` 重新生成 snapshot。 |
| Integration | `internal/integration/*` | **需 Docker。** testcontainers-go 啟動真實 VictoriaMetrics 容器；`SkipIfDockerUnavailable` 在沒有 Docker 的本機自動跳過，CI 跑全套。 |
| 手動 rig | `local/kind/smoke.sh` | Kind cluster — 只在本機跑，CI 不執行。 |

unit 與 integration 邊界嚴格：**任何透過 TCP socket 連到上游的測試都歸類為 integration**。單元測試必須能在無外部相依下執行。整合測試走 `internal/integration/` + testcontainers-go：直接以 Prometheus 文字曝露格式把 fixture series 推進臨時 VictoriaMetrics 容器。本地 Kind rig 走 `local/kind/`，由 kube-state-metrics 直接抓取真實 Kind cluster 產生 topology series。

## 授權

Apache-2.0
