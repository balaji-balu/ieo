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
	required := metadata["required"].([]any)
	if slices.Contains(required, any("annotations")) {
		t.Errorf("appDeploymentMetadata still requires annotations: %v", required)
	}
	for _, name := range []string{"name", "namespace", "deviceId"} {
		if !slices.Contains(required, any(name)) {
			t.Errorf("appDeploymentMetadata no longer requires %s: %v", name, required)
		}
	}

	// The workaround is for this one defect. When a pin change brings a file without it, these
	// fail: remove the workaround and its README row.
	if _, defined := metadata["properties"].(map[string]any)["annotations"]; defined {
		t.Error("the file now defines annotations in appDeploymentMetadata")
	}
	if !strings.Contains(string(margo.WorkloadManagementAPI), "required: [annotations, name, namespace, deviceId]") {
		t.Error("the file no longer requires annotations in appDeploymentMetadata")
	}
}
