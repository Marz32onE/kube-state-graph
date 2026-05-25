package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/marz32one/kube-state-graph/internal/auth"
	"github.com/marz32one/kube-state-graph/internal/build"
	"github.com/marz32one/kube-state-graph/internal/clock"
	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/observability"
	"github.com/marz32one/kube-state-graph/internal/promql"
	"github.com/marz32one/kube-state-graph/internal/telemetry"

	"log/slog"
)

// APIVersion is the version stamped into every JSON response and route prefix.
const APIVersion = "v1"

// Server bundles the dependencies needed by the HTTP handlers.
type Server struct {
	cfg     config.Config
	builder *build.Builder
	prom    promql.Querier
	r       promql.Renderer
	metrics *observability.Metrics
	logger  *slog.Logger
	keys    auth.Validator
	clk     clock.Clock
}

// New wires up a Server. keys may be nil to run with API-key authentication
// disabled. clk may be nil; nil falls back to clock.System. The Renderer is
// derived from cfg.MetricPrefix so the cluster-discovery + readiness
// (`up{}`) queries the Server issues on its own (independent of the build
// pipeline) honour the configured upstream metric-name prefix
// (see design.md D26).
func New(cfg config.Config, builder *build.Builder, prom promql.Querier, m *observability.Metrics, logger *slog.Logger, keys auth.Validator, clk clock.Clock) *Server {
	if clk == nil {
		clk = clock.System{}
	}
	if keys == nil {
		keys = auth.NewKeySet()
	}
	return &Server{
		cfg:     cfg,
		builder: builder,
		prom:    prom,
		r:       promql.Renderer{Prefix: cfg.MetricPrefix},
		metrics: m,
		logger:  logger,
		keys:    keys,
		clk:     clk,
	}
}

// Handler returns the Gin engine fully configured with routes + middleware.
//
// otelgin is installed on the /v1/* route group only so kubelet probes,
// Prometheus scrapes, and documentation requests do not generate spans.
// Inbound W3C traceparent is honoured via the global propagator.
func (s *Server) Handler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(s.requestIDMiddleware(), s.loggingMiddleware())

	v1 := r.Group("/" + APIVersion)
	v1.Use(
		otelgin.Middleware(telemetry.ServiceName),
		s.apiKeyMiddleware(),
		s.spanEnrichMiddleware(),
	)
	v1.GET("/graph", s.handleGraph)
	v1.GET("/graph/nodegraph", s.handleNodeGraph)
	v1.GET("/clusters", s.handleClusters)
	v1.GET("/edge-types", s.handleEdgeTypes)

	r.GET("/livez", s.handleLivez)
	r.GET("/readyz", s.handleReadyz)
	r.GET("/metrics", gin.WrapH(promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{})))

	r.GET("/openapi.yaml", s.handleOpenAPIYAML)
	r.GET("/openapi.json", s.handleOpenAPIJSON)
	r.GET("/docs", s.handleDocs)
	r.GET("/docs/assets/*path", s.handleDocsAsset)

	return r
}

// requestIDMiddleware injects a unique X-Request-ID into context and response.
func (s *Server) requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// quietLogPaths are silent on success: kubelet probes and Prometheus scrape
// fire every few seconds and would otherwise dominate the access log. Any
// status >=400 is still emitted so genuine failures remain visible.
var quietLogPaths = map[string]struct{}{
	"/livez":   {},
	"/readyz":  {},
	"/metrics": {},
}

// loggingMiddleware emits one slog line per request.
func (s *Server) loggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start)
		status := c.Writer.Status()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		if _, quiet := quietLogPaths[path]; quiet && status < 400 {
			s.metrics.HTTPRequests.WithLabelValues(path, statusClass(status)).Inc()
			return
		}
		clusters := c.Request.URL.Query()["cluster"]

		s.logger.InfoContext(c.Request.Context(), "http",
			"method", c.Request.Method,
			"path", path,
			"status", status,
			"duration_ms", duration.Milliseconds(),
			"request_id", c.GetString("request_id"),
			"clusters", clusters,
		)
		s.metrics.HTTPRequests.WithLabelValues(path, statusClass(status)).Inc()
	}
}

func statusClass(s int) string {
	switch {
	case s >= 200 && s < 300:
		return "2xx"
	case s >= 300 && s < 400:
		return "3xx"
	case s >= 400 && s < 500:
		return "4xx"
	case s >= 500 && s < 600:
		return "5xx"
	default:
		return "other"
	}
}
