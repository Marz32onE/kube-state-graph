package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// APIKeyHeader is the HTTP header callers must use to present their API key.
const APIKeyHeader = "X-API-Key" //nolint:gosec // G101 false positive — header name, not a credential

// openPaths are exempt from API-key authentication. Health probes must answer
// kubelet without credentials, /metrics is consumed by Prometheus scrapes
// (operator gates it via NetworkPolicy / separate listen address), and the
// OpenAPI / Scalar UI must load without keys so docs work in any browser.
var openPaths = map[string]struct{}{
	"/livez":             {},
	"/readyz":            {},
	"/metrics":           {},
	"/openapi.yaml":      {},
	"/openapi.json":      {},
	"/docs":              {},
	"/docs/assets/*path": {},
}

// apiKeyMiddleware enforces X-API-Key on protected routes when at least one
// key is loaded. With no keys configured, the middleware is a no-op so dev
// rigs and existing tests run unchanged.
func (s *Server) apiKeyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.keys == nil || s.keys.Empty() {
			c.Next()
			return
		}
		path := c.FullPath()
		if _, open := openPaths[path]; open {
			c.Next()
			return
		}
		presented := c.GetHeader(APIKeyHeader)
		if presented == "" {
			s.metrics.AuthRejected.WithLabelValues("missing").Inc()
			writeError(c, http.StatusUnauthorized, "unauthorized", "missing "+APIKeyHeader+" header")
			c.Abort()
			return
		}
		if !s.keys.Validate(presented) {
			s.metrics.AuthRejected.WithLabelValues("invalid").Inc()
			writeError(c, http.StatusUnauthorized, "unauthorized", "invalid API key")
			c.Abort()
			return
		}
		c.Next()
	}
}
