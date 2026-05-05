// Command openapi-postprocess rewrites swag-generated OpenAPI 3.x specs so
// parameter-level `example` values are also embedded in `schema.example`.
//
// Scalar API Reference (and several other renderers) display only
// `schema.example` for query/path parameters. swag emits `example` at the
// parameter level, which leaves the inline parameter docs blank. This tool
// copies each parameter's `example` into its `schema.example` (without
// removing the original, since both forms are valid per OpenAPI 3.x).
//
// Usage:
//
//	openapi-postprocess <openapi.json> <openapi.yaml>
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: openapi-postprocess <openapi.json> <openapi.yaml>")
		os.Exit(2)
	}
	if err := processJSON(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "json: %v\n", err)
		os.Exit(1)
	}
	if err := processYAML(os.Args[2]); err != nil {
		fmt.Fprintf(os.Stderr, "yaml: %v\n", err)
		os.Exit(1)
	}
}

func processJSON(path string) error {
	// gosec G304: path is a build-tool CLI argument, not user input.
	raw, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	mutateParameters(doc)
	out, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	// gosec G306: openapi spec is a public, world-readable artefact.
	return os.WriteFile(path, out, 0o644) //nolint:gosec
}

func processYAML(path string) error {
	// gosec G304: path is a build-tool CLI argument, not user input.
	raw, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	mutateParameters(doc)
	var buf []byte
	{
		out, err := yaml.Marshal(doc)
		if err != nil {
			return err
		}
		buf = out
	}
	// gosec G306: openapi spec is a public, world-readable artefact.
	return os.WriteFile(path, buf, 0o644) //nolint:gosec
}

// multiValueExamples seeds schema.example for repeatable filter params with
// a 2-element array, hinting OR-combination semantics in the rendered docs
// (e.g. ?namespace=payments&namespace=checkout). Keyed by parameter name —
// only applied when the parameter is array-typed.
var multiValueExamples = map[string][]any{
	"cluster":   {"prod-eu", "prod-us"},
	"namespace": {"payments", "checkout"},
	"edge_type": {"pod-calls-pod", "pod-runs-on-node"},
	"pod":       {"checkout-7d9f6c8b8-abcde", "payments-5c8b6f9d4-fghij"},
}

// mutateParameters walks paths.<path>.<verb>.parameters[] and moves each
// parameter-level `example` value into the parameter's `schema.example`,
// removing the original to avoid duplicate rendering in Scalar (which
// displays both fields side by side). For repeatable array-typed filter
// params, swap the single example for a 2-element array so the renderer
// hints OR semantics.
func mutateParameters(doc map[string]any) {
	paths, _ := doc["paths"].(map[string]any)
	for _, item := range paths {
		ops, _ := item.(map[string]any)
		for verb, op := range ops {
			if !isHTTPVerb(verb) {
				continue
			}
			opMap, _ := op.(map[string]any)
			if opMap == nil {
				continue
			}
			params, _ := opMap["parameters"].([]any)
			for _, p := range params {
				pm, _ := p.(map[string]any)
				if pm == nil {
					continue
				}
				ex, hasEx := pm["example"]
				if !hasEx {
					continue
				}
				schema, ok := pm["schema"].(map[string]any)
				if !ok {
					schema = map[string]any{}
					pm["schema"] = schema
				}
				name, _ := pm["name"].(string)
				isArray := false
				if t, _ := schema["type"].(string); t == "array" {
					isArray = true
				}
				if isArray {
					if multi, ok := multiValueExamples[name]; ok {
						schema["example"] = multi
					} else if _, exists := schema["example"]; !exists {
						schema["example"] = []any{ex}
					}
					if items, ok := schema["items"].(map[string]any); ok {
						if _, exists := items["example"]; !exists {
							items["example"] = ex
						}
					}
				} else if _, exists := schema["example"]; !exists {
					schema["example"] = ex
				}
				delete(pm, "example")
			}
		}
	}
}

func isHTTPVerb(s string) bool {
	switch s {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	}
	return false
}
