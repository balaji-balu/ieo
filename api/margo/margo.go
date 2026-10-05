// Package margo embeds the pinned Margo specification files under f209a7f/ (SPEC header, ADR
// 0010), so production code validates against the same bytes the tests do. The files themselves
// are never edited; see f209a7f/README.md.
package margo

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

// ApplicationDescriptionSchema is the JSON Schema generated from Margo's LinkML Application
// Description schema (SPEC §5.3), unchanged. Its known defects are listed in f209a7f/README.md;
// internal/contract works around them in memory.
//
//go:embed f209a7f/application-description.schema.json
var ApplicationDescriptionSchema []byte

// WorkloadManagementAPI is Margo's Workload Management API OpenAPI file (SPEC §11.1), unchanged.
// Its known defects are listed in f209a7f/README.md; WorkloadManagementAPIDocument works around
// them.
//
//go:embed f209a7f/workload-management-api-1.0.0-rc.3.yaml
var WorkloadManagementAPI []byte

// WorkloadManagementAPIDocument returns WorkloadManagementAPI parsed as JSON values, with the
// known defects of f209a7f/README.md worked around in memory. It is the one place that knows
// those defects, for production validation and tests alike (G-A2). Numbers are json.Number, as
// JSON Schema validators expect. Each call returns a new value the caller may change.
func WorkloadManagementAPIDocument() (map[string]any, error) {
	js, err := yaml.YAMLToJSON(WorkloadManagementAPI)
	if err != nil {
		return nil, fmt.Errorf("margo OpenAPI file: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("margo OpenAPI file: %w", err)
	}
	components, _ := doc["components"].(map[string]any)
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("margo OpenAPI file: no components.schemas")
	}
	if err := moveDottedRequired(schemas); err != nil {
		return nil, err
	}
	return doc, nil
}

// moveDottedRequired fixes `required` entries of the form `<property>.<field>`:
// UnsignedAppStateManifest requires "bundle.mediaType", "bundle.digest" and "bundle.url", which
// JSON Schema reads as literal property names, so every valid manifest would fail. Each entry
// becomes `<field>` in the required list of the schema that `<property>` references (here
// DeploymentBundleRef), which is what the names mean. `required` does not apply to null, so a
// null bundle stays valid (f209a7f/README.md).
func moveDottedRequired(schemas map[string]any) error {
	for name, s := range schemas {
		obj, ok := s.(map[string]any)
		if !ok {
			continue
		}
		req, ok := obj["required"].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		for _, r := range req {
			entry, _ := r.(string)
			prop, field, dotted := strings.Cut(entry, ".")
			if !dotted {
				kept = append(kept, r)
				continue
			}
			target, err := referencedSchema(schemas, obj, prop)
			if err != nil {
				return fmt.Errorf("margo OpenAPI file: %s requires %q: %w", name, entry, err)
			}
			targetReq, _ := target["required"].([]any)
			if !slices.Contains(targetReq, any(field)) {
				target["required"] = append(targetReq, field)
			}
		}
		obj["required"] = kept
	}
	return nil
}

// referencedSchema returns the component schema that property prop of obj references by $ref.
func referencedSchema(schemas, obj map[string]any, prop string) (map[string]any, error) {
	props, _ := obj["properties"].(map[string]any)
	p, _ := props[prop].(map[string]any)
	ref, _ := p["$ref"].(string)
	target, ok := schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
	if !strings.HasPrefix(ref, "#/components/schemas/") || !ok {
		return nil, fmt.Errorf("property %q does not reference a component schema", prop)
	}
	return target, nil
}
