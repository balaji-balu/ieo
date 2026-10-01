package contract_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/balaji-balu/ieo/internal/contract"
)

// deploymentJSON is an ApplicationDeployment that is valid against the pinned Margo schema.
const deploymentJSON = `{"id":"` + testUUID2 + `",` +
	`"metadata":{"name":"hello","namespace":"default","deviceId":"site-1/host-03","annotations":{"note":"x"}},` +
	`"spec":{"applicationId":"hello-world","deploymentProfile":{"type":"compose",` +
	`"components":[{"name":"web","properties":{"repository":"oci://registry.local/hello/web",` +
	`"revision":"1.0.0_build.1","wait":true,"timeout":"5m0s"}}],` +
	`"deviceConstraints":{"capacityRequirements":{"memory":"256Mi"}}},` +
	`"parameters":{"greeting":{"value":"hi","targets":[{"pointer":"/env/GREETING","components":["web"]}]}}}}`

// SPEC §17.1: "Margo API request and response bodies validate against the vendored Margo OpenAPI
// file."
//
// Each body is a Go value from internal/contract, encoded with encoding/json and validated against
// the schema the vendored file names for that operation; it must also decode back to the same
// value. The invalid cases show the validator rejects what the file forbids.
func TestSpec_17_1_MargoBodiesValidateAgainstVendoredOpenAPI(t *testing.T) {
	api := loadMargoAPI(t)

	var deployment contract.ApplicationDeployment
	if err := json.Unmarshal([]byte(deploymentJSON), &deployment); err != nil {
		t.Fatalf("decode deploymentJSON: %v", err)
	}
	digest := contract.Digest(testDigest)
	host := mustDevice(t, "site-1/host-03")

	tests := []struct {
		name   string
		schema *jsonschema.Schema
		body   any // pointer to a Go value; decoding its JSON and encoding again must give the same bytes
	}{
		{"capabilities: gateway (identity only)",
			api.request(t, "put", "/api/v1/capabilities/{deviceId}", "application/json"),
			&contract.DeviceCapabilitiesManifest{Properties: contract.DeviceCapabilities{
				ID: mustDevice(t, "site-1"), Vendor: "ieo", ModelNumber: "lo", SerialNumber: "1",
			}}},
		{"capabilities: host",
			api.request(t, "put", "/api/v1/capabilities/{deviceId}", "application/json"),
			&contract.DeviceCapabilitiesManifest{
				Properties: contract.DeviceCapabilities{
					ID: host, Vendor: "ieo", ModelNumber: "en", SerialNumber: "2",
					CPUs:                     []contract.CPU{{Cores: 4, Architecture: "amd64"}},
					Memory:                   "8Gi",
					Storage:                  "64Gi",
					Peripherals:              []contract.Peripheral{{Type: "camera", Manufacturer: "acme"}},
					Interfaces:               []contract.CommunicationInterface{{Type: "ethernet"}},
					OTelCollector:            true,
					SupportedRuntimes:        []string{"oci"},
					SupportedDeploymentTypes: []string{"compose"},
				},
				Labels: map[string]string{"line": "2"},
			}},
		{"manifest with deployments",
			api.response(t, "get", "/api/v1/deployments", "200", "application/vnd.margo.manifest.v1+json"),
			&contract.StateManifest{
				ManifestVersion: 7,
				Deployments: []contract.DeploymentRef{{
					DeploymentID: uuid.MustParse(testUUID2), Digest: digest, SizeBytes: 812,
					URL: "/api/v1/deployments/" + testUUID2 + "/" + testDigest,
				}},
				Bundle: &contract.BundleRef{
					MediaType: contract.BundleMediaType, Digest: digest, SizeBytes: 512,
					URL: "/api/v1/bundles/" + testDigest,
				},
			}},
		{"manifest with no deployments",
			api.response(t, "get", "/api/v1/deployments", "200", "application/vnd.margo.manifest.v1+json"),
			&contract.StateManifest{ManifestVersion: 1}},
		{"deployment status",
			api.request(t, "post", "/api/v1/deployments/{deploymentId}/status", "application/json"),
			&contract.DeploymentStatus{
				DeploymentID: uuid.MustParse(testUUID2), DeviceID: host, AdoptedManifestVersion: 7,
				Status: contract.DeploymentState{State: contract.StateFailed,
					Error: &contract.StatusError{Code: "IEO-PULL-FAILED", Source: "en", Message: "m"}},
				Components: []contract.ComponentStatus{
					{Name: "web", State: contract.StateInstalled},
					{Name: "db", State: contract.StateFailed, Error: &contract.StatusError{Code: "c"}},
				},
			}},
		{"deployment status with no components",
			api.request(t, "post", "/api/v1/deployments/{deploymentId}/status", "application/json"),
			&contract.DeploymentStatus{
				DeploymentID: uuid.MustParse(testUUID2), DeviceID: host, AdoptedManifestVersion: 1,
				Status: contract.DeploymentState{State: contract.StatePending},
			}},
		{"problem",
			api.response(t, "get", "/api/v1/deployments", "403", "application/problem+json"),
			&contract.Problem{Type: "https://docs.margo.org/specification/problem-types/x", Title: "t",
				Status: 403, Detail: "d", Instance: "/api/v1/deployments"}},
		// The deployment endpoint returns raw YAML (`type: string`); the file describes its content
		// as appDeploymentManifest.
		{"application deployment",
			api.schema(t, "appDeploymentManifest"),
			&deployment},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if err := validateJSON(tt.schema, b); err != nil {
				t.Fatalf("%s does not validate: %v", b, err)
			}
			back := reflect.New(reflect.TypeOf(tt.body).Elem()).Interface()
			if err := json.Unmarshal(b, back); err != nil {
				t.Fatalf("decode %s: %v", b, err)
			}
			again, err := json.Marshal(back)
			if err != nil || !bytes.Equal(again, b) {
				t.Errorf("round trip changed the body:\n got %s (err %v)\nwant %s", again, err, b)
			}
		})
	}

	invalid := []struct {
		name   string
		schema *jsonschema.Schema
		body   string
	}{
		{"manifest without bundle",
			api.response(t, "get", "/api/v1/deployments", "200", "application/vnd.margo.manifest.v1+json"),
			`{"manifestVersion":1,"deployments":[]}`},
		{"capabilities with bad device ID",
			api.request(t, "put", "/api/v1/capabilities/{deviceId}", "application/json"),
			`{"properties":{"id":"site 1","vendor":"v","modelNumber":"m","serialNumber":"s"}}`},
		{"status with unknown state",
			api.request(t, "post", "/api/v1/deployments/{deploymentId}/status", "application/json"),
			`{"deploymentId":"d","adoptedManifestVersion":1,"status":{"state":"running"},"components":[]}`},
		{"deployment with a `+` revision",
			api.schema(t, "appDeploymentManifest"),
			strings.Replace(deploymentJSON, "1.0.0_build.1", "1.0.0+build.1", 1)},
	}
	for _, tt := range invalid {
		if err := validateJSON(tt.schema, []byte(tt.body)); err == nil {
			t.Errorf("%s: %s validated, want rejected", tt.name, tt.body)
		}
	}
}

// margoAPI is the vendored Margo OpenAPI file, ready to compile the schemas it names.
type margoAPI struct {
	doc      map[string]any
	compiler *jsonschema.Compiler
}

const margoResource = "https://margo.invalid/workload-management-api.json"

// loadMargoAPI reads the one vendored Margo OpenAPI file under api/margo/ (SPEC §17.1).
func loadMargoAPI(t *testing.T) *margoAPI {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "api", "margo", "*", "workload-management-api-*.yaml"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want exactly one vendored Margo OpenAPI file under api/margo/, got %v (err %v)", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	js, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatalf("%s: %v", files[0], err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		t.Fatalf("%s: %v", files[0], err)
	}
	doc := v.(map[string]any)
	fixUpstreamDefects(doc)

	// Only components are a schema resource; paths hold OpenAPI objects (e.g. `required: true`)
	// that are not JSON Schema.
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(margoResource, map[string]any{"components": doc["components"]}); err != nil {
		t.Fatal(err)
	}
	return &margoAPI{doc: doc, compiler: c}
}

// fixUpstreamDefects works around the defects listed in api/margo/<commit>/README.md, in memory.
// The vendored file itself stays unchanged.
func fixUpstreamDefects(doc map[string]any) {
	schemas := lookup(doc, "components", "schemas").(map[string]any)
	for _, s := range schemas {
		obj, ok := s.(map[string]any)
		if !ok {
			continue
		}
		req, ok := obj["required"].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		for _, name := range req {
			// e.g. UnsignedAppStateManifest requires "bundle.mediaType", which no object has.
			if !strings.Contains(name.(string), ".") {
				kept = append(kept, name)
			}
		}
		obj["required"] = kept
	}
}

func (a *margoAPI) request(t *testing.T, method, path, mediaType string) *jsonschema.Schema {
	t.Helper()
	return a.refAt(t, "paths", path, method, "requestBody", "content", mediaType, "schema")
}

func (a *margoAPI) response(t *testing.T, method, path, status, mediaType string) *jsonschema.Schema {
	t.Helper()
	return a.refAt(t, "paths", path, method, "responses", status, "content", mediaType, "schema")
}

func (a *margoAPI) schema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	return a.compile(t, "#/components/schemas/"+name)
}

// refAt follows keys from the document root to a schema object and compiles the schema it
// references, so the tests take the operation → schema mapping from the file itself.
func (a *margoAPI) refAt(t *testing.T, keys ...string) *jsonschema.Schema {
	t.Helper()
	s, ok := lookup(a.doc, keys...).(map[string]any)
	if !ok {
		t.Fatalf("vendored Margo file has no schema at %v", keys)
	}
	ref, ok := s["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/components/") {
		t.Fatalf("schema at %v is not a component $ref: %v", keys, s)
	}
	return a.compile(t, ref)
}

func (a *margoAPI) compile(t *testing.T, fragment string) *jsonschema.Schema {
	t.Helper()
	sch, err := a.compiler.Compile(margoResource + fragment)
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

func validateJSON(s *jsonschema.Schema, b []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	return s.Validate(v)
}
