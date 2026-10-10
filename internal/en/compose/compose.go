// Package compose runs a component's Compose project on the host's container engine (SPEC §8.9,
// §16.6). All runtime calls of the EN go through Runner (ADR 0003); CLI implements it with the
// `docker compose` or `podman compose` command (ADR 0017), and composetest has a fake for tests.
package compose

import (
	"context"
	"errors"
	"time"
)

// ErrStartTimeout reports that a project's containers were not all running when its wait timeout
// elapsed (SPEC §7.1, §8.9 step 4.5).
var ErrStartTimeout = errors.New("containers not running before the timeout")

// Project is one component's Compose project.
type Project struct {
	// Name is the Compose project name, derived only from the deployment ID and component name
	// (contract.ComposeProjectName, SPEC §4.2, §9.2).
	Name string
	// Files are the Compose files, in the order Compose merges them: the archive's compose.yaml,
	// then the EN's compose.ieo.yaml (SPEC §5.4).
	Files []string
	// Dir is the project directory, against which relative paths in the files resolve: the
	// archive's top-level directory.
	Dir string
}

// UpOptions say whether Up waits for the project's containers (SPEC §8.9 step 4.5).
type UpOptions struct {
	// Wait makes Up return only once every container is running.
	Wait bool
	// Timeout bounds the whole of Up when Wait is set; 0 means no bound. It is ignored without
	// Wait.
	Timeout time.Duration
}

// Runner runs Compose projects. Every call is bounded by its context.
type Runner interface {
	// Services returns the names of the services the project's files define, in the order
	// Compose prints them.
	Services(ctx context.Context, p Project) ([]string, error)
	// Up creates and starts the project's containers in the background. With o.Wait it returns
	// ErrStartTimeout (wrapped) if they are not all running within o.Timeout.
	Up(ctx context.Context, p Project, o UpOptions) error
	// Down stops and removes the containers and networks of the named project; its volumes are
	// kept. Down of a project that does not exist succeeds (SPEC §7.6).
	Down(ctx context.Context, project string) error
}
