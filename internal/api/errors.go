package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/marz32one/kube-state-graph/internal/build"
)

type errorBody struct {
	APIVersion string     `json:"apiVersion"`
	Error      errorField `json:"error"`
}

type errorField struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func writeError(c *gin.Context, status int, reason, message string) {
	c.JSON(status, errorBody{
		APIVersion: APIVersion,
		Error: errorField{
			Reason:  reason,
			Message: message,
		},
	})
}

// mapBuildError translates a typed build error into a REST-conventional HTTP
// status (RFC 9110 §15.6.3 Bad Gateway, §15.6.5 Gateway Timeout). The reason
// string is also stashed on the gin context so spanEnrichMiddleware can stamp
// it onto the otelgin server span and recorded onto the active span (if any).
func mapBuildError(c *gin.Context, err error) {
	reason := build.AsReason(err)
	c.Set("build_reason", string(reason))
	span := trace.SpanFromContext(c.Request.Context())
	if span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, string(reason))
	}
	switch reason {
	case build.ReasonTimeout:
		writeError(c, http.StatusGatewayTimeout, "timeout", err.Error())
	case build.ReasonOutsideRetention:
		writeError(c, http.StatusBadRequest, "outside_retention", err.Error())
	case build.ReasonUpstream:
		writeError(c, http.StatusBadGateway, "upstream", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "internal", err.Error())
	}
}
