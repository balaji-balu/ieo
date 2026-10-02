package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/balaji-balu/ieo/api/margo"
)

var (
	// ErrMalformed means the body is not JSON (Margo #invalid-request, SPEC §11.1).
	ErrMalformed = errors.New("malformed request body")
	// ErrSemantic means the body is JSON but not a valid Margo body for the operation (Margo
	// #semantic-error, SPEC §11.1).
	ErrSemantic = errors.New("semantic error in request body")
)

// FieldError is one field-level error of a request body (Margo ProblemDetail `errors[]`). Field
// is a dotted path such as `properties.vendor`; empty means the whole body.
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// DecodeDeviceCapabilities validates b against the Margo DeviceCapabilitiesManifest schema and
// decodes it (SPEC §11.1). Label values must be strings (SPEC §5.5).
func DecodeDeviceCapabilities(b []byte) (DeviceCapabilitiesManifest, []FieldError, error) {
	return decodeMargo[DeviceCapabilitiesManifest]("DeviceCapabilitiesManifest", b)
}

// DecodeDeploymentStatus validates b against the Margo DeploymentStatusManifest schema and decodes
// it (SPEC §11.1).
func DecodeDeploymentStatus(b []byte) (DeploymentStatus, []FieldError, error) {
	return decodeMargo[DeploymentStatus]("DeploymentStatusManifest", b)
}

// decodeMargo returns ErrMalformed for input that isn't JSON, and ErrSemantic with field errors
// for JSON that fails the schema or doesn't decode into T.
func decodeMargo[T any](schema string, b []byte) (T, []FieldError, error) {
	var zero T
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return zero, nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if err := wfmSchemas()[schema].Validate(v); err != nil {
		return zero, fieldErrors(err), fmt.Errorf("%w: %s", ErrSemantic, schemaViolations(err))
	}
	if fields := nonStringLabels(v); len(fields) > 0 {
		return zero, fields, fmt.Errorf("%w: label values must be strings", ErrSemantic)
	}
	// Decode only keys that name a field exactly, as DecodeSiteMessage does: encoding/json
	// would let "DeploymentID" overwrite the validated "deploymentId".
	var out T
	known, err := json.Marshal(keepKnownFields(v, reflect.TypeOf(out)))
	if err == nil {
		err = json.Unmarshal(known, &out)
	}
	if err != nil {
		// e.g. a deploymentId that is not a UUID; Go's message would name Go types.
		return zero, []FieldError{{Message: "a value does not have the expected form"}}, fmt.Errorf("%w: %w", ErrSemantic, err)
	}
	return out, nil, nil
}

// nonStringLabels lists the labels whose value is not a string. Margo allows numbers, booleans
// and arrays; IEO labels are strings (SPEC §5.5).
func nonStringLabels(v any) []FieldError {
	obj, _ := v.(map[string]any)
	labels, _ := obj["labels"].(map[string]any)
	var out []FieldError
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		if _, ok := labels[k].(string); !ok {
			out = append(out, FieldError{Field: "labels." + k, Message: "label values must be strings"})
		}
	}
	return out
}

const wfmSchemaURL = "https://margo.invalid/workload-management-api.json"

// wfmSchemas compiles the request-body schemas of the pinned Margo OpenAPI file once. Neither
// has a known defect (api/margo/f209a7f/README.md). The file is fixed at build time and covered
// by tests, so a failure here is a programmer error (G-B4).
var wfmSchemas = sync.OnceValue(func() map[string]*jsonschema.Schema {
	js, err := yaml.YAMLToJSON(margo.WorkloadManagementAPI)
	if err != nil {
		panic(fmt.Sprintf("Margo OpenAPI file: %v", err))
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		panic(fmt.Sprintf("Margo OpenAPI file: %v", err))
	}
	// Only components are a schema resource; paths hold OpenAPI objects that are not JSON Schema.
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(wfmSchemaURL, map[string]any{"components": doc.(map[string]any)["components"]}); err != nil {
		panic(fmt.Sprintf("Margo OpenAPI file: %v", err))
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"DeviceCapabilitiesManifest", "DeploymentStatusManifest"} {
		schemas[name] = c.MustCompile(wfmSchemaURL + "#/components/schemas/" + name)
	}
	return schemas
})

// fieldErrors lists the leaf errors of a schema failure, each at its dotted field path.
func fieldErrors(err error) []FieldError {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []FieldError{{Message: err.Error()}}
	}
	var out []FieldError
	var walk func(u jsonschema.OutputUnit)
	walk = func(u jsonschema.OutputUnit) {
		if len(u.Errors) == 0 && u.Error != nil {
			out = append(out, FieldError{Field: dotted(u.InstanceLocation), Message: u.Error.String()})
		}
		for _, e := range u.Errors {
			walk(e)
		}
	}
	walk(*ve.BasicOutput())
	if len(out) == 0 {
		out = []FieldError{{Message: schemaViolations(err)}}
	}
	return out
}

// dotted turns a JSON pointer (`/properties/cpus/0`) into Margo's dotted form (`properties.cpus.0`).
func dotted(pointer string) string {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, p := range parts {
		parts[i] = strings.NewReplacer("~1", "/", "~0", "~").Replace(p)
	}
	return strings.Join(parts, ".")
}
