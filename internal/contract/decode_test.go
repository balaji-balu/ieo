package contract_test

import (
	"errors"
	"testing"

	"github.com/balaji-balu/ieo/internal/contract"
)

func TestDecodeDeploymentStatusTakesOnlyExactKeys(t *testing.T) {
	body := `{"deploymentId":"` + testUUID2 + `","DEPLOYMENTID":"00000000-0000-0000-0000-000000000001",` +
		`"adoptedManifestVersion":3,"status":{"state":"installed"},"components":[]}`
	s, _, err := contract.DecodeDeploymentStatus([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.DeploymentID.String() != testUUID2 {
		t.Errorf("deploymentId %s, want the validated %s", s.DeploymentID, testUUID2)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       error
		field      string
	}{
		{"not JSON", `{"properties":`, contract.ErrMalformed, ""},
		{"trailing data", `{"properties":{"id":"s","vendor":"v","modelNumber":"m","serialNumber":"1"}} x`, contract.ErrMalformed, ""},
		{"schema", `{"properties":{"id":"s","vendor":"v","modelNumber":"m","serialNumber":"1","cpus":[{}]}}`,
			contract.ErrSemantic, "properties.cpus.0"},
		{"label that is not a string", `{"properties":{"id":"s","vendor":"v","modelNumber":"m","serialNumber":"1"},"labels":{"a":1}}`,
			contract.ErrSemantic, "labels.a"},
	} {
		_, fields, err := contract.DecodeDeviceCapabilities([]byte(tt.body))
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: err %v, want %v", tt.name, err, tt.want)
			continue
		}
		if errors.Is(tt.want, contract.ErrSemantic) && (len(fields) == 0 || fields[0].Field != tt.field) {
			t.Errorf("%s: fields %+v, want first at %q", tt.name, fields, tt.field)
		}
	}
}
