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

func mapBuildError(c *gin.Context, err error) {
	reason := build.AsReason(err)
	switch reason {
	case build.ReasonCapacity:
		c.Header("Retry-After", "1")
		writeError(c, http.StatusServiceUnavailable, "capacity", err.Error())
	case build.ReasonTimeout:
		c.Header("Retry-After", "1")
		writeError(c, http.StatusServiceUnavailable, "timeout", err.Error())
	case build.ReasonClusterTooLarge:
		writeError(c, http.StatusServiceUnavailable, "cluster_too_large", err.Error())
	case build.ReasonOutsideRetention:
		writeError(c, http.StatusBadRequest, "outside_retention", err.Error())
	case build.ReasonUpstream:
		writeError(c, http.StatusBadGateway, "upstream", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "internal", err.Error())
	}
}
