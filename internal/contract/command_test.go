package contract_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/balaji-balu/ieo/internal/contract"
)

// applyJSON is a valid Apply command for deploymentJSON.
const applyJSON = `{"commandId":"` + testUUID + `","action":"apply","deploymentId":"` + testUUID2 +
	`","digest":"` + testDigest + `","deployment":` + deploymentJSON + `}`

// A valid command decodes with its deployment; a Remove has none (SPEC §8.9, §11.2).
func TestDecodeCommand(t *testing.T) {
	cmd, err := contract.DecodeCommand([]byte(applyJSON))
	if err != nil {
		t.Fatalf("DecodeCommand(apply): %v", err)
	}
	if cmd.CommandID.String() != testUUID || cmd.Action != contract.ActionApply || cmd.DeploymentID.String() != testUUID2 || cmd.Digest != testDigest {
		t.Errorf("decoded %+v", cmd)
	}
	d := cmd.Deployment
	if d == nil || d.ID != cmd.DeploymentID || len(d.Spec.DeploymentProfile.Components) != 1 ||
		d.Spec.DeploymentProfile.Components[0].Properties.Timeout != "5m0s" || d.Spec.Parameters["greeting"].Value != "hi" {
		t.Errorf("decoded deployment %+v", d)
	}

	remove := `{"commandId":"` + testUUID + `","action":"remove","deploymentId":"` + testUUID2 + `","digest":"` + testDigest + `"}`
	cmd, err = contract.DecodeCommand([]byte(remove))
	if err != nil || cmd.Action != contract.ActionRemove || cmd.Deployment != nil {
		t.Errorf("DecodeCommand(remove) = %+v, %v", cmd, err)
	}

	// Unknown fields are ignored, in the command and in the deployment's extension points.
	extra := strings.Replace(applyJSON, `"action"`, `"priority":7,"ACTION":"remove","action"`, 1)
	if cmd, err := contract.DecodeCommand([]byte(extra)); err != nil || cmd.Action != contract.ActionApply {
		t.Errorf("DecodeCommand with unknown fields = %+v, %v", cmd, err)
	}
}

// An Apply whose deployment the EN must not apply is told apart from a message to drop: the error
// wraps ErrInvalidDeployment and the command can still be acked (SPEC §8.9 step 2).
func TestDecodeCommandInvalidDeployment(t *testing.T) {
	component := `{"name":"web","properties":{"repository":"oci://registry.local/hello/web",` +
		`"revision":"1.0.0_build.1","wait":true,"timeout":"5m0s"}}`
	replace := func(old, new string) string {
		if !strings.Contains(applyJSON, old) {
			t.Fatalf("test setup: %q is not in the command", old)
		}
		return strings.Replace(applyJSON, old, new, 1)
	}
	cases := map[string]string{
		"fails the Margo schema: no metadata":    replace(`"metadata":`, `"meta":`),
		"fails the Margo schema: bad repository": replace("oci://registry.local/hello/web", "https://registry.local/hello/web"),
		"fails the Margo schema: bad timeout":    replace(`"5m0s"`, `"5 minutes"`),
		"fails the Margo schema: unknown key":    replace(`"type":"compose"`, `"type":"compose","replicas":3`),
		"id is another deployment":               replace(`{"id":"`+testUUID2, `{"id":"`+testUUID),
		"id is missing":                          replace(`{"id":"`+testUUID2+`",`, `{`),
		"id is not a UUID":                       replace(`{"id":"`+testUUID2, `{"id":"hello`),
		"profile type is helm":                   replace(`"type":"compose"`, `"type":"helm"`),
		"two components with one project name":   replace(component, component+`,`+strings.Replace(component, `"web"`, `"WEB"`, 1)),
		"component names differing in a symbol":  replace(component, strings.Replace(component, `"web"`, `"a/b"`, 1)+`,`+strings.Replace(component, `"web"`, `"a.b"`, 1)),
		"component with an empty name":           replace(`"name":"web"`, `"name":""`),
		"timeout too large to be a duration":     replace(`"5m0s"`, `"99999999999999999999m0s"`),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, err := contract.DecodeCommand([]byte(data))
			if !errors.Is(err, contract.ErrInvalidDeployment) {
				t.Fatalf("error = %v, want one wrapping contract.ErrInvalidDeployment", err)
			}
			if errors.Is(err, contract.ErrInvalidMessage) {
				t.Errorf("error %v also wraps ErrInvalidMessage; the command must be acked, not dropped", err)
			}
			if cmd.CommandID.String() != testUUID || cmd.DeploymentID.String() != testUUID2 || cmd.Digest != testDigest || cmd.Deployment != nil {
				t.Errorf("returned %+v, want the command's IDs and digest and no deployment", cmd)
			}
		})
	}
}

// A message that is not a Command at all is dropped, not acked (SPEC §11.2).
func TestDecodeCommandInvalidMessage(t *testing.T) {
	cases := map[string]string{
		"not JSON":                    `{"commandId":`,
		"no commandId":                strings.Replace(applyJSON, `"commandId"`, `"cid"`, 1),
		"apply without deployment":    `{"commandId":"` + testUUID + `","action":"apply","deploymentId":"` + testUUID2 + `","digest":"` + testDigest + `"}`,
		"remove with a deployment":    strings.Replace(applyJSON, `"action":"apply"`, `"action":"remove"`, 1),
		"deployment is not an object": strings.Replace(applyJSON, deploymentJSON, `"hello"`, 1),
		"bad digest":                  strings.Replace(applyJSON, "sha256:", "md5:", 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, err := contract.DecodeCommand([]byte(data))
			if !errors.Is(err, contract.ErrInvalidMessage) || errors.Is(err, contract.ErrInvalidDeployment) {
				t.Errorf("error = %v, want one wrapping only contract.ErrInvalidMessage", err)
			}
			if cmd != (contract.Command{}) {
				t.Errorf("returned %+v with the error, want the zero Command", cmd)
			}
		})
	}
}
