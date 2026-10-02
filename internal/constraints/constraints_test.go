package constraints_test

import (
	"errors"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/balaji-balu/ieo/internal/constraints"
	"github.com/balaji-balu/ieo/internal/contract"
)

// host reports two CPU entries, memory, storage, two peripherals and string labels.
func host() contract.DeviceCapabilitiesManifest {
	return contract.DeviceCapabilitiesManifest{
		Properties: contract.DeviceCapabilities{
			ID:     contract.DeviceID{Site: "site-1", Host: "host-1"},
			Vendor: "acme", ModelNumber: "en", SerialNumber: "1",
			CPUs:    []contract.CPU{{Cores: 4, Architecture: "amd64"}, {Cores: 2, Architecture: "arm64"}},
			Memory:  "8Gi",
			Storage: "64Gi",
			Peripherals: []contract.Peripheral{
				{Type: "gpu", Manufacturer: "nvidia", Model: "a2"},
				{Type: "camera", Manufacturer: "acme"},
			},
			Interfaces:               []contract.CommunicationInterface{{Type: "ethernet"}},
			OTelCollector:            true,
			SupportedRuntimes:        []string{"oci"},
			SupportedDeploymentTypes: []string{"compose"},
		},
		Labels: map[string]string{"line": "2", "zone": "north", "gpu": "true", "ratio": "1.5"},
	}
}

// label and property wrap one match expression, written as YAML the way a description holds it.
func label(expr string) string {
	return "eligibilityRules:\n  - labelSelector:\n      matchExpressions:\n        - " + expr + "\n"
}

func property(expr string) string {
	return "eligibilityRules:\n  - propertySelector:\n      matchExpressions:\n        - " + expr + "\n"
}

// TestCheck covers SPEC §5.5 rule by rule. Constraints are decoded from YAML, as the CO reads them
// from an Application Description, so values arrive with YAML's types.
func TestCheck(t *testing.T) {
	tests := []struct {
		name        string
		constraints string
		ok          bool
	}{
		{"no constraints", "{}", true},

		// Labels compare by text form.
		{"label In string", label(`{key: line, operator: In, values: ["2"]}`), true},
		{"label In number by text form", label(`{key: line, operator: In, values: [2]}`), true},
		{"label In boolean by text form", label(`{key: gpu, operator: In, values: [true]}`), true},
		{"label In no match", label(`{key: line, operator: In, values: ["3", 4]}`), false},
		{"label In absent", label(`{key: hall, operator: In, values: ["2"]}`), false},
		{"label NotIn match", label(`{key: line, operator: NotIn, values: ["2"]}`), false},
		{"label NotIn other", label(`{key: line, operator: NotIn, values: ["3"]}`), true},
		{"label NotIn absent", label(`{key: hall, operator: NotIn, values: ["2"]}`), true},
		{"label Exists", label(`{key: zone, operator: Exists}`), true},
		{"label Exists absent", label(`{key: hall, operator: Exists}`), false},
		{"label DoesNotExist absent", label(`{key: hall, operator: DoesNotExist}`), true},
		{"label DoesNotExist present", label(`{key: zone, operator: DoesNotExist}`), false},
		{"label Gt", label(`{key: ratio, operator: Gt, values: [1]}`), true},
		{"label Lt", label(`{key: ratio, operator: Lt, values: [1]}`), false},
		{"label Gt not a number", label(`{key: zone, operator: Gt, values: [1]}`), false},
		{"label Gt two values", label(`{key: ratio, operator: Gt, values: [1, 2]}`), false},
		{"label Gt value not a number", label(`{key: ratio, operator: Gt, values: ["1"]}`), false},
		{"label Gt absent", label(`{key: hall, operator: Gt, values: [1]}`), false},
		{"unknown operator", label(`{key: line, operator: Like, values: ["2"]}`), false},

		// Properties are JSON Pointers into the wire encoding and compare by JSON type.
		{"property In string", property(`{key: /cpus/0/architecture, operator: In, values: [amd64]}`), true},
		{"property In number", property(`{key: /cpus/0/cores, operator: In, values: [4]}`), true},
		{"property In number as string", property(`{key: /cpus/0/cores, operator: In, values: ["4"]}`), false},
		{"property In boolean", property(`{key: /otelCollector, operator: In, values: [true]}`), true},
		{"property Gt", property(`{key: /cpus/1/cores, operator: Gt, values: [1.5]}`), true},
		{"property Lt", property(`{key: /cpus/1/cores, operator: Lt, values: [2]}`), false},
		{"property Gt on a string", property(`{key: /memory, operator: Gt, values: [1]}`), false},
		{"property Exists", property(`{key: /peripherals/1/manufacturer, operator: Exists}`), true},
		{"property index out of range", property(`{key: /cpus/5/cores, operator: Exists}`), false},
		{"property unknown field", property(`{key: /gpus, operator: DoesNotExist}`), true},
		{"property NotIn absent", property(`{key: /gpus, operator: NotIn, values: [1]}`), true},
		{"ContainsAny", property(`{key: /peripherals, operator: ContainsAny, itemSelector: {matchExpressions: [
            {key: /type, operator: In, values: [display]}, {key: /type, operator: In, values: [gpu]}]}}`), true},
		{"ContainsAny none", property(`{key: /peripherals, operator: ContainsAny, itemSelector: {matchExpressions: [
            {key: /type, operator: In, values: [display]}]}}`), false},
		{"ContainsAll in one element", property(`{key: /peripherals, operator: ContainsAll, itemSelector: {matchExpressions: [
            {key: /type, operator: In, values: [gpu]}, {key: /manufacturer, operator: In, values: [nvidia]}]}}`), true},
		{"ContainsAll split across elements", property(`{key: /peripherals, operator: ContainsAll, itemSelector: {matchExpressions: [
            {key: /type, operator: In, values: [gpu]}, {key: /manufacturer, operator: In, values: [acme]}]}}`), false},
		{"ContainsAny on a string", property(`{key: /memory, operator: ContainsAny, itemSelector: {matchExpressions: [
            {key: /type, operator: Exists}]}}`), false},
		{"ContainsAny absent", property(`{key: /gpus, operator: ContainsAny, itemSelector: {matchExpressions: [
            {key: /type, operator: Exists}]}}`), false},

		// Combining: every rule, both selectors of a rule, every expression of a selector.
		{"all expressions of a selector", label(`{key: line, operator: In, values: ["2"]}
        - {key: zone, operator: In, values: [south]}`), false},
		{"both selectors of a rule", `eligibilityRules:
  - labelSelector: {matchExpressions: [{key: line, operator: In, values: ["2"]}]}
    propertySelector: {matchExpressions: [{key: /cpus/0/architecture, operator: In, values: [arm]}]}
`, false},
		{"every rule", `eligibilityRules:
  - labelSelector: {matchExpressions: [{key: line, operator: In, values: ["2"]}]}
  - labelSelector: {matchExpressions: [{key: zone, operator: In, values: [south]}]}
`, false},
		{"every rule matches", `eligibilityRules:
  - labelSelector: {matchExpressions: [{key: line, operator: In, values: ["2"]}]}
  - propertySelector: {matchExpressions: [{key: /supportedRuntimes/0, operator: In, values: [oci]}]}
`, true},

		// Capacity, against reported totals.
		{"memory enough", "capacityRequirements: {memory: 512Mi}", true},
		{"memory equal", "capacityRequirements: {memory: 8Gi}", true},
		{"memory too much", "capacityRequirements: {memory: 9Gi}", false},
		{"storage too much", "capacityRequirements: {storage: 1Ti}", false},
		{"cpu cores on one entry", "capacityRequirements: {cpu: {cores: 4}}", true},
		{"cpu cores never summed", "capacityRequirements: {cpu: {cores: 5}}", false},
		{"cpu architecture", "capacityRequirements: {cpu: {cores: 1.5, architectures: [arm64, arm]}}", true},
		{"cpu architecture lacks cores", "capacityRequirements: {cpu: {cores: 3, architectures: [arm64]}}", false},
		{"cpu architecture missing", "capacityRequirements: {cpu: {cores: 1, architectures: [riscv64]}}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c contract.DeviceConstraints
			if err := yaml.Unmarshal([]byte(tt.constraints), &c); err != nil {
				t.Fatalf("decode constraints: %v\n%s", err, tt.constraints)
			}
			err := constraints.Check(c, host())
			switch {
			case tt.ok && err != nil:
				t.Errorf("Check: %v, want satisfied", err)
			case !tt.ok && !errors.Is(err, constraints.ErrNotSatisfied):
				t.Errorf("Check: err = %v, want ErrNotSatisfied", err)
			}
		})
	}
}

// TestCheckUnreportedCapacity: a host that doesn't report a required field fails it (SPEC §5.5).
func TestCheckUnreportedCapacity(t *testing.T) {
	h := host()
	h.Properties.Memory, h.Properties.Storage, h.Properties.CPUs = "", "", nil
	for _, req := range []contract.CapacityRequirements{
		{Memory: "1Ki"}, {Storage: "1Ki"}, {CPU: &contract.CPURequirement{Cores: 0.5}},
	} {
		err := constraints.Check(contract.DeviceConstraints{CapacityRequirements: &req}, h)
		if !errors.Is(err, constraints.ErrNotSatisfied) {
			t.Errorf("requirement %+v on a host that doesn't report it: err = %v, want ErrNotSatisfied", req, err)
		}
	}
}

func TestParseQuantity(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want uint64
	}{
		{"1Ki", 1 << 10}, {"512Mi", 512 << 20}, {"8Gi", 8 << 30}, {"2Ti", 2 << 40}, {"3Pi", 3 << 50}, {"15Ei", 15 << 60},
	} {
		if got, err := constraints.ParseQuantity(tt.in); err != nil || got != tt.want {
			t.Errorf("ParseQuantity(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "512", "Mi", "1.5Gi", "1Gb", "1 Gi", "-1Ki", "16Ei", "99999999999999999999Ki"} {
		if got, err := constraints.ParseQuantity(in); err == nil {
			t.Errorf("ParseQuantity(%q) = %d, want an error", in, got)
		}
	}
}
