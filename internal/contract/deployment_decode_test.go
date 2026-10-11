package contract_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// layoutDeployment returns the deployment YAML the CO builds from layoutDescription.
func layoutDeployment(t *testing.T, modify func(c *contract.DeploymentChoices)) []byte {
	t.Helper()
	c := layoutChoices()
	if modify != nil {
		modify(&c)
	}
	b, err := contract.BuildApplicationDeployment([]byte(layoutDescription), c)
	if err != nil {
		t.Fatalf("test setup: build deployment: %v", err)
	}
	return b
}

// The YAML an LO holds decodes into the deployment of an Apply (SPEC §8.5, §11.2): what the CO
// builds is accepted, with its extension keys ignored.
func TestDecodeApplicationDeployment(t *testing.T) {
	id := uuid.MustParse(testUUID)
	d, err := contract.DecodeApplicationDeployment(id, layoutDeployment(t, nil))
	if err != nil {
		t.Fatalf("DecodeApplicationDeployment: %v", err)
	}
	if d.ID != id || d.Metadata.Name != "hello" || d.Metadata.Namespace != "apps" ||
		d.Metadata.DeviceID != (contract.DeviceID{Site: "site-1", Host: "host-1"}) {
		t.Errorf("decoded id and metadata %+v %+v", d.ID, d.Metadata)
	}
	p := d.Spec.DeploymentProfile
	if d.Spec.ApplicationID != "com-example-hello" || p.Type != "compose" || len(p.Components) != 1 ||
		p.Components[0].Name != "web" || p.Components[0].Properties.Revision != "1.2.3_build.5" ||
		!strings.Contains(string(p.DeviceConstraints), "512Mi") {
		t.Errorf("decoded spec %+v", d.Spec)
	}
	if d.Spec.Parameters["token"].Value != "t-1" || d.Spec.Parameters["greeting"].Value != "Hello" {
		t.Errorf("decoded parameters %+v", d.Spec.Parameters)
	}

	autonomous := layoutDeployment(t, func(c *contract.DeploymentChoices) {
		c.DeviceID = contract.DeviceID{Site: "site-1", Autonomous: true}
	})
	d, err = contract.DecodeApplicationDeployment(id, autonomous)
	if err != nil || !d.Metadata.DeviceID.Autonomous {
		t.Errorf("autonomous deployment = %+v, %v", d.Metadata, err)
	}
}

// What the LO decodes, an EN accepts: the deployment survives the Apply command unchanged (SPEC
// §8.9 step 2).
func TestDecodeApplicationDeploymentIsWhatAnENAccepts(t *testing.T) {
	id := uuid.MustParse(testUUID)
	d, err := contract.DecodeApplicationDeployment(id, layoutDeployment(t, nil))
	if err != nil {
		t.Fatalf("DecodeApplicationDeployment: %v", err)
	}
	sent := contract.Command{
		CommandID: uuid.MustParse(testUUID2), Action: contract.ActionApply, DeploymentID: id, Digest: testDigest, Deployment: &d,
	}
	data, err := contract.EncodeSiteMessage(sent)
	if err != nil {
		t.Fatalf("encode the Apply: %v", err)
	}
	got, err := contract.DecodeCommand(data)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}
	if got.Deployment == nil || !reflect.DeepEqual(*got.Deployment, d) {
		t.Errorf("the EN decoded\n%+v\nwant\n%+v", got.Deployment, d)
	}
}

// A deployment an EN would refuse is refused by the LO for the same reason (SPEC §8.9 step 2), and
// so is YAML IEO never reads (SPEC §5.3).
func TestDecodeApplicationDeploymentRejects(t *testing.T) {
	valid := string(layoutDeployment(t, nil))
	replace := func(old, new string) string {
		if !strings.Contains(valid, old) {
			t.Fatalf("test setup: %q is not in the deployment", old)
		}
		return strings.Replace(valid, old, new, 1)
	}
	component := `    - name: web
      properties:
        repository: oci://registry.test/example/hello-web
        revision: 1.2.3_build.5
`
	cases := map[string]string{
		"not YAML":                             "id: [",
		"empty":                                "",
		"two documents":                        valid + "---\n" + valid,
		"an alias":                             replace("name: hello", "name: &n hello") + "x-again: *n\n",
		"fails the Margo schema: no metadata":  replace("metadata:", "meta:"),
		"fails the Margo schema: unknown key":  replace("    type: compose", "    type: compose\n    replicas: 3"),
		"fails the Margo schema: bad timeout":  replace("revision: 1.2.3_build.5", "revision: 1.2.3_build.5\n        timeout: 5 minutes"),
		"id is another deployment":             replace("id: "+testUUID, "id: "+testUUID2),
		"id is not a UUID":                     replace("id: "+testUUID, "id: hello"),
		"device ID is not one":                 replace("deviceId: site-1/host-1", "deviceId: site 1/host-1"),
		"profile type is helm":                 replace("    type: compose", "    type: helm"),
		"two components with one project name": replace(component, component+strings.Replace(component, "name: web", "name: WEB", 1)),
		"timeout too large to be a duration":   replace("revision: 1.2.3_build.5", "revision: 1.2.3_build.5\n        timeout: 99999999999999999999m0s"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := contract.DecodeApplicationDeployment(uuid.MustParse(testUUID), []byte(data))
			if !errors.Is(err, contract.ErrInvalidDeployment) {
				t.Fatalf("error = %v, want one wrapping contract.ErrInvalidDeployment", err)
			}
			if !reflect.DeepEqual(d, contract.ApplicationDeployment{}) {
				t.Errorf("returned %+v with the error, want the zero deployment", d)
			}
		})
	}
}
