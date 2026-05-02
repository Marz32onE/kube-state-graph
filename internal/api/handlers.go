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
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/common/model"

	"github.com/marz32one/kube-state-graph/internal/cache"
	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

// ----- /v1/graph (Cytoscape.js) ---------------------------------------------

func (s *Server) handleGraph(c *gin.Context) {
	req, errBody := s.parseGraphRequest(c)
	if errBody != nil {
		return // parseGraphRequest already wrote response
	}
	res, err := s.orch.Resolve(c.Request.Context(), req.cacheKey, req.bucket)
	if err != nil {
		mapBuildError(c, err)
		return
	}
	c.Set("cache_status", res.CacheStatus)

	view := graph.Project(res.Graph, req.scope)
	body := serialiseCytoscape(req, res.Graph, view)
	s.writeJSONWithCaching(c, body, req.bucket, res.CacheStatus, "cytoscape")
}

// ----- /v1/graph/nodegraph (Grafana) ----------------------------------------

func (s *Server) handleNodeGraph(c *gin.Context) {
	req, errBody := s.parseGraphRequest(c)
	if errBody != nil {
		return
	}
	res, err := s.orch.Resolve(c.Request.Context(), req.cacheKey, req.bucket)
	if err != nil {
		mapBuildError(c, err)
		return
	}
	c.Set("cache_status", res.CacheStatus)

	view := graph.Project(res.Graph, req.scope)
	body := serialiseGrafanaNodeGraph(view)
	s.writeJSONWithCaching(c, body, req.bucket, res.CacheStatus, "nodegraph")
}

// ----- /v1/clusters ---------------------------------------------------------

type discoveryCache struct {
	mu       sync.Mutex
	value    []ClusterInfo
	expires  time.Time
	ttl      time.Duration
}

// ClusterInfo is one entry in /v1/clusters.
type ClusterInfo struct {
	Name string `json:"name"`
}

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
	c.Header("Cache-Control", "public, max-age=60")
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

func (s *Server) discoverClusters(ctx context.Context) ([]ClusterInfo, error) {
	s.discoveryCache.mu.Lock()
	defer s.discoveryCache.mu.Unlock()
	if !s.discoveryCache.expires.IsZero() && time.Now().Before(s.discoveryCache.expires) {
		return s.discoveryCache.value, nil
	}

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

	s.discoveryCache.value = out
	s.discoveryCache.expires = time.Now().Add(s.discoveryCache.ttl)
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

func (s *Server) handleLivez(c *gin.Context) {
	c.String(http.StatusOK, "ok")
}

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

// ----- /admin/cache, /debug/last-queries ------------------------------------

func (s *Server) handleAdminCacheFlush(c *gin.Context) {
	s.cache.Clear()
	c.Status(http.StatusNoContent)
}

func (s *Server) handleDebugLastQueries(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]any{
		"apiVersion": APIVersion,
		"queries":    []string{},
		"note":       "v1 debug endpoint stub; populated by build pipeline in a later iteration",
	})
}

// ----- request parsing ------------------------------------------------------

type graphRequest struct {
	start    time.Time
	end      time.Time
	bucket   cache.Bucketing
	cacheKey uint64
	scope    graph.Scope
	format   string
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

	bucket := cache.Bucket(start, end, now)
	cacheKey := cache.Key(bucket)

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
		q["node"],
		q["edge_type"],
		q.Get("root"),
		depth,
		q.Get("direction"),
	)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_scope", err.Error())
		return graphRequest{}, err
	}

	return graphRequest{
		start:    start,
		end:      end,
		bucket:   bucket,
		cacheKey: cacheKey,
		scope:    scope,
		format:   "cytoscape",
	}, nil
}

func parseTimestamp(s string) (time.Time, error) {
	// Unix seconds first.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("timestamp must be RFC 3339 or Unix seconds: %q", s)
}

// ----- response helpers -----------------------------------------------------

func (s *Server) writeJSONWithCaching(c *gin.Context, body any, bucket cache.Bucketing, cacheStatus, format string) {
	start := time.Now()
	raw, err := json.Marshal(body)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "encode", err.Error())
		return
	}
	s.metrics.SerialiseDuration.WithLabelValues(format).Observe(time.Since(start).Seconds())
	etag := sha256ETag(raw)
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d", bucket.MaxAge))
	c.Header("ETag", etag)
	c.Header("X-Cache", strings.ToUpper(cacheStatus))
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

// Suppress unused import warnings on platforms where the model package is
// referenced only transitively via promql.
var _ model.Vector
