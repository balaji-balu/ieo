package contract_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/contract/margotest"
)

const layoutDescription = `apiVersion: margo.org/v1-alpha1
id: com-example-hello
metadata:
  name: Hello
  version: 1.2.3
  catalog:
    organization:
      - name: Example
x-acme-extensions: {owner: team-a}
deploymentProfiles:
  - type: compose
    id: other
    components:
      - name: other
        properties:
          repository: oci://registry.test/example/other
          revision: 1.0.0
  - id: hello-compose
    x-acme-extensions: {tier: gold}
    type: compose
    deviceConstraints:
      capacityRequirements: {memory: 512Mi}
    components:
      - name: web
        properties:
          repository: oci://registry.test/example/hello-web
          revision: 1.2.3_build.5
parameters:
  greeting:
    value: Hello
    targets:
      - pointer: GREETING
        components: [web]
  token:
    targets:
      - pointer: TOKEN
        components: [web]
`

func layoutChoices() contract.DeploymentChoices {
	return contract.DeploymentChoices{
		ID:         uuid.MustParse(testUUID),
		Name:       "hello",
		Namespace:  "apps",
		DeviceID:   contract.DeviceID{Site: "site-1", Host: "host-1"},
		Profile:    "hello-compose",
		Parameters: map[string]any{"token": "t-1"},
	}
}

// TestBuildApplicationDeployment pins the layout of SPEC §4.1.5: Margo's key order, the
// selected profile without its id, its extensions after deviceConstraints, parameter values with
// defaults filled in, and the top-level extensions last in spec.
func TestBuildApplicationDeployment(t *testing.T) {
	got, err := contract.BuildApplicationDeployment([]byte(layoutDescription), layoutChoices())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := `id: ` + testUUID + `
metadata:
  name: hello
  namespace: apps
  deviceId: site-1/host-1
spec:
  applicationId: com-example-hello
  deploymentProfile:
    type: compose
    components:
    - name: web
      properties:
        repository: oci://registry.test/example/hello-web
        revision: 1.2.3_build.5
    deviceConstraints:
      capacityRequirements:
        memory: 512Mi
    x-acme-extensions:
      tier: gold
  parameters:
    greeting:
      value: Hello
      targets:
      - pointer: GREETING
        components:
        - web
    token:
      value: t-1
      targets:
      - pointer: TOKEN
        components:
        - web
  x-acme-extensions:
    owner: team-a
`
	if string(got) != want {
		t.Errorf("deployment YAML:\n%s\nwant:\n%s", got, want)
	}
	again, err := contract.BuildApplicationDeployment([]byte(layoutDescription), layoutChoices())
	if err != nil || string(again) != string(got) {
		t.Errorf("second build differs (err %v):\n%s", err, again)
	}
}

func TestBuildApplicationDeploymentRejects(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(c *contract.DeploymentChoices)
		parameter bool // the error wraps ErrDeploymentParameter
	}{
		{"required parameter without value", func(c *contract.DeploymentChoices) { delete(c.Parameters, "token") }, true},
		{"required parameter set to null", func(c *contract.DeploymentChoices) { c.Parameters["token"] = nil }, true},
		{"unknown parameter", func(c *contract.DeploymentChoices) { c.Parameters["colour"] = "red" }, true},
		{"unknown profile", func(c *contract.DeploymentChoices) { c.Profile = "nope" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := layoutChoices()
			tt.modify(&c)
			_, err := contract.BuildApplicationDeployment([]byte(layoutDescription), c)
			if err == nil {
				t.Fatal("build succeeded, want an error")
			}
			if got := errors.Is(err, contract.ErrDeploymentParameter); got != tt.parameter {
				t.Errorf("err = %v; wraps ErrDeploymentParameter = %v, want %v", err, got, tt.parameter)
			}
		})
	}
}

// A deployment the CO builds is one an EN applies: it passes the check of SPEC §8.9 step 2 as the
// `deployment` of an Apply, without the `annotations` the pinned OpenAPI file wrongly requires
// (api/margo/f209a7f/README.md).
func TestBuiltDeploymentIsAcceptedInAnApply(t *testing.T) {
	built, err := contract.BuildApplicationDeployment([]byte(layoutDescription), layoutChoices())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	js, err := yaml.YAMLToJSON(built)
	if err != nil {
		t.Fatalf("deployment YAML to JSON: %v", err)
	}
	if err := margotest.Validate(margotest.Load(t).Schema(t, "appDeploymentManifest"), js); err != nil {
		t.Errorf("the built deployment fails Margo's appDeploymentManifest: %v", err)
	}
	apply := `{"commandId":"` + testUUID2 + `","action":"apply","deploymentId":"` + testUUID +
		`","digest":"` + testDigest + `","deployment":` + string(js) + `}`
	cmd, err := contract.DecodeCommand([]byte(apply))
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}
	// The LO sends the decoded value, so it must pass again once encoded from the Go type.
	again, err := contract.EncodeSiteMessage(cmd)
	if err != nil {
		t.Fatalf("encode the decoded Apply: %v", err)
	}
	if _, err := contract.DecodeCommand(again); err != nil {
		t.Errorf("DecodeCommand of the encoded Apply: %v", err)
	}
	if strings.Contains(string(again), "annotations") {
		t.Errorf("the encoded Apply names annotations, which the deployment does not have: %s", again)
	}
}

// Margo's own valid DesiredState example at the pinned commit validates against the pinned
// OpenAPI file once its defects are worked around: its metadata has a name, a namespace and a
// device ID, and no annotations (api/margo/f209a7f/README.md).
func TestMargoDesiredStateExampleValidates(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "api", "margo", "f209a7f", "DesiredState-001.yaml"))
	if err != nil {
		t.Fatalf("read Margo's example: %v", err)
	}
	if strings.Contains(string(example), "annotations") {
		t.Fatal("test setup: Margo's example has annotations")
	}
	js, err := yaml.YAMLToJSON(example)
	if err != nil {
		t.Fatalf("example YAML to JSON: %v", err)
	}
	if err := margotest.Validate(margotest.Load(t).Schema(t, "appDeploymentManifest"), js); err != nil {
		t.Errorf("Margo's valid DesiredState example fails appDeploymentManifest: %v", err)
	}
}
