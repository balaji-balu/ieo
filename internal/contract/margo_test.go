package contract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/balaji-balu/ieo/api/margo"
	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/contract/margotest"
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
	api := margotest.Load(t)

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
			api.Request(t, "put", "/api/v1/capabilities/{deviceId}", "application/json"),
			&contract.DeviceCapabilitiesManifest{Properties: contract.DeviceCapabilities{
				ID: mustDevice(t, "site-1"), Vendor: "ieo", ModelNumber: "lo", SerialNumber: "1",
			}}},
		{"capabilities: host",
			api.Request(t, "put", "/api/v1/capabilities/{deviceId}", "application/json"),
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
			api.Response(t, "get", "/api/v1/deployments", "200", "application/vnd.margo.manifest.v1+json"),
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
			api.Response(t, "get", "/api/v1/deployments", "200", "application/vnd.margo.manifest.v1+json"),
			&contract.StateManifest{ManifestVersion: 1}},
		{"deployment status",
			api.Request(t, "post", "/api/v1/deployments/{deploymentId}/status", "application/json"),
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
			api.Request(t, "post", "/api/v1/deployments/{deploymentId}/status", "application/json"),
			&contract.DeploymentStatus{
				DeploymentID: uuid.MustParse(testUUID2), DeviceID: host, AdoptedManifestVersion: 1,
				Status: contract.DeploymentState{State: contract.StatePending},
			}},
		{"problem",
			api.Response(t, "get", "/api/v1/deployments", "403", "application/problem+json"),
			&contract.Problem{Type: "https://docs.margo.org/specification/problem-types/x", Title: "t",
				Status: 403, Detail: "d", Instance: "/api/v1/deployments"}},
		// The deployment endpoint returns raw YAML (`type: string`); the file describes its content
		// as appDeploymentManifest.
		{"application deployment",
			api.Schema(t, "appDeploymentManifest"),
			&deployment},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if err := margotest.Validate(tt.schema, b); err != nil {
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
			api.Response(t, "get", "/api/v1/deployments", "200", "application/vnd.margo.manifest.v1+json"),
			`{"manifestVersion":1,"deployments":[]}`},
		{"capabilities with bad device ID",
			api.Request(t, "put", "/api/v1/capabilities/{deviceId}", "application/json"),
			`{"properties":{"id":"site 1","vendor":"v","modelNumber":"m","serialNumber":"s"}}`},
		{"status with unknown state",
			api.Request(t, "post", "/api/v1/deployments/{deploymentId}/status", "application/json"),
			`{"deploymentId":"d","adoptedManifestVersion":1,"status":{"state":"running"},"components":[]}`},
		{"deployment with a `+` revision",
			api.Schema(t, "appDeploymentManifest"),
			strings.Replace(deploymentJSON, "1.0.0_build.1", "1.0.0+build.1", 1)},
	}
	for _, tt := range invalid {
		if err := margotest.Validate(tt.schema, []byte(tt.body)); err == nil {
			t.Errorf("%s: %s validated, want rejected", tt.name, tt.body)
		}
	}
}

// TestOneVendoredMargoAPIFile checks that the file margotest embeds is the only pinned one (SPEC
// §17.1, ADR 0010).
func TestOneVendoredMargoAPIFile(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "api", "margo", "*", "workload-management-api-*.yaml"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want exactly one vendored Margo OpenAPI file under api/margo/, got %v (err %v)", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, margo.WorkloadManagementAPI) {
		t.Errorf("%s differs from the embedded margo.WorkloadManagementAPI", files[0])
	}
}

// A component's `timeout` has Margo's ##m##s form (pinned OpenAPI, appDeploymentManifest).
func TestParseComponentTimeout(t *testing.T) {
	valid := map[string]time.Duration{
		"5m0s":    5 * time.Minute,
		"8m30s":   8*time.Minute + 30*time.Second,
		"0m45s":   45 * time.Second,
		"0m0s":    0,
		"1m90s":   2*time.Minute + 30*time.Second,
		"120m00s": 2 * time.Hour,
	}
	for in, want := range valid {
		got, err := contract.ParseComponentTimeout(in)
		if err != nil || got != want {
			t.Errorf("ParseComponentTimeout(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "5m", "30s", "5m0", "1h0m0s", "-5m0s", "5m 0s", "5.5m0s", " 5m0s", "5m0s\n",
		"99999999999999999999m0s", "9223372036854775807m0s", "0m9223372036854775807s"} {
		if got, err := contract.ParseComponentTimeout(in); err == nil {
			t.Errorf("ParseComponentTimeout(%q) = %v, want an error", in, got)
		}
	}
}
