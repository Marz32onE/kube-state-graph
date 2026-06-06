// Package docs holds the generated OpenAPI specification, embedded into the
// binary so the API server can serve it with no external files at runtime.
//
// Regenerate with `make docs` (runs `swag init`). The swagger.json and
// swagger.yaml files are committed; CI's docs-drift job verifies freshness.
package docs

import _ "embed"

// OpenAPIJSON is the generated OpenAPI 3.1 spec in JSON form.
//
//go:embed swagger.json
var OpenAPIJSON []byte

// OpenAPIYAML is the generated OpenAPI 3.1 spec in YAML form.
//
//go:embed swagger.yaml
var OpenAPIYAML []byte
