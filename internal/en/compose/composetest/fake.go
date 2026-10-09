// Package composetest provides a fake container runtime for EN tests (ADR 0003, G-F4).
package composetest

import (
	"context"
	"errors"

	"github.com/balaji-balu/ieo/internal/en/compose"
)

// Ops of Call and Fail.
const (
	OpServices = "services"
	OpUp       = "up"
	OpDown     = "down"
)

// Call is one call the fake received.
type Call struct {
	Op      string
	Project string
	Options compose.UpOptions // Up only
}

// Fake is an in-memory compose.Runner. It keeps the set of projects that are up, records every
// call in order, and fails the calls a test scripts with Fail. It is safe for concurrent use.
//
// Services reads the `services` keys of the project's files, so a test works with the archives
// it gives the EN. A failed Up leaves the project present, as Compose may have created its
// containers before it failed; Down removes it.
type Fake struct{}

var _ compose.Runner = (*Fake)(nil)

// New returns a fake with no project up.
func New() *Fake { return &Fake{} }

// Fail makes every later op on the named project return err, until Fail is called again for
// that op and project; a nil err clears the failure. Use compose.ErrStartTimeout to fake a
// start timeout.
func (f *Fake) Fail(op, project string, err error) {}

// Projects returns the names of the projects that are up, sorted.
func (f *Fake) Projects() []string { return nil }

// Calls returns the calls received so far, oldest first. Calls made with a done context are not
// recorded.
func (f *Fake) Calls() []Call { return nil }

// Services returns the services the project's files define, in the order they first appear.
func (f *Fake) Services(ctx context.Context, p compose.Project) ([]string, error) {
	return nil, errors.New("not implemented")
}

// Up brings the project up.
func (f *Fake) Up(ctx context.Context, p compose.Project, o compose.UpOptions) error {
	return errors.New("not implemented")
}

// Down brings the project down; a project that is not up succeeds.
func (f *Fake) Down(ctx context.Context, project string) error {
	return errors.New("not implemented")
}
