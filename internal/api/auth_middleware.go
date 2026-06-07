package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// APIKeyHeader is the HTTP header callers must use to present their API key.
const APIKeyHeader = "X-API-Key" //nolint:gosec // G101 false positive — header name, not a credential

// apiKeyMiddleware enforces X-API-Key on protected routes when at least one
// key is loaded. With no keys configured, the middleware is a no-op so dev
// rigs and existing tests run unchanged.
//
// The middleware is mounted on the /v1 route group only (see Server.Handler);
// the open paths — /livez, /readyz, /metrics, /openapi.*, /docs — are registered
// on the root engine outside that group, so they are exempt structurally and
// never reach this handler. No per-path allowlist is needed here.
func (s *Server) apiKeyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.keys == nil || s.keys.Empty() {
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
