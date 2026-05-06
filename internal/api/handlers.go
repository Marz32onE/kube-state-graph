package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/promql"
	"github.com/marz32one/kube-state-graph/internal/timewindow"
)

// ----- /v1/graph (Cytoscape.js) ---------------------------------------------

// handleGraph returns the multi-cluster pod / node / PVC graph for [start,end].
//
//	@Summary		Get multi-cluster graph (Cytoscape.js)
//	@Description	Returns the joined multi-cluster pod / node / PVC graph for the supplied `[start, end]` window in Cytoscape.js JSON shape (`{ elements: { nodes:[…], edges:[…] } }`).
//	@Description
//	@Description	**Window**: `start`/`end` accept RFC 3339 or Unix seconds. Window is aligned onto a 60 s grid (`start` floored, `end` ceiled). Each request triggers a fresh upstream PromQL fan-out — there is no in-process result cache.
//	@Description
//	@Description	**Filters** (all repeatable; AND across param names, OR within a single name): `cluster`, `namespace`, `edge_type`, `pod`.
//	@Description
//	@Description	**Traversal** (set `root` to enable): `depth` 0..6 (default 2), `direction` `in`/`out`/`both` (default `both`).
//	@Description
//	@Description	**Caching**: response carries a content-addressed `ETag` so callers may revalidate via `If-None-Match` and receive `304 Not Modified` when the body would be unchanged. No `Cache-Control` is emitted.
//	@Description
//	@Description	Example: `GET /v1/graph?start=2026-05-05T11:00:00Z&end=2026-05-05T12:00:00Z&cluster=prod-eu&namespace=payments&edge_type=pod-calls-pod`
//	@Description
//	@Description	<details><summary><b>Sample response</b></summary>
//	@Description
//	@Description	```json
//	@Description	{
//	@Description	  "apiVersion": "v1",
//	@Description	  "start": "2026-05-05T11:00:00Z",
//	@Description	  "end":   "2026-05-05T12:00:00Z",
//	@Description	  "start_actual": "2026-05-05T11:00:00Z",
//	@Description	  "end_actual":   "2026-05-05T12:00:00Z",
//	@Description	  "bucket_seconds": 60,
//	@Description	  "clusters": ["prod-eu", "prod-us"],
//	@Description	  "elements": {
//	@Description	    "nodes": [
//	@Description	      { "data": { "id": "prod-eu/8f8d4f1a-...-89ab", "type": "pod",  "name": "checkout-7d9f6c8b8-abcde", "labels": { "cluster": "prod-eu", "namespace": "payments" } } },
//	@Description	      { "data": { "id": "prod-eu/ip-10-0-1-23",     "type": "node", "name": "ip-10-0-1-23.ec2.internal", "labels": { "cluster": "prod-eu" } } }
//	@Description	    ],
//	@Description	    "edges": [
//	@Description	      { "data": { "id": "...uuidv5...", "type": "pod-runs-on-node", "source": "prod-eu/8f8d4f1a-...-89ab", "target": "prod-eu/ip-10-0-1-23", "labels": {} } },
//	@Description	      { "data": { "id": "...uuidv5...", "type": "pod-calls-pod",   "source": "prod-eu/8f8d4f1a-...-89ab", "target": "prod-us/a1b2c3d4-...-7654", "labels": { "cluster": "prod-eu" } } }
//	@Description	    ]
//	@Description	  }
//	@Description	}
//	@Description	```
//	@Description
//	@Description	</details>
//	@Tags			graph
//	@Produce		json
//	@Param			start		query		string		true	"Window start. RFC 3339 (`2026-05-05T11:00:00Z`) or Unix seconds (`1746442800`). Floored to the 60 s grid."	example(2026-05-05T11:00:00Z)
//	@Param			end			query		string		true	"Window end. RFC 3339 or Unix seconds. Ceiled to the 60 s grid; clamped to `floor(now, 60s)` if it would exceed now. Must be > start and within --max-window."	example(2026-05-05T12:00:00Z)
//	@Param			cluster		query		[]string	false	"Restrict to listed clusters (repeatable, OR-combined). Names match the upstream `cluster` label."	collectionFormat(multi)	example(prod-eu)
//	@Param			namespace	query		[]string	false	"Restrict to listed Kubernetes namespaces (repeatable, OR-combined)."	collectionFormat(multi)	example(payments)
//	@Param			edge_type	query		[]string	false	"Restrict to listed edge types. Repeatable, OR-combined."	collectionFormat(multi)	Enums(pod-runs-on-node,pod-mounts-pvc,pod-calls-pod)	example(pod-calls-pod)
//	@Param			pod			query		[]string	false	"Restrict to pods whose name matches exactly. Repeatable; multi-cluster name collisions return all matches."	collectionFormat(multi)	example(checkout-7d9f6c8b8-abcde)
//	@Param			root		query		string		false	"Cluster-scoped node ID anchoring a traversal. Format depends on type — pods `<cluster>/<uid>`, nodes `<cluster>/<node>`, PVCs `<cluster>/<ns>/<claim>`, externals `external/<value>`."	example(prod-eu/8f8d4f1a-1234-4abc-9def-0123456789ab)
//	@Param			depth		query		int			false	"BFS traversal depth in hops. Range `0..6`. Defaults to `2` when `root` is set, ignored otherwise."	minimum(0)	maximum(6)	default(2)	example(2)
//	@Param			direction	query		string		false	"Traversal direction relative to `root`. `out` = downstream edges, `in` = upstream edges, `both` = undirected. Defaults to `both`."	Enums(in,out,both)	default(both)	example(both)
//	@Param			X-API-Key	header		string		false	"API key. Required when the server is started with API keys configured."
//	@Success		200			{object}	cytoscapeBody
//	@Failure		400			{object}	errorBody	"Invalid parameters (missing/invalid start|end, window_too_large, end_in_future, depth_too_large, invalid_scope)"
//	@Failure		401			{object}	errorBody	"Missing or invalid `X-API-Key` (only when API key auth is configured)"
//	@Failure		503			{object}	errorBody	"Capacity / timeout / cluster_too_large or upstream unavailable"
//	@Security		ApiKeyAuth
//	@Router			/v1/graph [get]
func (s *Server) handleGraph(c *gin.Context) {
	req, errBody := s.parseGraphRequest(c)
	if errBody != nil {
		return
	}
	window := req.window.EndActual.Sub(req.window.StartActual)
	g, err := s.orch.Resolve(c.Request.Context(), window, req.window.EndActual)
	if err != nil {
		mapBuildError(c, err)
		return
	}

	ptStart := time.Now()
	view := graph.Project(g, req.scope)
	s.metrics.ProjectDuration.Observe(time.Since(ptStart).Seconds())
	body := serialiseCytoscape(req, g, view)
	s.writeJSON(c, body, "cytoscape")
}

// ----- /v1/graph/nodegraph (Grafana) ----------------------------------------

// handleNodeGraph returns the same graph as /v1/graph in Grafana Node Graph
// datasource shape.
//
//	@Summary		Get multi-cluster graph (Grafana Node Graph datasource)
//	@Description	Same underlying graph as `/v1/graph` but projected into the parallel-array shape Grafana's Node Graph panel expects via the JSON / Infinity datasource: `nodes_fields[]`, `nodes[]`, `edges_fields[]`, `edges[]`.
//	@Description
//	@Description	Filtering, traversal, alignment, and ETag semantics are identical to `/v1/graph` — see that endpoint for full details.
//	@Description
//	@Description	Example: `GET /v1/graph/nodegraph?start=1746442800&end=1746446400&cluster=prod-eu&edge_type=pod-calls-pod&root=prod-eu/8f8d4f1a-1234-4abc-9def-0123456789ab&depth=3&direction=out`
//	@Description
//	@Description	<details><summary><b>Sample response</b></summary>
//	@Description
//	@Description	```json
//	@Description	{
//	@Description	  "apiVersion": "v1",
//	@Description	  "nodes_fields": [
//	@Description	    { "name": "id",    "type": "string" },
//	@Description	    { "name": "title", "type": "string" },
//	@Description	    { "name": "subtitle", "type": "string" }
//	@Description	  ],
//	@Description	  "nodes": [
//	@Description	    { "id": "prod-eu/8f8d4f1a-...-89ab", "title": "checkout-7d9f6c8b8-abcde", "subtitle": "pod" }
//	@Description	  ],
//	@Description	  "edges_fields": [
//	@Description	    { "name": "id",     "type": "string" },
//	@Description	    { "name": "source", "type": "string" },
//	@Description	    { "name": "target", "type": "string" }
//	@Description	  ],
//	@Description	  "edges": [
//	@Description	    { "id": "...uuidv5...", "source": "prod-eu/8f8d4f1a-...-89ab", "target": "prod-us/a1b2c3d4-...-7654" }
//	@Description	  ]
//	@Description	}
//	@Description	```
//	@Description
//	@Description	</details>
//	@Tags			graph
//	@Produce		json
//	@Param			start		query		string		true	"Window start. RFC 3339 or Unix seconds. Floored to the 60 s grid."	example(2026-05-05T11:00:00Z)
//	@Param			end			query		string		true	"Window end. RFC 3339 or Unix seconds. Ceiled to the 60 s grid; clamped to now."	example(2026-05-05T12:00:00Z)
//	@Param			cluster		query		[]string	false	"Restrict to listed clusters (repeatable, OR-combined)."	collectionFormat(multi)	example(prod-eu)
//	@Param			namespace	query		[]string	false	"Restrict to listed namespaces (repeatable, OR-combined)."	collectionFormat(multi)	example(payments)
//	@Param			edge_type	query		[]string	false	"Restrict to listed edge types. Repeatable, OR-combined."	collectionFormat(multi)	Enums(pod-runs-on-node,pod-mounts-pvc,pod-calls-pod)	example(pod-calls-pod)
//	@Param			pod			query		[]string	false	"Restrict to pods whose name matches exactly. Repeatable."	collectionFormat(multi)	example(checkout-7d9f6c8b8-abcde)
//	@Param			root		query		string		false	"Cluster-scoped node ID anchoring a traversal. See /v1/graph for ID formats per type."	example(prod-eu/8f8d4f1a-1234-4abc-9def-0123456789ab)
//	@Param			depth		query		int			false	"BFS traversal depth `0..6`. Defaults to `2` when `root` is set."	minimum(0)	maximum(6)	default(2)	example(2)
//	@Param			direction	query		string		false	"Traversal direction. Defaults to `both`."	Enums(in,out,both)	default(both)	example(both)
//	@Param			X-API-Key	header		string		false	"API key. Required when the server is started with API keys configured."
//	@Success		200			{object}	grafanaBody
//	@Failure		400			{object}	errorBody	"Invalid parameters"
//	@Failure		401			{object}	errorBody	"Missing or invalid `X-API-Key` (only when API key auth is configured)"
//	@Failure		503			{object}	errorBody	"Capacity / timeout / upstream unavailable"
//	@Security		ApiKeyAuth
//	@Router			/v1/graph/nodegraph [get]
func (s *Server) handleNodeGraph(c *gin.Context) {
	req, errBody := s.parseGraphRequest(c)
	if errBody != nil {
		return
	}
	window := req.window.EndActual.Sub(req.window.StartActual)
	g, err := s.orch.Resolve(c.Request.Context(), window, req.window.EndActual)
	if err != nil {
		mapBuildError(c, err)
		return
	}

	ptStart := time.Now()
	view := graph.Project(g, req.scope)
	s.metrics.ProjectDuration.Observe(time.Since(ptStart).Seconds())
	body := serialiseGrafanaNodeGraph(view)
	s.writeJSON(c, body, "nodegraph")
}

// clustersBody is the response shape of GET /v1/clusters.
type clustersBody struct {
	APIVersion string        `json:"apiVersion"`
	Clusters   []ClusterInfo `json:"clusters"`
}

// edgeTypesBody is the response shape of GET /v1/edge-types.
//
//nolint:unused // referenced via swag @Success annotation
type edgeTypesBody struct {
	APIVersion string                     `json:"apiVersion"`
	EdgeTypes  []graph.EdgeTypeDefinition `json:"edge_types"`
}

// debugLastQueriesBody is the response shape of GET /debug/last-queries.
//
//nolint:unused // referenced via swag @Success annotation
type debugLastQueriesBody struct {
	APIVersion string   `json:"apiVersion"`
	Queries    []string `json:"queries"`
	Note       string   `json:"note"`
}

// ----- /v1/clusters ---------------------------------------------------------

// ClusterInfo is one entry in /v1/clusters.
type ClusterInfo struct {
	Name string `json:"name"`
}

// handleClusters returns the list of clusters with data in centralised
// VictoriaMetrics over the discovery lookback.
//
//	@Summary		List clusters
//	@Description	Returns the set of clusters observed in `kube_node_info` over the configured discovery lookback (default 1 h). Intersected with `--clusters-allowlist` when set. Each request hits VictoriaMetrics directly. The response carries a content-addressed `ETag` so callers may revalidate via `If-None-Match`.
//	@Description
//	@Description	<details><summary><b>Sample response</b></summary>
//	@Description
//	@Description	```json
//	@Description	{
//	@Description	  "apiVersion": "v1",
//	@Description	  "clusters": [
//	@Description	    { "name": "prod-eu" },
//	@Description	    { "name": "prod-us" },
//	@Description	    { "name": "stage-eu" }
//	@Description	  ]
//	@Description	}
//	@Description	```
//	@Description
//	@Description	</details>
//	@Tags			discovery
//	@Produce		json
//	@Param			X-API-Key	header		string		false	"API key. Required when the server is started with API keys configured."
//	@Success		200	{object}	clustersBody
//	@Failure		401	{object}	errorBody	"Missing or invalid `X-API-Key` (only when API key auth is configured)"
//	@Failure		502	{object}	errorBody	"Upstream VictoriaMetrics unavailable"
//	@Security		ApiKeyAuth
//	@Router			/v1/clusters [get]
func (s *Server) handleClusters(c *gin.Context) {
	clusters, err := s.discoverClusters(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	body := map[string]any{
		"apiVersion": APIVersion,
		"clusters":   clusters,
	}
	raw, _ := json.Marshal(body)
	etag := sha256ETag(raw)
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

func (s *Server) discoverClusters(ctx context.Context) ([]ClusterInfo, error) {
	allowlist := promql.AllowlistRegex(s.cfg.ClustersAllowlist)
	q := promql.Render(promql.QClusterDiscovery, s.cfg.ClusterDiscoveryLookback, allowlist)
	vec, err := s.prom.Instant(ctx, string(promql.QClusterDiscovery), q, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, sample := range vec {
		c := string(sample.Metric["cluster"])
		if c == "" {
			c = "unknown"
		}
		seen[c] = struct{}{}
	}
	allowSet := stringSliceToSet(s.cfg.ClustersAllowlist)
	out := make([]ClusterInfo, 0, len(seen))
	for c := range seen {
		if len(allowSet) > 0 {
			if _, ok := allowSet[c]; !ok {
				continue
			}
		}
		out = append(out, ClusterInfo{Name: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func stringSliceToSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}

// ----- /v1/edge-types -------------------------------------------------------

// handleEdgeTypes returns the static catalogue of edge types this server
// can produce.
//
//	@Summary		Edge-type catalogue
//	@Description	Static catalogue of edge types this server can produce — directionality, valid source/target node types, supported labels, and whether the edge may cross cluster boundaries. No upstream calls; served with `Cache-Control: public, max-age=3600` and a stable `ETag`. Use this to validate the `edge_type` filter on `/v1/graph` and to drive UI legends.
//	@Description
//	@Description	<details><summary><b>Edge type matrix</b></summary>
//	@Description
//	@Description	| type | source → target | directed | cross-cluster |
//	@Description	|---|---|---|---|
//	@Description	| `pod-runs-on-node` | pod → node | yes | no |
//	@Description	| `pod-mounts-pvc` | pod → pvc | yes | no |
//	@Description	| `pod-calls-pod` | pod → pod \| external | yes | yes |
//	@Description
//	@Description	</details>
//	@Tags			discovery
//	@Produce		json
//	@Param			X-API-Key	header		string		false	"API key. Required when the server is started with API keys configured."
//	@Success		200	{object}	edgeTypesBody
//	@Failure		401	{object}	errorBody	"Missing or invalid `X-API-Key` (only when API key auth is configured)"
//	@Security		ApiKeyAuth
//	@Router			/v1/edge-types [get]
func (s *Server) handleEdgeTypes(c *gin.Context) {
	body := map[string]any{
		"apiVersion": APIVersion,
		"edge_types": graph.EdgeTypes,
	}
	raw, _ := json.Marshal(body)
	etag := graph.EdgeTypesETag()
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// ----- /livez, /readyz ------------------------------------------------------

// handleLivez is the liveness probe.
//
//	@Summary	Liveness
//	@Tags		health
//	@Produce	plain
//	@Success	200	{string}	string	"ok"
//	@Router		/livez [get]
func (s *Server) handleLivez(c *gin.Context) {
	c.String(http.StatusOK, "ok")
}

// handleReadyz is the readiness probe — issues a 1 s upstream `up{}` query.
//
//	@Summary	Readiness
//	@Description	Returns 200 only when a 1 s `up{}` probe against the configured upstream succeeds.
//	@Tags		health
//	@Produce	plain
//	@Success	200	{string}	string	"ok"
//	@Failure	503	{object}	errorBody
//	@Router		/readyz [get]
func (s *Server) handleReadyz(c *gin.Context) {
	probeCtx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	_, err := s.prom.Instant(probeCtx, string(promql.QUpProbe), promql.Render(promql.QUpProbe, 0, ""), time.Now().UTC())
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "upstream_unreachable", err.Error())
		return
	}
	c.String(http.StatusOK, "ok")
}

// ----- /debug/last-queries --------------------------------------------------

// handleDebugLastQueries returns the raw upstream query strings of the most
// recent build (only available with --enable-debug).
//
// Currently unimplemented: returns 501 so clients can distinguish "feature not
// built" from "no recent queries". A future iteration will wire a ring buffer
// in promql.Client and return the captured set here.
//
//	@Summary	Debug: last upstream queries
//	@Tags		debug
//	@Produce	json
//	@Param		X-API-Key	header		string		false	"API key. Required when the server is started with API keys configured."
//	@Success	501	{object}	errorBody	"Not implemented in v1"
//	@Failure	401	{object}	errorBody	"Missing or invalid `X-API-Key` (only when API key auth is configured)"
//	@Security	ApiKeyAuth
//	@Router		/debug/last-queries [get]
func (s *Server) handleDebugLastQueries(c *gin.Context) {
	writeError(c, http.StatusNotImplemented, "not_implemented",
		"/debug/last-queries is registered but not yet implemented; tracked for a future iteration")
}

// ----- request parsing ------------------------------------------------------

type graphRequest struct {
	start  time.Time
	end    time.Time
	window timewindow.Window
	scope  graph.Scope
	format string
}

func (s *Server) parseGraphRequest(c *gin.Context) (graphRequest, error) {
	q := c.Request.URL.Query()
	startStr := q.Get("start")
	endStr := q.Get("end")
	if startStr == "" {
		writeError(c, http.StatusBadRequest, "missing_start", "start query parameter is required")
		return graphRequest{}, fmt.Errorf("missing_start")
	}
	if endStr == "" {
		writeError(c, http.StatusBadRequest, "missing_end", "end query parameter is required")
		return graphRequest{}, fmt.Errorf("missing_end")
	}
	start, err := parseTimestamp(startStr)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_start", err.Error())
		return graphRequest{}, err
	}
	end, err := parseTimestamp(endStr)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_end", err.Error())
		return graphRequest{}, err
	}
	if !end.After(start) {
		writeError(c, http.StatusBadRequest, "invalid_range", "end must be after start")
		return graphRequest{}, fmt.Errorf("invalid_range")
	}
	if end.Sub(start) > s.cfg.MaxWindow {
		writeError(c, http.StatusBadRequest, "window_too_large", "window exceeds --max-window")
		return graphRequest{}, fmt.Errorf("window_too_large")
	}
	now := time.Now().UTC()
	if end.After(now.Add(s.cfg.MaxSkew)) {
		writeError(c, http.StatusBadRequest, "end_in_future", "end is too far in the future")
		return graphRequest{}, fmt.Errorf("end_in_future")
	}

	window := timewindow.Align(start, end, now)

	depth := 0
	if s := q.Get("depth"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_depth", "depth must be an integer")
			return graphRequest{}, err
		}
		depth = v
	}
	if depth > graph.MaxTraversalDepth {
		writeError(c, http.StatusBadRequest, "depth_too_large", "depth exceeds maximum")
		return graphRequest{}, fmt.Errorf("depth_too_large")
	}
	scope, err := graph.NewScope(
		q["cluster"],
		q["namespace"],
		q["edge_type"],
		q["pod"],
		q.Get("root"),
		depth,
		q.Get("direction"),
	)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_scope", err.Error())
		return graphRequest{}, err
	}

	return graphRequest{
		start:  start,
		end:    end,
		window: window,
		scope:  scope,
		format: "cytoscape",
	}, nil
}

func parseTimestamp(s string) (time.Time, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("timestamp must be RFC 3339 or Unix seconds: %q", s)
}

// ----- response helpers -----------------------------------------------------

func (s *Server) writeJSON(c *gin.Context, body any, format string) {
	start := time.Now()
	raw, err := json.Marshal(body)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "encode", err.Error())
		return
	}
	s.metrics.SerialiseDuration.WithLabelValues(format).Observe(time.Since(start).Seconds())
	etag := sha256ETag(raw)
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

func sha256ETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
