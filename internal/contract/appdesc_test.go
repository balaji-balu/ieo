package contract

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// appDescription is valid against the pinned schema only with both workarounds of
// api/margo/f209a7f/README.md: vendor extensions at three levels, and a configuration schema
// that uses a TextValidationSchema field (maxLength).
const appDescription = `apiVersion: margo.org/v1-alpha1
id: com-example-hello
metadata:
  name: Hello
  version: 1.2.3
  catalog:
    organization:
      - name: Example
x-acme-extensions:
  tier: gold
deploymentProfiles:
  - type: compose
    id: hello-compose
    x-acme-extensions: {gpu: false}
    deviceConstraints:
      capacityRequirements:
        memory: 512Mi
    components:
      - name: web
        x-acme-extensions: {probe: /healthz}
        properties:
          repository: oci://registry.test/example/hello-web
          revision: 1.2.3_build.5
          wait: false
          timeout: 5m0s
  - type: helm
    id: hello-helm
    components:
      - name: web-chart
        properties:
          repository: oci://registry.test/example/hello-chart
          revision: 1.2.3
parameters:
  greeting:
    value: Hello
    targets:
      - pointer: GREETING
        components: [web]
configuration:
  sections:
    - name: General
      settings:
        - parameter: greeting
          name: Greeting
          schema: text
  schema:
    - name: text
      dataType: string
      maxLength: 45
`

func TestParseApplicationDescription(t *testing.T) {
	got, err := ParseApplicationDescription([]byte(appDescription))
	if err != nil {
		t.Fatalf("ParseApplicationDescription: %v", err)
	}
	no := false
	want := ApplicationDescription{
		ID:       "com-example-hello",
		Metadata: ApplicationMetadata{Name: "Hello", Version: "1.2.3"},
		DeploymentProfiles: []ApplicationDeploymentProfile{
			{ID: "hello-compose", Type: "compose", Components: []Component{{
				Name: "web",
				Properties: ComponentProperties{
					Repository: "oci://registry.test/example/hello-web", Revision: "1.2.3_build.5",
					Wait: &no, Timeout: "5m0s",
				},
			}}, DeviceConstraints: &DeviceConstraints{CapacityRequirements: &CapacityRequirements{Memory: "512Mi"}}},
			{ID: "hello-helm", Type: "helm", Components: []Component{{
				Name: "web-chart",
				Properties: ComponentProperties{
					Repository: "oci://registry.test/example/hello-chart", Revision: "1.2.3",
				},
			}}},
		},
		Parameters: map[string]ApplicationParameter{
			"greeting": {Value: "Hello", Targets: []ParameterTarget{{Pointer: "GREETING", Components: []string{"web"}}}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseApplicationDescription =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseApplicationDescriptionRejects(t *testing.T) {
	tests := []struct {
		name, old, new string
		// reason is a part of the error message that names the violation.
		reason string
	}{
		{"not YAML", "id: com-example-hello", "id: [unclosed", "application description"},
		{"duplicate key", "id: com-example-hello", "id: com-example-hello\nid: other", "already defined"},
		{"missing required field", "apiVersion: margo.org/v1-alpha1\n", "", "apiVersion"},
		{"unknown field in a closed object", "    id: hello-compose\n", "    id: hello-compose\n    color: red\n", "color"},
		{"extension key without -extensions", "x-acme-extensions: {gpu: false}", "x-acme: {gpu: false}", "x-acme"},
		{"extension that is not an object", "x-acme-extensions: {gpu: false}", "x-acme-extensions: on", "x-acme-extensions"},
		{"profile type outside helm, compose, custom", "type: helm", "type: helm.v3", "/type"},
		{"version that is not a string", "version: 1.2.3", "version: 1.2", "/metadata/version"},
		{"key differing only in case is unknown", "  version: 1.2.3\n", "  version: 1.2.3\n  Version: 9.9.9\n", "Version"},
		// Aliases can expand exponentially ("billion laughs"), so none is accepted (SPEC §5.3).
		{"YAML alias", "  name: Hello\n  version: 1.2.3\n  catalog:\n    organization:\n      - name: Example",
			"  name: &n Hello\n  version: 1.2.3\n  catalog:\n    organization:\n      - name: *n", "alias"},
		{"merge key alias",
			"    x-acme-extensions: {gpu: false}\n    deviceConstraints:\n      capacityRequirements:\n        memory: 512Mi\n    components:\n      - name: web\n        x-acme-extensions: {probe: /healthz}",
			"    x-acme-extensions: &e {gpu: false}\n    deviceConstraints:\n      capacityRequirements:\n        memory: 512Mi\n    components:\n      - name: web\n        x-acme-extensions: {<<: *e, probe: /healthz}",
			"alias"},
		{"two YAML documents", "configuration:", "---\nid: other\n---\nconfiguration:", "document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(appDescription, tt.old) {
				t.Fatalf("fixture does not contain %q", tt.old)
			}
			_, err := ParseApplicationDescription([]byte(strings.Replace(appDescription, tt.old, tt.new, 1)))
			if !errors.Is(err, ErrInvalidApplicationDescription) {
				t.Fatalf("got error %v, want ErrInvalidApplicationDescription", err)
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("error %q does not name %q", err, tt.reason)
			}
		})
	}
}
