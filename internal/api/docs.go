package api

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed static/scalar/index.html static/scalar/scalar.js static/scalar/scalar.css static/scalar/VERSION
var scalarFS embed.FS

//go:embed static/openapi/openapi.yaml static/openapi/openapi.json
var openAPIFS embed.FS

// loadEmbedded reads the named file out of the supplied embed.FS, panicking
// (at boot) on absence — embedded contents are checked at compile time.
func loadEmbedded(efs embed.FS, name string) []byte {
	b, err := efs.ReadFile(name)
	if err != nil {
		panic("missing embedded asset: " + name + ": " + err.Error())
	}
	return b
}

func sha256Quoted(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// handleOpenAPIYAML serves the embedded OpenAPI 3.0 spec in YAML form.
//
//	@Summary	OpenAPI spec (YAML)
//	@Tags		docs
//	@Produce	application/yaml
//	@Success	200	{string}	string	"OpenAPI 3.0 YAML"
//	@Router		/openapi.yaml [get]
func (s *Server) handleOpenAPIYAML(c *gin.Context) {
	body := loadEmbedded(openAPIFS, "static/openapi/openapi.yaml")
	etag := sha256Quoted(body)
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", body)
}

// handleOpenAPIJSON serves the embedded OpenAPI 3.0 spec in JSON form.
//
//	@Summary	OpenAPI spec (JSON)
//	@Tags		docs
//	@Produce	application/json
//	@Success	200	{string}	string	"OpenAPI 3.0 JSON"
//	@Router		/openapi.json [get]
func (s *Server) handleOpenAPIJSON(c *gin.Context) {
	body := loadEmbedded(openAPIFS, "static/openapi/openapi.json")
	etag := sha256Quoted(body)
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

// handleDocs renders the Scalar API Reference UI. All assets vendored — no
// external CDN reference appears in the served HTML.
//
//	@Summary	API reference UI (Scalar)
//	@Tags		docs
//	@Produce	text/html
//	@Success	200	{string}	string	"HTML"
//	@Router		/docs [get]
func (s *Server) handleDocs(c *gin.Context) {
	body := loadEmbedded(scalarFS, "static/scalar/index.html")
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "text/html; charset=utf-8", body)
}

// handleDocsAsset serves a vendored Scalar asset.
//
//	@Summary	API reference asset (vendored)
//	@Tags		docs
//	@Param		path	path	string	true	"Asset path"
//	@Success	200		{string}	string	"static asset"
//	@Failure	404		{object}	errorBody
//	@Router		/docs/assets/{path} [get]
func (s *Server) handleDocsAsset(c *gin.Context) {
	requested := strings.TrimPrefix(c.Param("path"), "/")
	requested = path.Clean(requested)
	if strings.Contains(requested, "..") || requested == "." || requested == "" {
		c.Status(http.StatusNotFound)
		return
	}

	full := "static/scalar/" + requested
	body, err := fs.ReadFile(scalarFS, full)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	ct := mimeFor(requested)
	c.Header("Cache-Control", "public, max-age=86400, immutable")
	c.Data(http.StatusOK, ct, body)
}

func mimeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

