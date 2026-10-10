package override_test

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/compose"
	"github.com/balaji-balu/ieo/internal/en/compose/composetest"
	"github.com/balaji-balu/ieo/internal/en/override"
)

const (
	top = "app"
	// Two services, so "every service" is not "the service".
	composeYAML = "services:\n  web:\n    image: nginx\n    environment:\n      GREETING: from-archive\n  sidecar:\n    image: busybox\n"
)

var (
	deploymentID = uuid.MustParse("6f1d7d3e-9d0c-4a39-8f5d-1b8f0c9f2a11")
	otel         = override.OTel{HTTPEndpoint: "http://otel.internal:4318"}
	// What otel alone puts in every service (SPEC §9.3).
	otelVariables = map[string]string{
		"HTTP_OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel.internal:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL":      "http/protobuf",
	}
)

// componentDir returns a component's directory holding an extracted archive: <dir>/app/compose.yaml.
func componentDir(t *testing.T, compose string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(filepath.Join(dir, top), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, top, "compose.yaml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// deployment returns a deployment with the components web and db and the given parameters.
func deployment(parameters map[string]contract.ParameterValue) contract.ApplicationDeployment {
	return contract.ApplicationDeployment{
		ID: deploymentID,
		Spec: contract.DeploymentSpec{
			DeploymentProfile: contract.DeploymentProfile{Type: "compose", Components: []contract.Component{{Name: "web"}, {Name: "db"}}},
			Parameters:        parameters,
		},
	}
}

// param is a parameter with one target: the variable name, set in components.
func param(value any, name string, components ...string) contract.ParameterValue {
	return contract.ParameterValue{Value: value, Targets: []contract.ParameterTarget{{Pointer: name, Components: components}}}
}

// environments reads the override file as Compose would before interpolation: service →
// variable → value. It fails the test if the file holds anything but `services.<name>.environment`.
func environments(t *testing.T, dir string) map[string]map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, override.File))
	if err != nil {
		t.Fatalf("read override file: %v", err)
	}
	var doc map[string]map[string]map[string]map[string]string
	if err := yaml.UnmarshalWithOptions(b, &doc, yaml.Strict()); err != nil {
		t.Fatalf("override file is not the YAML expected: %v\n%s", err, b)
	}
	if len(doc) != 1 || doc["services"] == nil {
		t.Fatalf("override file has top-level keys other than services:\n%s", b)
	}
	out := map[string]map[string]string{}
	for service, keys := range doc["services"] {
		if len(keys) != 1 || keys["environment"] == nil {
			t.Fatalf("service %q has keys other than environment:\n%s", service, b)
		}
		out[service] = keys["environment"]
	}
	return out
}

// with returns the OpenTelemetry variables plus more.
func with(more map[string]string) map[string]string {
	out := maps.Clone(otelVariables)
	maps.Copy(out, more)
	return out
}

func assertEnvironments(t *testing.T, dir string, want map[string]string, services ...string) {
	t.Helper()
	got := environments(t, dir)
	if len(got) != len(services) {
		t.Errorf("override file sets %d services, want %v", len(got), services)
	}
	for _, service := range services {
		if !maps.Equal(got[service], want) {
			t.Errorf("service %q environment =\n  %v\nwant\n  %v", service, got[service], want)
		}
	}
}

func assertNoFile(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, override.File)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("override file exists (Lstat error: %v)", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("%d entries in the component's directory, want only the archive's", len(entries))
	}
}

// SPEC §17.6: "Parameter values appear as environment variables only in the listed components."
// Checked in the override file of each component; containers are the site profile's (ADR 0003).
func TestSpec_17_6_ParametersOnlyInListedComponents(t *testing.T) {
	d := deployment(map[string]contract.ParameterValue{
		"greeting": param("hello", "GREETING", "web"),
		"region":   param("eu-1", "REGION", "web", "db"),
		"dbUser":   param("app", "DB_USER", "db"),
		"both": {Value: "x", Targets: []contract.ParameterTarget{
			{Pointer: "WEB_NAME", Components: []string{"web"}},
			{Pointer: "DB_NAME", Components: []string{"db"}},
		}},
		"unused": {Value: "y"},
	})
	want := map[string]map[string]string{
		"web": with(map[string]string{"GREETING": "hello", "REGION": "eu-1", "WEB_NAME": "x"}),
		"db":  with(map[string]string{"REGION": "eu-1", "DB_USER": "app", "DB_NAME": "x"}),
	}
	for component, env := range want {
		t.Run(component, func(t *testing.T) {
			dir := componentDir(t, composeYAML)
			if _, err := override.Write(context.Background(), composetest.New(), dir, top, d, component, otel); err != nil {
				t.Fatalf("Write: %v", err)
			}
			assertEnvironments(t, dir, env, "web", "sidecar")
		})
	}
}

// SPEC §17.6: "Margo OpenTelemetry variables are present in every container." Checked in the
// override file, which sets them for every service (SPEC §9.3).
func TestSpec_17_6_OTelVariables(t *testing.T) {
	both := override.OTel{HTTPEndpoint: "http://otel.internal:4318", GRPCEndpoint: "http://otel.internal:4317"}
	cases := []struct {
		name       string
		otel       override.OTel
		parameters map[string]contract.ParameterValue
		want       map[string]string
	}{
		{"no parameters", otel, nil, otelVariables},
		{"gRPC endpoint configured", both, nil, with(map[string]string{"GRPC_OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel.internal:4317"})},
		{"parameters with the variables' names", both, map[string]contract.ParameterValue{
			"a": param("http://evil.example", "HTTP_OTEL_EXPORTER_OTLP_ENDPOINT", "web"),
			"b": param("grpc", "OTEL_EXPORTER_OTLP_PROTOCOL", "web"),
			"c": param("http://evil.example", "GRPC_OTEL_EXPORTER_OTLP_ENDPOINT", "web"),
			"d": param("kept", "OTHER", "web"),
		}, with(map[string]string{"GRPC_OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel.internal:4317", "OTHER": "kept"})},
		// The EN has no value for these two, so the parameter's is not set either.
		{"parameters naming variables the EN does not set", otel, map[string]contract.ParameterValue{
			"c": param("http://evil.example", "GRPC_OTEL_EXPORTER_OTLP_ENDPOINT", "web"),
			"e": param("/certs/ca.pem", "OTEL_EXPORTER_OTLP_CERTIFICATE", "web"),
		}, otelVariables},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := componentDir(t, composeYAML)
			if _, err := override.Write(context.Background(), composetest.New(), dir, top, deployment(c.parameters), "web", c.otel); err != nil {
				t.Fatalf("Write: %v", err)
			}
			assertEnvironments(t, dir, c.want, "web", "sidecar")
		})
	}
}

// A value is set exactly, whatever characters it holds: quoted for YAML, and with every `$`
// doubled so Compose does not interpolate it (SPEC §5.4).
func TestWriteValuesAreExact(t *testing.T) {
	values := map[string]string{
		"PLAIN":      "hello world",
		"EMPTY":      "",
		"DOLLAR":     "cost: $5 and ${HOME} and $$",
		"QUOTES":     `she said "hi" and 'bye'`,
		"BACKSLASH":  `C:\temp\new`,
		"NEWLINES":   "line one\nline two\r\n\ttabbed",
		"YAML":       "key: value # not a comment\n- item",
		"LEADING":    "  padded  ",
		"LOOKS_BOOL": "yes",
		"LOOKS_NULL": "~",
		"UNICODE":    "grüße 世界 \u2028 \u0085 \ufeff",
		"CONTROL":    "bell\a escape\x1b del\x7f",
	}
	parameters := map[string]contract.ParameterValue{}
	for name, v := range values {
		parameters[strings.ToLower(name)] = param(v, name, "web")
	}
	dir := componentDir(t, composeYAML)
	if _, err := override.Write(context.Background(), composetest.New(), dir, top, deployment(parameters), "web", otel); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, override.File))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(string(raw), "$$", ""), "$") {
		t.Errorf("override file holds a single $, which Compose would interpolate:\n%s", raw)
	}
	got := environments(t, dir)["web"]
	for name, want := range values {
		// What Compose passes to the container: `$$` read as `$`.
		if v := strings.ReplaceAll(got[name], "$$", "$"); v != want {
			t.Errorf("%s = %q, want %q", name, v, want)
		}
	}
}

// Values that are not strings are written as SPEC §5.4 says.
func TestWriteValueTypes(t *testing.T) {
	dir := componentDir(t, composeYAML)
	d := deployment(map[string]contract.ParameterValue{
		"a": param(true, "ENABLED", "web"),
		"b": param(false, "DEBUG", "web"),
		"c": param(float64(8080), "PORT", "web"), // JSON numbers decode to float64
		"d": param(1.5, "RATIO", "web"),
		"e": param(float64(-3), "OFFSET", "web"),
		"f": param(float64(12345678901), "BIG", "web"),
	})
	if _, err := override.Write(context.Background(), composetest.New(), dir, top, d, "web", otel); err != nil {
		t.Fatalf("Write: %v", err)
	}
	assertEnvironments(t, dir, with(map[string]string{
		"ENABLED": "true", "DEBUG": "false", "PORT": "8080", "RATIO": "1.5", "OFFSET": "-3", "BIG": "12345678901",
	}), "web", "sidecar")
}

// Parameters that can't be set fail before any Compose command runs, and write nothing (SPEC §5.4).
func TestWriteInvalidParameters(t *testing.T) {
	cases := map[string]map[string]contract.ParameterValue{
		"name starts with a digit": {"p": param("v", "1ST", "web")},
		"name with a dash":         {"p": param("v", "MY-VAR", "web")},
		"name with an equals sign": {"p": param("v", "A=B", "web")},
		"name with a space":        {"p": param("v", "MY VAR", "web")},
		"name with a JSON pointer": {"p": param("v", "/services/web/image", "web")},
		"empty name":               {"p": param("v", "", "web")},
		"null value":               {"p": param(nil, "NAME", "web")},
		"list value":               {"p": param([]any{"a"}, "NAME", "web")},
		"object value":             {"p": param(map[string]any{"a": "b"}, "NAME", "web")},
		"two parameters, one variable": {
			"p": param("one", "NAME", "web"),
			"q": param("two", "NAME", "web", "db"),
		},
	}
	for name, parameters := range cases {
		t.Run(name, func(t *testing.T) {
			dir := componentDir(t, composeYAML)
			runner := composetest.New()
			_, err := override.Write(context.Background(), runner, dir, top, deployment(parameters), "web", otel)
			if !errors.Is(err, override.ErrInvalidParameter) {
				t.Errorf("error = %v, want one wrapping override.ErrInvalidParameter", err)
			}
			if calls := runner.Calls(); len(calls) != 0 {
				t.Errorf("Compose was called before the parameters were checked: %v", calls)
			}
			assertNoFile(t, dir)
		})
	}
}

// A parameter that can't be set fails only the components it lists (SPEC §5.4).
func TestWriteInvalidParameterOfAnotherComponent(t *testing.T) {
	dir := componentDir(t, composeYAML)
	d := deployment(map[string]contract.ParameterValue{
		"bad":  param("v", "MY-VAR", "db"),
		"null": param(nil, "NAME", "db"),
		"good": param("v", "GOOD", "web"),
	})
	if _, err := override.Write(context.Background(), composetest.New(), dir, top, d, "web", otel); err != nil {
		t.Fatalf("Write: %v", err)
	}
	assertEnvironments(t, dir, with(map[string]string{"GOOD": "v"}), "web", "sidecar")
}

// Write returns the project compose.Runner is given: the §4.2 name, compose.yaml then the
// override file, and the archive's top-level directory (SPEC §5.4 step 2, §9.1).
func TestWriteReturnsProject(t *testing.T) {
	dir := componentDir(t, composeYAML)
	got, err := override.Write(context.Background(), composetest.New(), dir, top, deployment(nil), "web", otel)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := compose.Project{
		Name:  contract.ComposeProjectName(deploymentID, "web"),
		Files: []string{filepath.Join(dir, top, "compose.yaml"), filepath.Join(dir, override.File)},
		Dir:   filepath.Join(dir, top),
	}
	if got.Name != want.Name || got.Dir != want.Dir || strings.Join(got.Files, "|") != strings.Join(want.Files, "|") {
		t.Errorf("project = %+v, want %+v", got, want)
	}
	if runtime.GOOS != "windows" { // on Windows the directory's ACL governs (ADR 0016)
		fi, err := os.Stat(filepath.Join(dir, override.File))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != 0o600 {
			t.Errorf("override file has mode %v, want 0600", fi.Mode())
		}
	}
}

// A second Write replaces the file, lists the services of the archive's compose.yaml only, and
// gives the same bytes for the same input.
func TestWriteReplacesAndRepeats(t *testing.T) {
	dir := componentDir(t, composeYAML)
	stale := "services:\n  ghost:\n    environment:\n      OLD: \"1\"\n"
	if err := os.WriteFile(filepath.Join(dir, override.File), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	d := deployment(map[string]contract.ParameterValue{
		"b": param("2", "B", "web"), "a": param("1", "A", "web"), "c": param("3", "C", "web"),
	})
	var first []byte
	for i := range 20 { // map order differs between runs
		if _, err := override.Write(context.Background(), composetest.New(), dir, top, d, "web", otel); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		b, err := os.ReadFile(filepath.Join(dir, override.File))
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = b
		} else if string(b) != string(first) {
			t.Fatalf("Write %d gave other bytes:\n%s\nfirst:\n%s", i, b, first)
		}
	}
	assertEnvironments(t, dir, with(map[string]string{"A": "1", "B": "2", "C": "3"}), "web", "sidecar")
}

// Service names are quoted like values, and an archive without services gets an empty override.
func TestWriteServiceNames(t *testing.T) {
	t.Run("names YAML could misread", func(t *testing.T) {
		dir := componentDir(t, "services:\n  \"yes\":\n    image: a\n  \"123\":\n    image: b\n  web.api-1_x:\n    image: c\n")
		if _, err := override.Write(context.Background(), composetest.New(), dir, top, deployment(nil), "web", otel); err != nil {
			t.Fatalf("Write: %v", err)
		}
		assertEnvironments(t, dir, otelVariables, "yes", "123", "web.api-1_x")
	})
	t.Run("no services", func(t *testing.T) {
		dir := componentDir(t, "services: {}\n")
		if _, err := override.Write(context.Background(), composetest.New(), dir, top, deployment(nil), "web", otel); err != nil {
			t.Fatalf("Write: %v", err)
		}
		assertEnvironments(t, dir, nil)
	})
}

// A Compose failure, or a missing OpenTelemetry endpoint, fails Write and leaves no file.
func TestWriteFailures(t *testing.T) {
	t.Run("compose cannot list services", func(t *testing.T) {
		dir := componentDir(t, composeYAML)
		runner := composetest.New()
		errCompose := errors.New("compose.yaml is not valid")
		runner.Fail(composetest.OpServices, contract.ComposeProjectName(deploymentID, "web"), errCompose)

		_, err := override.Write(context.Background(), runner, dir, top, deployment(nil), "web", otel)
		if !errors.Is(err, errCompose) {
			t.Errorf("error = %v, want the runner's", err)
		}
		if errors.Is(err, override.ErrInvalidParameter) {
			t.Errorf("error %v wraps ErrInvalidParameter; the parameters were not at fault", err)
		}
		assertNoFile(t, dir)
	})
	t.Run("no HTTP endpoint", func(t *testing.T) {
		dir := componentDir(t, composeYAML)
		if _, err := override.Write(context.Background(), composetest.New(), dir, top, deployment(nil), "web", override.OTel{}); err == nil {
			t.Error("Write succeeded without en.otel.http_endpoint")
		}
		assertNoFile(t, dir)
	})
	t.Run("context cancelled", func(t *testing.T) {
		dir := componentDir(t, composeYAML)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := override.Write(ctx, composetest.New(), dir, top, deployment(nil), "web", otel)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		assertNoFile(t, dir)
	})
}
