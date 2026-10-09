package composetest_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/balaji-balu/ieo/internal/en/compose"
	"github.com/balaji-balu/ieo/internal/en/compose/composetest"
)

func project(t *testing.T, name, composeYAML string) compose.Project {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte(composeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return compose.Project{Name: name, Files: []string{file}, Dir: dir}
}

// The fake keeps the projects that are up and records calls in order.
func TestFakeUpDown(t *testing.T) {
	ctx := context.Background()
	f := composetest.New()
	a := project(t, "a", "services: {web: {image: x}}\n")
	b := project(t, "b", "services: {db: {image: y}}\n")
	opts := compose.UpOptions{Wait: true, Timeout: time.Minute}

	for _, err := range []error{
		f.Up(ctx, b, opts),
		f.Up(ctx, a, compose.UpOptions{}),
		f.Up(ctx, a, compose.UpOptions{}), // already up: still succeeds
		f.Down(ctx, b.Name),
		f.Down(ctx, "unknown"), // not up: succeeds (SPEC §7.6)
		f.Up(ctx, b, opts),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, want := f.Projects(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Projects = %q, want %q", got, want)
	}
	want := []composetest.Call{
		{Op: composetest.OpUp, Project: "b", Options: opts},
		{Op: composetest.OpUp, Project: "a"},
		{Op: composetest.OpUp, Project: "a"},
		{Op: composetest.OpDown, Project: "b"},
		{Op: composetest.OpDown, Project: "unknown"},
		{Op: composetest.OpUp, Project: "b", Options: opts},
	}
	if got := f.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("Calls = %+v, want %+v", got, want)
	}
}

// Services reads the services of the project's files in order of first appearance.
func TestFakeServices(t *testing.T) {
	p := project(t, "a", "services:\n  web: {image: x}\n  cache: {image: z}\n")
	extra := filepath.Join(p.Dir, "compose.ieo.yaml")
	if err := os.WriteFile(extra, []byte("services:\n  web: {environment: {A: \"1\"}}\n  db: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.Files = append(p.Files, extra)
	got, err := composetest.New().Services(context.Background(), p)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if want := []string{"web", "cache", "db"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Services = %q, want %q", got, want)
	}

	bad := project(t, "b", "services: [\n")
	if _, err := composetest.New().Services(context.Background(), bad); err == nil {
		t.Error("Services of invalid YAML succeeded, want an error")
	}
}

// Fail scripts an error per op and project; a failed Up leaves the project present.
func TestFakeFail(t *testing.T) {
	ctx := context.Background()
	f := composetest.New()
	a := project(t, "a", "services: {web: {image: x}}\n")
	f.Fail(composetest.OpUp, "a", compose.ErrStartTimeout)
	f.Fail(composetest.OpDown, "a", errors.New("engine gone"))

	if err := f.Up(ctx, a, compose.UpOptions{Wait: true}); !errors.Is(err, compose.ErrStartTimeout) {
		t.Errorf("Up = %v, want ErrStartTimeout", err)
	}
	if got := f.Projects(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Projects after failed Up = %q, want [a]", got)
	}
	if err := f.Down(ctx, "a"); err == nil {
		t.Error("Down succeeded, want the scripted error")
	}
	f.Fail(composetest.OpDown, "a", nil)
	if err := f.Down(ctx, "a"); err != nil {
		t.Errorf("Down after clearing the failure = %v", err)
	}
	if got := f.Projects(); len(got) != 0 {
		t.Errorf("Projects = %q, want none", got)
	}
}

// A done context fails the call, which changes nothing and is not recorded.
func TestFakeHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := composetest.New()
	if err := f.Up(ctx, project(t, "a", "services: {}\n"), compose.UpOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Up = %v, want context.Canceled", err)
	}
	if len(f.Projects()) != 0 || len(f.Calls()) != 0 {
		t.Errorf("Projects = %q, Calls = %+v, want none", f.Projects(), f.Calls())
	}
}
