// Package margotest validates bodies against the pinned Margo OpenAPI file (SPEC §17.1) in tests.
// The operation → schema mapping comes from the file itself, so a test names the operation, not
// the schema.
package margotest

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/balaji-balu/ieo/api/margo"
)

const resource = "https://margo.invalid/workload-management-api.json"

// API is the pinned Margo OpenAPI file, ready to compile the schemas it names.
type API struct {
	doc      map[string]any
	compiler *jsonschema.Compiler
}

// Load parses the pinned file, with its known defects worked around by
// margo.WorkloadManagementAPIDocument.
func Load(t testing.TB) *API {
	t.Helper()
	doc, err := margo.WorkloadManagementAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	// Only components are a schema resource; paths hold OpenAPI objects (e.g. `required: true`)
	// that are not JSON Schema.
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(resource, map[string]any{"components": doc["components"]}); err != nil {
		t.Fatal(err)
	}
	return &API{doc: doc, compiler: c}
}

// Request returns the schema of an operation's request body.
func (a *API) Request(t testing.TB, method, path, mediaType string) *jsonschema.Schema {
	t.Helper()
	return a.refAt(t, "paths", path, method, "requestBody", "content", mediaType, "schema")
}

// Response returns the schema of an operation's response body.
func (a *API) Response(t testing.TB, method, path, status, mediaType string) *jsonschema.Schema {
	t.Helper()
	return a.refAt(t, "paths", path, method, "responses", status, "content", mediaType, "schema")
}

// Schema returns a schema of components/schemas by name.
func (a *API) Schema(t testing.TB, name string) *jsonschema.Schema {
	t.Helper()
	return a.compile(t, "#/components/schemas/"+name)
}

// HasResponse reports whether the file lists status as a response of the operation.
func (a *API) HasResponse(method, path, status string) bool {
	return lookup(a.doc, "paths", path, method, "responses", status) != nil
}

// refAt follows keys from the document root to a schema object and compiles the schema it
// references.
func (a *API) refAt(t testing.TB, keys ...string) *jsonschema.Schema {
	t.Helper()
	s, ok := lookup(a.doc, keys...).(map[string]any)
	if !ok {
		t.Fatalf("Margo OpenAPI file has no schema at %v", keys)
	}
	ref, ok := s["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/components/") {
		t.Fatalf("schema at %v is not a component $ref: %v", keys, s)
	}
	return a.compile(t, ref)
}

func (a *API) compile(t testing.TB, fragment string) *jsonschema.Schema {
	t.Helper()
	sch, err := a.compiler.Compile(resource + fragment)
	if err != nil {
		t.Fatalf("compile %s: %v", fragment, err)
	}
	return sch
}

func lookup(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// Validate parses b as JSON and validates it against s.
func Validate(s *jsonschema.Schema, b []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	return s.Validate(v)
}
