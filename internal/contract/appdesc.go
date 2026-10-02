package contract

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/balaji-balu/ieo/api/margo"
)

// Media types of a Margo application package in an OCI registry (SPEC §5.1).
const (
	// AppPackageArtifactType is the artifactType of an application package's image manifest.
	AppPackageArtifactType = "application/vnd.margo.app.v1+json"
	// AppDescriptionMediaType is the media type of the layer holding the Application Description.
	AppDescriptionMediaType = "application/vnd.margo.app.description.v1+yaml"
)

// ProfileTypeCompose is the deployment profile type IEO runs in phase 1 (SPEC §4.1.4).
const ProfileTypeCompose = "compose"

// ErrInvalidApplicationDescription is returned, wrapped with the reason, for an Application
// Description that is not YAML or fails the pinned Margo schema (SPEC §5.3).
var ErrInvalidApplicationDescription = errors.New("invalid application description")

// ApplicationDescription holds the fields of a Margo Application Description (`margo.yaml`) that
// IEO reads (SPEC §4.1.4). It is a read-only view: the exact bytes, which carry vendor extensions
// and everything else, are kept by whoever stores the description, never re-encoded from this type.
type ApplicationDescription struct {
	ID                 string                          `yaml:"id"`
	Metadata           ApplicationMetadata             `yaml:"metadata"`
	DeploymentProfiles []ApplicationDeploymentProfile  `yaml:"deploymentProfiles"`
	Parameters         map[string]ApplicationParameter `yaml:"parameters"`
}

// ApplicationMetadata names an application version. Version equals the registry tag of its
// package (SPEC §5.1).
type ApplicationMetadata struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// ApplicationDeploymentProfile is one way to deploy the application: Type is `compose`, `helm` or
// `custom`, and Components are installed in order.
type ApplicationDeploymentProfile struct {
	ID         string      `yaml:"id"`
	Type       string      `yaml:"type"`
	Components []Component `yaml:"components"`
}

// ApplicationParameter is a configurable parameter: its default Value and the components it is
// written to.
type ApplicationParameter struct {
	Value   any               `yaml:"value"`
	Targets []ParameterTarget `yaml:"targets"`
}

// ParseApplicationDescription validates b against the pinned Margo Application Description
// schema and returns the fields IEO reads. Keys match by exact name. Any failure wraps
// ErrInvalidApplicationDescription and names the violations.
func ParseApplicationDescription(b []byte) (ApplicationDescription, error) {
	var desc ApplicationDescription
	if err := checkPlainYAML(b); err != nil {
		return desc, fmt.Errorf("%w: %w", ErrInvalidApplicationDescription, err)
	}
	js, err := yaml.YAMLToJSON(b)
	if err != nil {
		return desc, fmt.Errorf("%w: %w", ErrInvalidApplicationDescription, err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		return desc, fmt.Errorf("%w: %w", ErrInvalidApplicationDescription, err)
	}
	if err := appDescriptionSchema().Validate(v); err != nil {
		return desc, fmt.Errorf("%w: %s", ErrInvalidApplicationDescription, schemaViolations(err))
	}
	// goccy/go-yaml matches keys by exact name, so a key differing only in case is never read. It
	// falls back to `json` tags, which the shared types (Component, ParameterTarget) carry.
	if err := yaml.Unmarshal(b, &desc); err != nil {
		return ApplicationDescription{}, fmt.Errorf("%w: %w", ErrInvalidApplicationDescription, err)
	}
	return desc, nil
}

// checkPlainYAML rejects input that isn't exactly one YAML document, or that uses aliases (SPEC
// §5.3). The description comes from a registry and is untrusted: a few hundred bytes of nested
// aliases expand to gigabytes. Parsing to the AST doesn't expand them.
func checkPlainYAML(b []byte) error {
	f, err := parser.ParseBytes(b, 0)
	if err != nil {
		return err
	}
	if len(f.Docs) != 1 {
		return fmt.Errorf("want one YAML document, found %d", len(f.Docs))
	}
	if aliases := ast.Filter(ast.AliasType, f.Docs[0]); len(aliases) > 0 {
		pos := aliases[0].GetToken().Position
		return fmt.Errorf("YAML alias at line %d, column %d is not allowed", pos.Line, pos.Column)
	}
	return nil
}

const appDescriptionSchemaURL = "https://margo.invalid/application-description.schema.json"

// appDescriptionSchema compiles the pinned schema once, with the in-memory workarounds listed in
// api/margo/f209a7f/README.md. The schema is fixed at build time and covered by tests, so a
// failure here is a programmer error (G-B4).
var appDescriptionSchema = sync.OnceValue(func() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(margo.ApplicationDescriptionSchema))
	if err != nil {
		panic(fmt.Sprintf("application description schema: %v", err))
	}
	fixGeneratedSchema(doc)
	c := jsonschema.NewCompiler()
	if err := c.AddResource(appDescriptionSchemaURL, doc); err != nil {
		panic(fmt.Sprintf("application description schema: %v", err))
	}
	return c.MustCompile(appDescriptionSchemaURL)
})

// validationSchemaClasses are the LinkML subclasses of the Application Description's `Schema`
// class, emitted by the generator as separate definitions.
var validationSchemaClasses = []string{
	"TextValidationSchema", "BooleanValidationSchema", "NumericIntegerValidationSchema",
	"NumericDoubleValidationSchema", "SelectValidationSchema",
}

// fixGeneratedSchema works around the generator defects in api/margo/f209a7f/README.md, walking
// the whole document:
//   - the literal property `x_placeholder_extensions` becomes a pattern for every
//     `x-<name>-extensions` key, whose value is an object;
//   - a reference to the closed base `Schema` class accepts any of its subclasses.
func fixGeneratedSchema(node any) {
	switch n := node.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			if _, ok := props["x_placeholder_extensions"]; ok {
				delete(props, "x_placeholder_extensions")
				patterns, _ := n["patternProperties"].(map[string]any)
				if patterns == nil {
					patterns = map[string]any{}
					n["patternProperties"] = patterns
				}
				patterns[extensionKeyPattern] = map[string]any{"type": "object"}
			}
		}
		if n["$ref"] == "#/$defs/Schema" {
			delete(n, "$ref")
			var anyOf []any
			for _, class := range validationSchemaClasses {
				anyOf = append(anyOf, map[string]any{"$ref": "#/$defs/" + class})
			}
			n["anyOf"] = anyOf
		}
		for _, v := range n {
			fixGeneratedSchema(v)
		}
	case []any:
		for _, v := range n {
			fixGeneratedSchema(v)
		}
	}
}
