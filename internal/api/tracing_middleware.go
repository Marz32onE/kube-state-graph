package api

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// spanEnrichMiddleware decorates the otelgin server span with response-side
// attributes that the upstream middleware cannot know (build.Reason).
// The reason string is propagated via the gin context "build_reason" key by
// mapBuildError; absence implies success or an unmapped error.
func (s *Server) spanEnrichMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		span := trace.SpanFromContext(c.Request.Context())
		if !span.IsRecording() {
			return
		}
		status := c.Writer.Status()
		if status >= 500 {
			reason := c.GetString("build_reason")
			if reason == "" {
				reason = "internal"
			}
			span.SetStatus(codes.Error, reason)
		}
	}
}
