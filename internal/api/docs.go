package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	ksgdocs "github.com/marz32one/kube-state-graph/docs"
)

// scalarHTML is the Scalar API Reference UI, loaded from the official CDN.
// This is Scalar's documented minimal integration: load the standalone bundle,
// then call Scalar.createApiReference pointed at our same-origin OpenAPI spec
// (so no proxyUrl is needed). See https://github.com/scalar/scalar.
const scalarHTML = `<!doctype html>
<html lang="en">
    <head>
        <meta charset="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <title>kube-state-graph API reference</title>
    </head>
    <body>
        <div id="app"></div>
        <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
        <script>
            Scalar.createApiReference('#app', { url: '/openapi.json' })
        </script>
    </body>
</html>
`

// handleOpenAPIYAML serves the embedded OpenAPI 3.1 spec in YAML form.
//
//	@Summary	OpenAPI spec (YAML)
//	@Tags		docs
//	@Produce	application/yaml
//	@Success	200	{string}	string	"OpenAPI 3.1 YAML"
//	@Router		/openapi.yaml [get]
func (s *Server) handleOpenAPIYAML(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", ksgdocs.OpenAPIYAML)
}

// handleOpenAPIJSON serves the embedded OpenAPI 3.1 spec in JSON form.
//
//	@Summary	OpenAPI spec (JSON)
//	@Tags		docs
//	@Produce	application/json
//	@Success	200	{string}	string	"OpenAPI 3.1 JSON"
//	@Router		/openapi.json [get]
func (s *Server) handleOpenAPIJSON(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "application/json; charset=utf-8", ksgdocs.OpenAPIJSON)
}

// handleDocs renders the Scalar API Reference UI.
//
//	@Summary	API reference UI (Scalar)
//	@Tags		docs
//	@Produce	text/html
//	@Success	200	{string}	string	"HTML"
//	@Router		/docs [get]
func (s *Server) handleDocs(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(scalarHTML))
}
