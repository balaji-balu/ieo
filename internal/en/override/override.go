// Package override writes the EN's Compose override file, compose.ieo.yaml (SPEC §5.4, §9.3): the
// file that sets a deployment's parameter values and the OpenTelemetry variables in every
// container of a component. It owns the file's name, place and form.
package override

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/compose"
)

// File is the name of the override file, in the component's directory (SPEC §9.1).
const File = "compose.ieo.yaml"

// ErrInvalidParameter is returned, wrapped with the reason, when a deployment's parameters can't
// be set for the component: a variable name outside `[A-Za-z_][A-Za-z0-9_]*`, a value that is
// null, a list or an object, or two parameters that set one variable (SPEC §5.4). The EN reports
// it as IEO-COMPOSE-FAILED.
var ErrInvalidParameter = errors.New("invalid parameter")

// OTel is where workloads send telemetry (SPEC §9.3).
type OTel struct {
	// HTTPEndpoint is en.otel.http_endpoint, as seen from inside a container. REQUIRED.
	HTTPEndpoint string
	// GRPCEndpoint is en.otel.grpc_endpoint; empty when not configured.
	GRPCEndpoint string
}

// Write writes the override file for one component of deployment d and returns the component's
// Compose project, ready for compose.Runner: its name (SPEC §4.2), the archive's compose.yaml and
// then the override file, and the archive's top-level directory as project directory.
//
// dir is the component's directory and top the archive's top-level directory inside it, as
// archive.Extract returned it. The file is <dir>/compose.ieo.yaml, mode 0600, and replaces an
// earlier one. For every service r lists for the archive's compose.yaml it sets, under
// `environment:`, the parameter values that name component and the OpenTelemetry variables, which
// win over a parameter of the same name. The same input gives the same bytes.
//
// Parameters are checked before any Compose call: an error wrapping ErrInvalidParameter means r
// was not called. On any error no override file is written, and an earlier one is left as it was.
func Write(ctx context.Context, r compose.Runner, dir, top string, d contract.ApplicationDeployment, component string, otel OTel) (compose.Project, error) {
	p, err := write(ctx, r, dir, top, d, component, otel)
	if err != nil {
		return compose.Project{}, fmt.Errorf("write %s for component %q: %w", File, component, err)
	}
	return p, nil
}

func write(ctx context.Context, r compose.Runner, dir, top string, d contract.ApplicationDeployment, component string, otel OTel) (compose.Project, error) {
	env, err := environment(d, component, otel)
	if err != nil {
		return compose.Project{}, err
	}
	p := compose.Project{
		Name:  contract.ComposeProjectName(d.ID, component),
		Files: []string{filepath.Join(dir, top, archive.ComposeFile)},
		Dir:   filepath.Join(dir, top),
	}
	// SPEC §5.4: the services of the archive's compose.yaml alone, not of an earlier override.
	services, err := r.Services(ctx, p)
	if err != nil {
		return compose.Project{}, fmt.Errorf("list services: %w", err)
	}
	path := filepath.Join(dir, File)
	if err := replaceFile(path, render(services, env)); err != nil {
		return compose.Project{}, err
	}
	p.Files = append(p.Files, path)
	return p, nil
}

// variableName is the form of an environment variable name (SPEC §5.4).
var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// The OpenTelemetry variables (SPEC §9.3). A parameter never sets one of them: the EN's value is
// used, or none if the EN has none.
const (
	otelHTTPEndpoint = "HTTP_OTEL_EXPORTER_OTLP_ENDPOINT"
	otelProtocol     = "OTEL_EXPORTER_OTLP_PROTOCOL"
	otelGRPCEndpoint = "GRPC_OTEL_EXPORTER_OTLP_ENDPOINT"
	otelCertificate  = "OTEL_EXPORTER_OTLP_CERTIFICATE" // not injected in phase 1
)

// environment returns the variables to set in every container of component: the parameter values
// that name it, then the OpenTelemetry variables.
func environment(d contract.ApplicationDeployment, component string, otel OTel) (map[string]string, error) {
	if otel.HTTPEndpoint == "" {
		return nil, errors.New("en.otel.http_endpoint is not set")
	}
	env := map[string]string{}
	for _, parameter := range slices.Sorted(maps.Keys(d.Spec.Parameters)) { // sorted: the same error every time
		p := d.Spec.Parameters[parameter]
		for _, target := range p.Targets {
			if !slices.Contains(target.Components, component) {
				continue
			}
			name := target.Pointer
			if !variableName.MatchString(name) {
				return nil, fmt.Errorf("%w %q: %q is not an environment variable name", ErrInvalidParameter, parameter, name)
			}
			if _, set := env[name]; set {
				return nil, fmt.Errorf("%w %q: another parameter already sets %s", ErrInvalidParameter, parameter, name)
			}
			value, err := valueString(p.Value)
			if err != nil {
				return nil, fmt.Errorf("%w %q: %w", ErrInvalidParameter, parameter, err)
			}
			env[name] = value
		}
	}
	delete(env, otelGRPCEndpoint)
	delete(env, otelCertificate)
	env[otelHTTPEndpoint] = otel.HTTPEndpoint
	env[otelProtocol] = "http/protobuf"
	if otel.GRPCEndpoint != "" {
		env[otelGRPCEndpoint] = otel.GRPCEndpoint
	}
	return env, nil
}

// valueString returns a parameter value as the environment holds it (SPEC §5.4). The value is as
// encoding/json decodes it.
func valueString(v any) (string, error) {
	switch v := v.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case nil:
		return "", errors.New("the value is null")
	default:
		return "", fmt.Errorf("the value is a %T, not a string, number or boolean", v)
	}
}

// render returns the override file: env under `environment:` of every service, names sorted.
func render(services []string, env map[string]string) []byte {
	var b strings.Builder
	b.WriteString("# Written by the IEO edge node agent on every Apply (SPEC §5.4). Changes are lost.\n")
	if len(services) == 0 {
		b.WriteString("services: {}\n")
		return []byte(b.String())
	}
	b.WriteString("services:\n")
	names := slices.Sorted(maps.Keys(env))
	for _, service := range services {
		b.WriteString("  " + quote(service) + ":\n    environment:\n")
		for _, name := range names {
			// `$$` is how a Compose file writes one `$`; a single one would be interpolated.
			b.WriteString("      " + quote(name) + ": " + quote(strings.ReplaceAll(env[name], "$", "$$")) + "\n")
		}
	}
	return []byte(b.String())
}

// quote returns s as a YAML double-quoted scalar, which every YAML reader takes as that string:
// never as a number, a boolean, null or more structure.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02X`, r)
		// C1 controls, the Unicode line breaks YAML folds, and the byte order mark.
		case r >= 0x80 && r <= 0x9f, r == 0x2028, r == 0x2029, r == 0xfeff:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// replaceFile writes content to path with mode 0600, through a temporary file beside it, so path
// holds the old file or the whole new one.
func replaceFile(path string, content []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*") // mode 0600
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name()) // the write has failed; its error is the one to report
		}
	}()
	_, err = tmp.Write(content)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
