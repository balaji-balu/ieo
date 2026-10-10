package exec_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/archive/archivetest"
	"github.com/balaji-balu/ieo/internal/en/compose"
	"github.com/balaji-balu/ieo/internal/en/compose/composetest"
	"github.com/balaji-balu/ieo/internal/en/exec"
	"github.com/balaji-balu/ieo/internal/en/override"
	"github.com/balaji-balu/ieo/internal/en/pull"
	"github.com/balaji-balu/ieo/internal/en/store"
	"github.com/balaji-balu/ieo/internal/ocitest"
	"github.com/balaji-balu/ieo/internal/platform/platformtest"
)

const revision = "1.0.0"

var (
	deploymentID = uuid.MustParse("6f1d7d3e-9d0c-4a39-8f5d-1b8f0c9f2a11")
	digestA      = contract.DigestOf([]byte("deployment A"))
	digestB      = contract.DigestOf([]byte("deployment B"))
	start        = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	startTimeout = 7 * time.Minute
	ctx          = context.Background()
)

// recorder is the Publisher: it keeps the events, and checks each is one the LO would accept.
type recorder struct {
	t      *testing.T
	mu     sync.Mutex
	events []contract.ComponentStatusEvent
	err    error // returned by every Publish
}

func (r *recorder) Publish(_ context.Context, e contract.ComponentStatusEvent) error {
	r.t.Helper()
	if _, err := contract.EncodeSiteMessage(e); err != nil {
		r.t.Errorf("published an event the LO would drop: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return r.err
}

// take returns the events published since the last take, as "component state code".
func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		s := strings.TrimSpace(e.Component + " " + string(e.State))
		if e.Error != nil {
			s += " " + e.Error.Code
		}
		out = append(out, s)
	}
	r.events = nil
	return out
}

func (r *recorder) all() []contract.ComponentStatusEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contract.ComponentStatusEvent(nil), r.events...)
}

// hookedRunner is the fake runtime with a hook before Up, for tests that look at the EN's record
// at that moment or end the context there.
type hookedRunner struct {
	*composetest.Fake
	beforeUp func(p compose.Project) error
}

func (h *hookedRunner) Up(ctx context.Context, p compose.Project, o compose.UpOptions) error {
	if h.beforeUp != nil {
		if err := h.beforeUp(p); err != nil {
			return err
		}
	}
	return h.Fake.Up(ctx, p, o)
}

// failingStore is the EN store with writes that can be made to fail.
type failingStore struct {
	*store.Bolt
	putApplied error
}

func (s *failingStore) PutApplied(ctx context.Context, id uuid.UUID, a store.Applied) error {
	if s.putApplied != nil {
		return s.putApplied
	}
	return s.Bolt.PutApplied(ctx, id, a)
}

type fixture struct {
	t       *testing.T
	dataDir string
	reg     *ocitest.Registry
	runner  *hookedRunner
	store   *failingStore
	pub     *recorder
	clock   *platformtest.FakeClock
	exec    *exec.Executor
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dataDir := t.TempDir()
	bolt, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = bolt.Close() })
	f := &fixture{
		t:       t,
		dataDir: dataDir,
		reg:     ocitest.NewRegistry(),
		runner:  &hookedRunner{Fake: composetest.New()},
		store:   &failingStore{Bolt: bolt},
		pub:     &recorder{t: t},
		clock:   platformtest.NewFakeClock(start),
	}
	cfg := exec.Config{
		DataDir:      dataDir,
		Archive:      archive.Limits{MaxEntries: 100, MaxExtractedBytes: 1 << 20},
		StartTimeout: startTimeout,
		OTel:         override.OTel{HTTPEndpoint: "http://otel.internal:4318"},
	}
	puller := pull.New(f.reg.Open, filepath.Join(dataDir, "pull"), pull.Limits{MaxBytes: 1 << 20, Timeout: time.Minute})
	f.exec = exec.New(cfg, puller, f.runner, f.store, f.pub, f.clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return f
}

func repository(component string) string {
	return "registry.example/acme/" + contract.ComponentSlug(component)
}

func project(component string) string { return contract.ComposeProjectName(deploymentID, component) }

// push stores a valid Compose archive for each component, with one service named after it.
func (f *fixture) push(components ...string) {
	f.t.Helper()
	for _, c := range components {
		yaml := "services:\n  " + contract.ComponentSlug(c) + ":\n    image: example\n"
		f.reg.PushComposeArchive(f.t, repository(c), revision, archivetest.Build(f.t, archivetest.Valid("app", yaml)...))
	}
}

// deployment returns a deployment of the named components, in order, with default properties.
func deployment(components ...string) contract.ApplicationDeployment {
	d := contract.ApplicationDeployment{ID: deploymentID}
	d.Spec.DeploymentProfile.Type = "compose"
	for _, c := range components {
		d.Spec.DeploymentProfile.Components = append(d.Spec.DeploymentProfile.Components, contract.Component{
			Name:       c,
			Properties: contract.ComponentProperties{Repository: "oci://" + repository(c), Revision: revision},
		})
	}
	return d
}

func applyCommand(digest contract.Digest, d contract.ApplicationDeployment) contract.Command {
	return contract.Command{CommandID: uuid.New(), Action: contract.ActionApply, DeploymentID: d.ID, Digest: digest, Deployment: &d}
}

func removeCommand(digest contract.Digest) contract.Command {
	return contract.Command{CommandID: uuid.New(), Action: contract.ActionRemove, DeploymentID: deploymentID, Digest: digest}
}

func (f *fixture) apply(digest contract.Digest, d contract.ApplicationDeployment) {
	f.t.Helper()
	if err := f.exec.Apply(ctx, applyCommand(digest, d)); err != nil {
		f.t.Fatalf("Apply: %v", err)
	}
}

// stored returns the EN's record of the deployment; ok is false if it has none.
func (f *fixture) stored() (store.Deployment, bool) {
	f.t.Helper()
	d, ok, err := f.store.Get(ctx, deploymentID)
	if err != nil {
		f.t.Fatalf("read store: %v", err)
	}
	return d, ok
}

// assertStored checks the recorded digest, projects and component states ("name state").
func (f *fixture) assertStored(digest contract.Digest, projects []string, states ...string) {
	f.t.Helper()
	d, ok := f.stored()
	if !ok {
		f.t.Fatal("the deployment is not in the EN store")
	}
	if d.Applied.Digest != digest {
		f.t.Errorf("recorded digest = %s, want %s", d.Applied.Digest, digest)
	}
	if !reflect.DeepEqual(d.Applied.ComposeProjects, projects) {
		f.t.Errorf("recorded projects = %v, want %v", d.Applied.ComposeProjects, projects)
	}
	want := map[string]string{}
	for _, s := range states {
		name, state, _ := strings.Cut(s, " ")
		want[name] = state
	}
	got := map[string]string{}
	for name, s := range d.Components {
		got[name] = string(s.State)
	}
	if !reflect.DeepEqual(got, want) {
		f.t.Errorf("recorded component states = %v, want %v", got, want)
	}
}

func (f *fixture) assertEvents(want ...string) {
	f.t.Helper()
	if got := f.pub.take(); !reflect.DeepEqual(got, want) {
		f.t.Errorf("status events =\n  %q\nwant\n  %q", got, want)
	}
}

// calls returns the runtime calls so far as "op component".
func (f *fixture) calls() []string {
	var out []string
	for _, c := range f.runner.Calls() {
		out = append(out, c.Op+" "+strings.TrimPrefix(c.Project, deploymentID.String()+"-"))
	}
	return out
}

func (f *fixture) assertCalls(want ...string) {
	f.t.Helper()
	if got := f.calls(); !reflect.DeepEqual(got, want) {
		f.t.Errorf("runtime calls = %q, want %q", got, want)
	}
}

// componentDir is where SPEC §9.1 puts a component of the deployment at digest.
func (f *fixture) componentDir(digest contract.Digest, component string) string {
	return filepath.Join(f.dataDir, "deployments", deploymentID.String(),
		strings.Replace(digest.String(), ":", "-", 1), contract.ComponentSlug(component))
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return err == nil
}

// An Apply pulls, extracts and starts each component, reports each state, and records what it
// applied (SPEC §8.9 steps 4 and 6, §9.1).
func TestApplyInstallsComponents(t *testing.T) {
	f := newFixture(t)
	f.push("web", "DB/Primary")

	f.apply(digestA, deployment("web", "DB/Primary"))

	events := f.pub.all()
	f.assertEvents("web installing", "web installed", "DB/Primary installing", "DB/Primary installed")
	for _, e := range events {
		if e.DeploymentID != deploymentID || e.Digest != digestA || !e.At.Equal(start) {
			t.Errorf("event %+v: want deployment %s, digest %s, at %s", e, deploymentID, digestA, start)
		}
	}
	f.assertCalls("services web", "up web", "services db-primary", "up db-primary")
	if got, want := f.runner.Projects(), []string{project("DB/Primary"), project("web")}; !reflect.DeepEqual(got, want) {
		t.Errorf("projects up = %v, want %v", got, want)
	}
	f.assertStored(digestA, []string{project("web"), project("DB/Primary")}, "web installed", "DB/Primary installed")
	for _, c := range []string{"web", "DB/Primary"} {
		dir := f.componentDir(digestA, c)
		if !exists(t, filepath.Join(dir, "app", archive.ComposeFile)) || !exists(t, filepath.Join(dir, override.File)) {
			t.Errorf("component %q: compose.yaml or the override file is missing under %s", c, dir)
		}
	}
	if left, _ := os.ReadDir(filepath.Join(f.dataDir, "pull")); len(left) != 0 {
		t.Errorf("%d layers left in the spool directory", len(left))
	}
}

// SPEC §17.6: "Apply of an already installed digest does nothing and acks `accepted: true`." The
// ack is the command queue's (roadmap D4c).
func TestSpec_17_6_ApplyInstalledDigestIsNoOp(t *testing.T) {
	f := newFixture(t)
	f.push("web", "db")
	d := deployment("web", "db")
	f.apply(digestA, d)
	f.pub.take()
	calls := len(f.runner.Calls())
	served := f.reg.BytesServed(repository("web")) + f.reg.BytesServed(repository("db"))

	f.apply(digestA, d)

	f.assertEvents()
	if got := len(f.runner.Calls()); got != calls {
		t.Errorf("the second Apply made %d runtime calls, want none", got-calls)
	}
	if got := f.reg.BytesServed(repository("web")) + f.reg.BytesServed(repository("db")); got != served {
		t.Errorf("the second Apply read %d bytes from the registry, want none", got-served)
	}
	f.assertStored(digestA, []string{project("web"), project("db")}, "web installed", "db installed")
}

// An Apply of the installed digest runs again when a component is not `installed`, and an Apply of
// another digest always runs (SPEC §8.9 step 1, §7.1).
func TestApplyRunsAgain(t *testing.T) {
	t.Run("after a failure at the same digest", func(t *testing.T) {
		f := newFixture(t)
		f.push("web", "db")
		d := deployment("web", "db")
		f.runner.Fail(composetest.OpUp, project("db"), errors.New("port in use"))
		f.apply(digestA, d)
		f.assertEvents("web installing", "web installed", "db installing", "db failed IEO-COMPOSE-FAILED")

		f.runner.Fail(composetest.OpUp, project("db"), nil)
		f.apply(digestA, d)

		f.assertEvents("web installing", "web installed", "db installing", "db installed")
		f.assertStored(digestA, []string{project("web"), project("db")}, "web installed", "db installed")
	})
	t.Run("at another digest", func(t *testing.T) {
		f := newFixture(t)
		f.push("web", "api")
		f.apply(digestA, deployment("web"))
		f.pub.take()

		f.apply(digestB, deployment("api"))

		f.assertEvents("api installing", "api installed")
		// The project of the dropped component stays recorded, so a Remove still brings it down;
		// bringing it down on update is roadmap F (SPEC §8.9 step 5).
		d, _ := f.stored()
		if d.Applied.Digest != digestB || !reflect.DeepEqual(d.Applied.ComposeProjects, []string{project("web"), project("api")}) {
			t.Errorf("recorded %+v, want digest B and both projects", d.Applied)
		}
		if !exists(t, filepath.Join(f.componentDir(digestB, "api"), "app", archive.ComposeFile)) {
			t.Error("the new digest's component was not extracted under its own directory")
		}
	})
}

// SPEC §17.6: "Components are started in listed order; a failure stops later components."
func TestSpec_17_6_ComponentOrder(t *testing.T) {
	t.Run("listed order", func(t *testing.T) {
		f := newFixture(t)
		f.push("zeta", "alpha", "mid")
		f.apply(digestA, deployment("zeta", "alpha", "mid"))
		f.assertCalls("services zeta", "up zeta", "services alpha", "up alpha", "services mid", "up mid")
		f.assertEvents("zeta installing", "zeta installed", "alpha installing", "alpha installed", "mid installing", "mid installed")
	})
	t.Run("a failure stops later components", func(t *testing.T) {
		f := newFixture(t)
		f.push("zeta", "alpha", "mid")
		f.runner.Fail(composetest.OpUp, project("alpha"), errors.New("image not found"))

		f.apply(digestA, deployment("zeta", "alpha", "mid"))

		f.assertCalls("services zeta", "up zeta", "services alpha", "up alpha")
		f.assertEvents("zeta installing", "zeta installed", "alpha installing", "alpha failed IEO-COMPOSE-FAILED")
		// The failed project is recorded: Compose may have created its containers.
		f.assertStored(digestA, []string{project("zeta"), project("alpha")}, "zeta installed", "alpha failed", "mid pending")
		if exists(t, f.componentDir(digestA, "mid")) {
			t.Error("the component after the failed one was extracted")
		}
	})
}

// SPEC §17.6: "`wait: true` waits for running containers; exceeding `timeout` fails with
// `IEO-START-TIMEOUT`."
func TestSpec_17_6_StartTimeout(t *testing.T) {
	f := newFixture(t)
	f.push("web", "db")
	d := deployment("web", "db")
	wait := true
	d.Spec.DeploymentProfile.Components[0].Properties.Wait = &wait
	d.Spec.DeploymentProfile.Components[0].Properties.Timeout = "1m30s"
	f.runner.Fail(composetest.OpUp, project("web"), compose.ErrStartTimeout)

	f.apply(digestA, d)

	up := f.runner.Calls()[1]
	if want := (compose.UpOptions{Wait: true, Timeout: 90 * time.Second}); up.Op != composetest.OpUp || up.Options != want {
		t.Errorf("Up was called as %+v, want options %+v", up, want)
	}
	f.assertEvents("web installing", "web failed IEO-START-TIMEOUT")
	f.assertStored(digestA, []string{project("web")}, "web failed", "db pending")
}

// SPEC §17.6: "`wait: true` with no `timeout` fails with `IEO-START-TIMEOUT` once
// `en.start_timeout` elapses."
func TestSpec_17_6_DefaultStartTimeout(t *testing.T) {
	no := false
	yes := true
	cases := []struct {
		name    string
		wait    *bool
		timeout string
		want    compose.UpOptions
	}{
		{"wait by default, no timeout", nil, "", compose.UpOptions{Wait: true, Timeout: startTimeout}},
		{"wait set, no timeout", &yes, "", compose.UpOptions{Wait: true, Timeout: startTimeout}},
		{"wait by default, own timeout", nil, "0m20s", compose.UpOptions{Wait: true, Timeout: 20 * time.Second}},
		// Without wait neither timeout is used (SPEC §8.9 step 4.5).
		{"no wait", &no, "", compose.UpOptions{}},
		{"no wait, own timeout", &no, "5m0s", compose.UpOptions{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.push("web")
			d := deployment("web")
			d.Spec.DeploymentProfile.Components[0].Properties.Wait = c.wait
			d.Spec.DeploymentProfile.Components[0].Properties.Timeout = c.timeout

			f.apply(digestA, d)

			if up := f.runner.Calls()[1]; up.Op != composetest.OpUp || up.Options != c.want {
				t.Errorf("Up was called as %+v, want options %+v", up, c.want)
			}
			f.assertEvents("web installing", "web installed")
		})
	}
	t.Run("the default elapses", func(t *testing.T) {
		f := newFixture(t)
		f.push("web")
		f.runner.Fail(composetest.OpUp, project("web"), compose.ErrStartTimeout)
		f.apply(digestA, deployment("web"))
		f.assertEvents("web installing", "web failed IEO-START-TIMEOUT")
	})
}

// Each way a component can fail is reported with its code (SPEC §8.9 step 4, §10), names the
// component as the source, and leaves the digest recorded so inventory shows the failure.
func TestApplyErrorCodes(t *testing.T) {
	cases := []struct {
		name      string
		arrange   func(f *fixture, d *contract.ApplicationDeployment)
		code      string
		extracted bool // the component's directory exists afterwards
		recorded  bool // its project is in the record
		calls     []string
	}{
		{"tag does not exist", func(_ *fixture, d *contract.ApplicationDeployment) {
			d.Spec.DeploymentProfile.Components[0].Properties.Revision = "9.9.9"
		}, contract.CodePullFailed, false, false, nil},
		{"layer does not match its digest", func(f *fixture, _ *contract.ApplicationDeployment) {
			data := archivetest.Build(f.t, archivetest.Valid("app", "services: {}\n")...)
			_, layer := f.reg.PushComposeArchive(f.t, repository("web"), revision, data)
			f.reg.Tamper(repository("web"), layer, []byte("not the archive"))
		}, contract.CodeDigestMismatch, false, false, nil},
		{"archive without compose.yaml", func(f *fixture, _ *contract.ApplicationDeployment) {
			f.reg.PushComposeArchive(f.t, repository("web"), revision, archivetest.Build(f.t, archivetest.File("app/readme.md", "x")))
		}, contract.CodeArchiveInvalid, false, false, nil},
		{"parameter that is not a variable name", func(_ *fixture, d *contract.ApplicationDeployment) {
			d.Spec.Parameters = map[string]contract.ParameterValue{"p": {Value: "v", Targets: []contract.ParameterTarget{
				{Pointer: "NOT-A-NAME", Components: []string{"web"}}}}}
		}, contract.CodeComposeFailed, true, false, nil},
		{"compose cannot read the archive's file", func(f *fixture, _ *contract.ApplicationDeployment) {
			f.runner.Fail(composetest.OpServices, project("web"), errors.New("yaml: line 3"))
		}, contract.CodeComposeFailed, true, false, []string{"services web"}},
		{"compose up fails", func(f *fixture, _ *contract.ApplicationDeployment) {
			f.runner.Fail(composetest.OpUp, project("web"), errors.New("image not found"))
		}, contract.CodeComposeFailed, true, true, []string{"services web", "up web"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.push("web", "db")
			d := deployment("web", "db")
			c.arrange(f, &d)

			f.apply(digestA, d)

			events := f.pub.all()
			f.assertEvents("web installing", "web failed "+c.code)
			if e := events[len(events)-1].Error; e == nil || e.Source != "web" || e.Message == "" {
				t.Errorf("failed event error = %+v, want source web and a message", e)
			}
			f.assertCalls(c.calls...)
			projects := []string{}
			if c.recorded {
				projects = []string{project("web")}
			}
			f.assertStored(digestA, projects, "web failed", "db pending")
			if got := exists(t, f.componentDir(digestA, "web")); got != c.extracted {
				t.Errorf("component directory exists = %v, want %v", got, c.extracted)
			}
			if left, _ := os.ReadDir(filepath.Join(f.dataDir, "pull")); len(left) != 0 {
				t.Errorf("%d layers left in the spool directory", len(left))
			}
		})
	}
}

// A project is in the EN's record before it is brought up, with the command's digest, so no
// project is ever up without a record (SPEC §8.9 step 4.4).
func TestApplyRecordsProjectBeforeUp(t *testing.T) {
	f := newFixture(t)
	f.push("web", "db")
	var seen [][]string
	f.runner.beforeUp = func(compose.Project) error {
		d, ok := f.stored()
		if !ok || d.Applied.Digest != digestA {
			t.Errorf("at Up the record is %+v (present %v), want digest A", d.Applied, ok)
		}
		seen = append(seen, d.Applied.ComposeProjects)
		return nil
	}

	f.apply(digestA, deployment("web", "db"))

	want := [][]string{{project("web")}, {project("web"), project("db")}}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("projects recorded at each Up = %v, want %v", seen, want)
	}
}

// A status event that can't be published does not stop the Apply: the state is recorded, and
// inventory carries it later.
func TestApplyGoesOnWhenPublishFails(t *testing.T) {
	f := newFixture(t)
	f.push("web")
	f.pub.err = errors.New("nats: no connection")

	f.apply(digestA, deployment("web"))

	f.assertStored(digestA, []string{project("web")}, "web installed")
	if got := f.runner.Projects(); len(got) != 1 {
		t.Errorf("projects up = %v, want the one component", got)
	}
}

// When the EN can't keep its record, Apply stops before it brings anything up and returns the
// error (SPEC §8.9 step 4.4).
func TestApplyStopsWhenRecordFails(t *testing.T) {
	f := newFixture(t)
	f.push("web")
	errDisk := errors.New("disk full")
	f.store.putApplied = errDisk

	err := f.exec.Apply(ctx, applyCommand(digestA, deployment("web")))

	if !errors.Is(err, errDisk) {
		t.Errorf("Apply error = %v, want the store's", err)
	}
	f.assertCalls("services web")
	if got := f.runner.Projects(); len(got) != 0 {
		t.Errorf("projects up = %v, want none", got)
	}
	f.assertEvents("web installing")
}

// When the EN is stopped during an Apply, the Apply ends there: it records what it brought up
// and reports nothing more (SPEC §8.9).
func TestApplyStopsWhenContextEnds(t *testing.T) {
	f := newFixture(t)
	f.push("web", "db")
	runCtx, cancel := context.WithCancel(ctx)
	f.runner.beforeUp = func(p compose.Project) error {
		if p.Name == project("db") {
			cancel()
			return runCtx.Err()
		}
		return nil
	}

	err := f.exec.Apply(runCtx, applyCommand(digestA, deployment("web", "db")))

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Apply error = %v, want context.Canceled", err)
	}
	f.assertEvents("web installing", "web installed", "db installing")
	f.assertStored(digestA, []string{project("web"), project("db")}, "web installed", "db installing")
}

// SPEC §17.6: "Remove of an unknown deployment acks `accepted: true` and reports `removed` with
// the command's digest." The ack is the command queue's (roadmap D4c).
func TestSpec_17_6_RemoveUnknownDeployment(t *testing.T) {
	f := newFixture(t)

	if err := f.exec.Remove(ctx, removeCommand(digestB)); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	events := f.pub.all()
	f.assertEvents("removed")
	if e := events[0]; e.Digest != digestB || e.DeploymentID != deploymentID || e.Component != "" || !e.At.Equal(start) {
		t.Errorf("event = %+v, want the command's digest and no component", e)
	}
	f.assertCalls()
	if _, ok := f.stored(); ok {
		t.Error("Remove of an unknown deployment left a record")
	}
}

// SPEC §17.6: "Remove of a deployment applied at another digest than the command's reports
// `removing` and `removed` with the applied digest."
func TestSpec_17_6_RemoveReportsAppliedDigest(t *testing.T) {
	f := newFixture(t)
	f.push("web", "db")
	f.apply(digestA, deployment("web", "db"))
	f.pub.take()
	f.clock.Advance(time.Hour)

	if err := f.exec.Remove(ctx, removeCommand(digestB)); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	events := f.pub.all()
	f.assertEvents("removing", "removed")
	for _, e := range events {
		if e.Digest != digestA || e.Component != "" || !e.At.Equal(start.Add(time.Hour)) {
			t.Errorf("event = %+v, want the applied digest A, no component, the time of the Remove", e)
		}
	}
	if got := f.runner.Projects(); len(got) != 0 {
		t.Errorf("projects still up: %v", got)
	}
	if _, ok := f.stored(); ok {
		t.Error("the deployment is still in the EN store")
	}
	if exists(t, filepath.Join(f.dataDir, "deployments", deploymentID.String())) {
		t.Error("the deployment's working directory was not deleted")
	}
}

// A Remove that can't bring a project down keeps the record and does not report `removed`, so the
// LO sends Remove again; the other projects are still brought down (SPEC §8.9).
func TestRemoveKeepsRecordWhenDownFails(t *testing.T) {
	f := newFixture(t)
	f.push("web", "db")
	f.apply(digestA, deployment("web", "db"))
	f.pub.take()
	f.runner.Fail(composetest.OpDown, project("web"), errors.New("engine not reachable"))

	if err := f.exec.Remove(ctx, removeCommand(digestA)); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	f.assertEvents("removing")
	if got, want := f.runner.Projects(), []string{project("web")}; !reflect.DeepEqual(got, want) {
		t.Errorf("projects up = %v, want only the one that could not be brought down", got)
	}
	f.assertStored(digestA, []string{project("web"), project("db")}, "web installed", "db installed")

	f.runner.Fail(composetest.OpDown, project("web"), nil)
	if err := f.exec.Remove(ctx, removeCommand(digestA)); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	f.assertEvents("removing", "removed")
	if _, ok := f.stored(); ok {
		t.Error("the deployment is still in the EN store after the second Remove")
	}
}

// A Remove after a failed first Apply, which recorded the digest and no project, still clears
// the record and reports `removed`.
func TestRemoveAfterFailedApply(t *testing.T) {
	f := newFixture(t)
	f.apply(digestA, deployment("web")) // nothing pushed: the pull fails
	f.assertEvents("web installing", "web failed IEO-PULL-FAILED")

	if err := f.exec.Remove(ctx, removeCommand(digestA)); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	f.assertEvents("removing", "removed")
	if _, ok := f.stored(); ok {
		t.Error("the deployment is still in the EN store")
	}
}

// Remove stops when the EN is stopped, keeps the record and reports nothing more.
func TestRemoveStopsWhenContextEnds(t *testing.T) {
	f := newFixture(t)
	f.push("web")
	f.apply(digestA, deployment("web"))
	f.pub.take()
	runCtx, cancel := context.WithCancel(ctx)
	cancel()

	err := f.exec.Remove(runCtx, removeCommand(digestA))

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Remove error = %v, want context.Canceled", err)
	}
	if _, ok := f.stored(); !ok {
		t.Error("the record was deleted although the projects were not brought down")
	}
	for _, e := range f.pub.take() {
		if e == "removed" {
			t.Error("reported removed although the projects were not brought down")
		}
	}
}
