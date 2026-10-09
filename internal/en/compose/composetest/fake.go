// Package composetest provides a fake container runtime for EN tests (ADR 0003, G-F4).
package composetest

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/goccy/go-yaml"

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
type Fake struct {
	mu       sync.Mutex
	up       map[string]bool
	calls    []Call
	failures map[[2]string]error // (op, project) → error
}

var _ compose.Runner = (*Fake)(nil)

// New returns a fake with no project up.
func New() *Fake {
	return &Fake{up: map[string]bool{}, failures: map[[2]string]error{}}
}

// Fail makes every later op on the named project return err, until Fail is called again for
// that op and project; a nil err clears the failure. Use compose.ErrStartTimeout to fake a
// start timeout.
func (f *Fake) Fail(op, project string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.failures, [2]string{op, project})
		return
	}
	f.failures[[2]string{op, project}] = err
}

// Projects returns the names of the projects that are up, sorted.
func (f *Fake) Projects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.up {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Calls returns the calls received so far, oldest first. Calls made with a done context are not
// recorded.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// record records a call and returns the error scripted for it.
func (f *Fake) record(ctx context.Context, c Call) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if err := f.failures[[2]string{c.Op, c.Project}]; err != nil {
		return fmt.Errorf("fake compose %s %s: %w", c.Op, c.Project, err)
	}
	return nil
}

// Services returns the services the project's files define, in the order they first appear.
func (f *Fake) Services(ctx context.Context, p compose.Project) ([]string, error) {
	if err := f.record(ctx, Call{Op: OpServices, Project: p.Name}); err != nil {
		return nil, err
	}
	var services []string
	for _, file := range p.Files {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Services yaml.MapSlice `yaml:"services"` // keeps the file's order
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for _, item := range doc.Services {
			if name := fmt.Sprint(item.Key); !slices.Contains(services, name) {
				services = append(services, name)
			}
		}
	}
	return services, nil
}

// Up brings the project up.
func (f *Fake) Up(ctx context.Context, p compose.Project, o compose.UpOptions) error {
	err := f.record(ctx, Call{Op: OpUp, Project: p.Name, Options: o})
	if ctx.Err() == nil {
		f.mu.Lock()
		f.up[p.Name] = true
		f.mu.Unlock()
	}
	return err
}

// Down brings the project down; a project that is not up succeeds.
func (f *Fake) Down(ctx context.Context, project string) error {
	if err := f.record(ctx, Call{Op: OpDown, Project: project}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.up, project)
	return nil
}
