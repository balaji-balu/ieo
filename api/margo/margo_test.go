package margo_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/balaji-balu/ieo/api/margo"
)

// TestWorkloadManagementAPIDocumentFixesRequired checks the workaround in f209a7f/README.md:
// UnsignedAppStateManifest's dotted names become required properties of the bundle schema they
// name, DeploymentBundleRef.
func TestWorkloadManagementAPIDocumentFixesRequired(t *testing.T) {
	doc, err := margo.WorkloadManagementAPIDocument()
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for name, s := range schemas {
		req, _ := s.(map[string]any)["required"].([]any)
		for _, r := range req {
			if strings.Contains(r.(string), ".") {
				t.Errorf("%s still requires %q", name, r)
			}
		}
	}
	required := func(name string) []string {
		var out []string
		req, _ := schemas[name].(map[string]any)["required"].([]any)
		for _, r := range req {
			out = append(out, r.(string))
		}
		return out
	}
	if got, want := required("UnsignedAppStateManifest"), []string{"manifestVersion", "bundle", "deployments"}; !slices.Equal(got, want) {
		t.Errorf("UnsignedAppStateManifest required = %v, want %v", got, want)
	}
	if got, want := required("DeploymentBundleRef"), []string{"mediaType", "digest", "url"}; !slices.Equal(got, want) {
		t.Errorf("DeploymentBundleRef required = %v, want %v", got, want)
	}

	// Each call returns its own value, so one caller's change never reaches another.
	doc["components"] = nil
	again, err := margo.WorkloadManagementAPIDocument()
	if err != nil || again["components"] == nil {
		t.Errorf("second call shares the first call's value (err %v)", err)
	}
}

// TestWorkloadManagementAPIDocumentDropsUndefinedRequired checks the workaround in
// f209a7f/README.md: appDeploymentMetadata requires `annotations`, a property neither it nor
// Margo's DesiredState definition has, so no deployment Margo calls valid would pass.
func TestWorkloadManagementAPIDocumentDropsUndefinedRequired(t *testing.T) {
	doc, err := margo.WorkloadManagementAPIDocument()
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	metadata := schemas["appDeploymentMetadata"].(map[string]any)
	var got []string
	for _, r := range metadata["required"].([]any) {
		got = append(got, r.(string))
	}
	if want := []string{"name", "namespace", "deviceId"}; !slices.Equal(got, want) {
		t.Errorf("appDeploymentMetadata required = %v, want %v", got, want)
	}
	// The workaround is for this one defect: it must be dropped when a pin change fixes the file.
	if _, defined := metadata["properties"].(map[string]any)["annotations"]; defined {
		t.Error("appDeploymentMetadata now defines annotations: drop the workaround and its README row")
	}
}
