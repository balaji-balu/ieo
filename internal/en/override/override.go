// Package override writes the EN's Compose override file, compose.ieo.yaml (SPEC §5.4, §9.3): the
// file that sets a deployment's parameter values and the OpenTelemetry variables in every
// container of a component. It owns the file's name, place and form.
package override

import (
	"context"
	"errors"

	"github.com/balaji-balu/ieo/internal/contract"
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
	return compose.Project{}, errors.New("override: Write is not implemented")
}
