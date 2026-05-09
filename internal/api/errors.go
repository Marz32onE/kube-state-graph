package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

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
// status (RFC 9110 §15.6.3 Bad Gateway, §15.6.5 Gateway Timeout).
func mapBuildError(c *gin.Context, err error) {
	reason := build.AsReason(err)
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
