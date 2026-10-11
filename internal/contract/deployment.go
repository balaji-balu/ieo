package contract

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrDeploymentParameter is returned, wrapped with the parameter name, when a deployment would
// leave a parameter without a value or names a parameter its Application Description does not
// define (SPEC §8.1.1).
var ErrDeploymentParameter = errors.New("deployment parameter")

// DeploymentChoices are what the CO chooses for one deployment of an Application Description:
// everything in the ApplicationDeployment that the description does not hold (SPEC §4.1.5).
type DeploymentChoices struct {
	ID        uuid.UUID
	Name      string
	Namespace string
	DeviceID  DeviceID
	// Profile is the `id` of the selected deployment profile.
	Profile string
	// Parameters are values by parameter name. A parameter left out takes the description's
	// `value`.
	Parameters map[string]any
}

// BuildApplicationDeployment returns the YAML bytes of the ApplicationDeployment for one
// deployment of description, an Application Description that ParseApplicationDescription
// accepts. The layout, the parameter values and the copied parts follow SPEC §4.1.5; the same
// inputs always give the same bytes. A parameter without a value or unknown to the description
// wraps ErrDeploymentParameter; any other error means choices don't fit description.
func BuildApplicationDeployment(description []byte, choices DeploymentChoices) ([]byte, error) {
	// The description was validated on import; checking for aliases again is cheap and keeps
	// this function safe on its own (SPEC §5.3).
	if err := checkPlainYAML(description); err != nil {
		return nil, fmt.Errorf("build deployment: %w", err)
	}
	// Ordered maps keep every key and its order, so copied parts stay exactly as the
	// description has them.
	var desc yaml.MapSlice
	if err := yaml.UnmarshalWithOptions(description, &desc, yaml.UseOrderedMap()); err != nil {
		return nil, fmt.Errorf("build deployment: %w", err)
	}
	profile, err := findProfile(desc, choices.Profile)
	if err != nil {
		return nil, fmt.Errorf("build deployment: %w", err)
	}
	params, err := deploymentParameters(get(desc, "parameters"), choices.Parameters)
	if err != nil {
		return nil, fmt.Errorf("build deployment: %w", err)
	}

	// Margo DesiredState layout (SPEC §4.1.5).
	deploymentProfile := yaml.MapSlice{
		{Key: "type", Value: get(profile, "type")},
		{Key: "components", Value: get(profile, "components")},
	}
	if dc, ok := lookup(profile, "deviceConstraints"); ok {
		deploymentProfile = append(deploymentProfile, yaml.MapItem{Key: "deviceConstraints", Value: dc})
	}
	deploymentProfile = append(deploymentProfile, extensions(profile)...)
	spec := yaml.MapSlice{
		{Key: "applicationId", Value: get(desc, "id")},
		{Key: "deploymentProfile", Value: deploymentProfile},
		{Key: "parameters", Value: params},
	}
	spec = append(spec, extensions(desc)...)
	doc := yaml.MapSlice{
		{Key: "id", Value: choices.ID.String()},
		{Key: "metadata", Value: yaml.MapSlice{
			{Key: "name", Value: choices.Name},
			{Key: "namespace", Value: choices.Namespace},
			{Key: "deviceId", Value: choices.DeviceID.String()},
		}},
		{Key: "spec", Value: spec},
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("build deployment: %w", err)
	}
	return b, nil
}

func findProfile(desc yaml.MapSlice, id string) (yaml.MapSlice, error) {
	profiles, _ := get(desc, "deploymentProfiles").([]any)
	for _, p := range profiles {
		if m, ok := p.(yaml.MapSlice); ok && get(m, "id") == id {
			return m, nil
		}
	}
	return nil, fmt.Errorf("no deployment profile %q", id)
}

// deploymentParameters returns spec.parameters: every parameter of the description, in its order,
// with the chosen value or else the description's (SPEC §4.1.5, §8.1.1). Always non-nil, so it
// encodes as `{}` when there are none.
func deploymentParameters(described any, values map[string]any) (yaml.MapSlice, error) {
	params, _ := described.(yaml.MapSlice)
	out := yaml.MapSlice{}
	known := map[string]bool{}
	for _, item := range params {
		name, _ := item.Key.(string)
		param, _ := item.Value.(yaml.MapSlice)
		known[name] = true
		value := values[name]
		if value == nil {
			value = get(param, "value")
		}
		if value == nil {
			return nil, fmt.Errorf("%w %q: required and has no value", ErrDeploymentParameter, name)
		}
		out = append(out, yaml.MapItem{Key: name, Value: yaml.MapSlice{
			{Key: "value", Value: value},
			{Key: "targets", Value: get(param, "targets")},
		}})
	}
	for _, name := range slices.Sorted(maps.Keys(values)) { // a stable first error
		if !known[name] {
			return nil, fmt.Errorf("%w %q: not defined by the application description", ErrDeploymentParameter, name)
		}
	}
	return out, nil
}

// extensionKeyPattern matches Margo vendor extension keys, `x-<name>-extensions` (SPEC §4.1.4).
const extensionKeyPattern = `^x-.+-extensions$`

var extensionKey = regexp.MustCompile(extensionKeyPattern)

// extensions returns the vendor extensions of m, in order.
func extensions(m yaml.MapSlice) yaml.MapSlice {
	var out yaml.MapSlice
	for _, item := range m {
		if k, ok := item.Key.(string); ok && extensionKey.MatchString(k) {
			out = append(out, item)
		}
	}
	return out
}

func lookup(m yaml.MapSlice, key string) (any, bool) {
	for _, item := range m {
		if item.Key == key {
			return item.Value, true
		}
	}
	return nil, false
}

func get(m yaml.MapSlice, key string) any {
	v, _ := lookup(m, key)
	return v
}

// DecodeApplicationDeployment decodes the YAML bytes of one desired deployment, as an LO holds
// them, into the deployment an Apply command carries (SPEC §8.5, §11.2). id is the deployment ID
// the State Manifest lists the bytes under.
//
// The YAML must be one document without aliases, like every YAML IEO reads (SPEC §5.3), and its
// content must pass the checks DecodeCommand applies to the `deployment` of an Apply for id (SPEC
// §8.9 step 2), so an LO never sends a deployment its ENs would refuse. The result holds the
// fields IEO reads; keys ApplicationDeployment does not name, vendor extensions among them, are
// not in it. Any failure wraps ErrInvalidDeployment and names the reason, and the returned
// deployment is the zero value.
func DecodeApplicationDeployment(id uuid.UUID, b []byte) (ApplicationDeployment, error) {
	d, err := decodeDeploymentYAML(id, b)
	if err != nil {
		return ApplicationDeployment{}, fmt.Errorf("%w: %w", ErrInvalidDeployment, err)
	}
	return d, nil
}

func decodeDeploymentYAML(id uuid.UUID, b []byte) (ApplicationDeployment, error) {
	if err := checkPlainYAML(b); err != nil {
		return ApplicationDeployment{}, err
	}
	js, err := yaml.YAMLToJSON(b)
	if err != nil {
		return ApplicationDeployment{}, err
	}
	raw, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		return ApplicationDeployment{}, err
	}
	return decodeDeployment(raw, id)
}
