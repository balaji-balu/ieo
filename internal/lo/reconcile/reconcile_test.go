package reconcile_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/reconcile"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
	"github.com/balaji-balu/ieo/internal/platform"
	"github.com/balaji-balu/ieo/internal/platform/platformtest"
)

const (
	site     = contract.SiteID("site-1")
	host1    = contract.HostID("host-1")
	host2    = contract.HostID("host-2")
	interval = 5 * time.Minute
	// deadline bounds every wait for something the test expects to happen.
	deadline = 5 * time.Second
)

var (
	idA = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	idB = uuid.MustParse("0b0b0b0b-0000-4000-8000-00000000000b")
	idC = uuid.MustParse("0c0c0c0c-0000-4000-8000-00000000000c")

	start       = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	otherDigest = contract.DigestOf([]byte("what the host runs"))
)

const description = `apiVersion: margo.org/v1-alpha1
id: com-example-hello
deploymentProfiles:
  - type: compose
    id: hello-compose
    components:
      - name: web
        properties:
          repository: oci://registry.test/example/hello-web
          revision: 1.2.3
`

// want returns the desired state of deployment id for device, as the sync loop stores it. name
// goes into the YAML, so two names give two digests.
func want(t *testing.T, id uuid.UUID, device, name string) losync.Desired {
	t.Helper()
	target, err := contract.ParseDeviceID(device)
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}
	y, err := contract.BuildApplicationDeployment([]byte(description), contract.DeploymentChoices{
		ID: id, Name: name, Namespace: "apps", DeviceID: target, Profile: "hello-compose",
	})
	if err != nil {
		t.Fatalf("test setup: build deployment: %v", err)
	}
	return losync.Desired{Digest: contract.DigestOf(y), YAML: y, AdoptedVersion: 1}
}

func component(name string, state contract.ComponentState) contract.ComponentStatus {
	return contract.ComponentStatus{Name: name, State: state}
}

func deployment(id uuid.UUID, digest contract.Digest, components ...contract.ComponentStatus) contract.InventoryDeployment {
	return contract.InventoryDeployment{DeploymentID: id, Digest: digest, Components: components}
}

func inventory(host contract.HostID, at time.Time, deployments ...contract.InventoryDeployment) contract.Inventory {
	return contract.Inventory{HostID: host, At: at, Deployments: deployments}
}

// sent is one call of the fake sender.
type sent struct {
	host contract.HostID
	cmd  contract.Command
	// hasDeadline says whether the call's context had a deadline.
	hasDeadline bool
}

// what names a command without its ID, which is new on every send.
func (s sent) what() string {
	return string(s.cmd.Action) + " " + s.cmd.DeploymentID.String() + " " + string(s.cmd.Digest)
}

// fakeSender is a reconcile.Sender that records every command. It accepts each one unless reply
// says otherwise. With blocking set, a call reports itself on started and then waits for release
// or for its context to end.
type fakeSender struct {
	reply func(s sent) (contract.CommandAck, error)

	blocking bool
	started  chan sent
	gate     map[contract.HostID]chan struct{}
	// onCancel, if set, is waited for by a blocked call whose context ended, before it returns.
	onCancel chan struct{}

	mu          sync.Mutex
	calls       []sent
	inFlight    map[contract.HostID]int
	maxInFlight map[contract.HostID]int
}

func newSender() *fakeSender {
	return &fakeSender{
		started:  make(chan sent),
		gate:     map[contract.HostID]chan struct{}{host1: make(chan struct{}), host2: make(chan struct{})},
		inFlight: map[contract.HostID]int{}, maxInFlight: map[contract.HostID]int{},
	}
}

func (f *fakeSender) Send(ctx context.Context, host contract.HostID, cmd contract.Command) (contract.CommandAck, error) {
	_, hasDeadline := ctx.Deadline()
	s := sent{host: host, cmd: cmd, hasDeadline: hasDeadline}
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.inFlight[host]++
	f.maxInFlight[host] = max(f.maxInFlight[host], f.inFlight[host])
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight[host]--
		f.mu.Unlock()
	}()
	if f.blocking {
		f.started <- s
		select {
		case <-f.gate[host]:
		case <-ctx.Done():
			if f.onCancel != nil {
				<-f.onCancel
			}
			return contract.CommandAck{}, ctx.Err()
		}
	}
	if f.reply != nil {
		return f.reply(s)
	}
	return contract.CommandAck{CommandID: cmd.CommandID, Accepted: true}, nil
}

// to returns the calls made for host so far, in order.
func (f *fakeSender) to(host contract.HostID) []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sent
	for _, s := range f.calls {
		if s.host == host {
			out = append(out, s)
		}
	}
	return out
}

// take returns what was sent to host since the last take, and forgets it.
func (f *fakeSender) take(host contract.HostID) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	rest := f.calls[:0]
	for _, s := range f.calls {
		if s.host == host {
			out = append(out, s.what())
		} else {
			rest = append(rest, s)
		}
	}
	f.calls = rest
	return out
}

// release lets the blocked call for host go on.
func (f *fakeSender) release(t *testing.T, host contract.HostID) {
	t.Helper()
	select {
	case f.gate[host] <- struct{}{}:
	case <-time.After(deadline):
		t.Fatalf("no command for %s is waiting to be released", host)
	}
}

// testStore is the in-memory LO store, counting the writes of actual state and failing on demand.
type testStore struct {
	*store.Memory

	mu       sync.Mutex
	puts     int
	failPut  error
	failLoad error
}

func (s *testStore) PutActual(ctx context.Context, host contract.HostID, a store.HostActual) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPut != nil {
		return s.failPut
	}
	s.puts++
	return s.Memory.PutActual(ctx, host, a)
}

func (s *testStore) Load(ctx context.Context) (losync.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failLoad != nil {
		return losync.State{}, s.failLoad
	}
	return s.Memory.Load(ctx)
}

func (s *testStore) fail(put, load error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPut, s.failLoad = put, load
}

func (s *testStore) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// lockedBuffer is a bytes.Buffer the Reconciler's goroutines and the test may use together.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	store  *testStore
	sender *fakeSender
	logs   *lockedBuffer
	cfg    reconcile.Config
	r      *reconcile.Reconciler
}

// newFixture returns a store holding desired, a fake sender, and a Reconciler made from them once
// setup, if given, has changed the store or the configuration.
func newFixture(t *testing.T, desired map[uuid.UUID]losync.Desired, setup ...func(f *fixture)) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), store: &testStore{Memory: store.NewMemory()}, sender: newSender(), logs: &lockedBuffer{}}
	f.setDesired(desired)
	f.cfg = reconcile.Config{
		SiteID: site, AckTimeout: 5 * time.Second, Interval: interval,
		Clock: platformtest.NewFakeClock(start),
		Log:   slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, fn := range setup {
		fn(f)
	}
	r, err := reconcile.New(f.ctx, f.cfg, f.store, f.sender)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.r = r
	return f
}

func (f *fixture) setDesired(desired map[uuid.UUID]losync.Desired) {
	f.t.Helper()
	if err := f.store.ReplaceDesired(f.ctx, desired); err != nil {
		f.t.Fatalf("test setup: ReplaceDesired: %v", err)
	}
}

// actualOf returns what the store holds as the actual state of host; nil if it holds none.
func (f *fixture) actualOf(host contract.HostID) *store.HostActual {
	f.t.Helper()
	hosts, err := f.store.LoadHosts(f.ctx)
	if err != nil {
		f.t.Fatalf("LoadHosts: %v", err)
	}
	return hosts[host].Actual
}

// pass runs the passes that are due and returns what they sent to host.
func (f *fixture) pass(host contract.HostID) []string {
	f.r.RunPending(f.ctx)
	return f.sender.take(host)
}

// lines returns the log lines with message msg.
func (f *fixture) lines(msg string) []map[string]any {
	f.t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(f.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			f.t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(deadline):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n  got  %q\n  want %q", what, got, want)
	}
}

func apply(id uuid.UUID, d losync.Desired) string {
	return "apply " + id.String() + " " + string(d.Digest)
}

func remove(id uuid.UUID, digest contract.Digest) string {
	return "remove " + id.String() + " " + string(digest)
}

// SPEC §17.4: "Sending a command does not change actual state; only EN reports do."
func TestSpec_17_4_SendingACommandDoesNotChangeActualState(t *testing.T) {
	a, b := want(t, idA, "site-1/host-1", "a"), want(t, idB, "site-1/host-1", "b")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a, idB: b})
	// One command of each outcome: accepted, rejected, not acknowledged.
	f.sender.reply = func(s sent) (contract.CommandAck, error) {
		switch s.cmd.DeploymentID {
		case idA:
			return contract.CommandAck{CommandID: s.cmd.CommandID, Accepted: true}, nil
		case idB:
			return contract.CommandAck{CommandID: s.cmd.CommandID, Error: &contract.AckError{Code: contract.CodeInvalidCommand, Message: "no"}}, nil
		default:
			return contract.CommandAck{}, errors.New("no reply to the request")
		}
	}

	// The host runs A at another digest and C, which is not wanted; B is absent.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start,
		deployment(idA, otherDigest, component("web", contract.StateInstalled)),
		deployment(idC, otherDigest, component("web", contract.StateInstalled))))
	reportedState := f.actualOf(host1)
	if reportedState == nil {
		t.Fatal("the inventory did not reach the store")
	}
	commands := []string{apply(idA, a), apply(idB, b), remove(idC, otherDigest)}
	equal(t, "first pass", f.pass(host1), commands)
	if got := f.actualOf(host1); !reflect.DeepEqual(got, reportedState) {
		t.Errorf("the store's actual state changed by sending commands:\n%+v\nwas\n%+v", got, reportedState)
	}
	if n := f.store.writes(); n != 1 {
		t.Errorf("actual state was written %d times, want once, for the inventory", n)
	}
	// The state the Reconciler plans from did not change either: the next pass sends the same.
	f.r.ReconcileAll(f.ctx)
	equal(t, "second pass", f.pass(host1), commands)

	// Only EN reports change it. A status event:
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idC, Digest: otherDigest, State: contract.StateRemoved, At: start.Add(time.Minute)})
	afterEvent := f.actualOf(host1)
	if reflect.DeepEqual(afterEvent, reportedState) {
		t.Error("the status event did not change the store's actual state")
	}
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass after the status event", f.pass(host1), []string{apply(idA, a), apply(idB, b)})
	// An inventory:
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(2*time.Minute),
		deployment(idA, a.Digest, component("web", contract.StateInstalled)),
		deployment(idB, b.Digest, component("web", contract.StateInstalled))))
	if got := f.actualOf(host1); reflect.DeepEqual(got, afterEvent) {
		t.Error("the inventory did not change the store's actual state")
	}
	equal(t, "pass after the inventory", f.pass(host1), nil)
}

// notifyClock is a FakeClock that reports each timer it makes, so a test advances time only once
// Run is waiting.
type notifyClock struct {
	*platformtest.FakeClock
	timers chan time.Duration
}

func (c notifyClock) NewTimer(d time.Duration) platform.Timer {
	t := c.FakeClock.NewTimer(d)
	c.timers <- d
	return t
}

func withClock(c platform.Clock) func(f *fixture) {
	return func(f *fixture) { f.cfg.Clock = c }
}

// waitIdle waits until no pass of host is in progress.
func (f *fixture) waitIdle(host contract.HostID) {
	f.t.Helper()
	for end := time.Now().Add(deadline); f.r.PassRunning(host); {
		if time.Now().After(end) {
			f.t.Fatalf("the pass of %s did not end", host)
		}
		time.Sleep(time.Millisecond)
	}
}

// SPEC §17.4: "No two reconcile passes for the same host run concurrently."
//
// Every pass here sends one Apply, which the fake sender holds. While the pass of host-1 is held,
// each trigger of SPEC §7.5 marks the host again: an accepted manifest, an inventory and the
// safety timer.
func TestSpec_17_4_NoTwoReconcilePassesForTheSameHostRunConcurrently(t *testing.T) {
	clock := notifyClock{platformtest.NewFakeClock(start), make(chan time.Duration)}
	f := newFixture(t, map[uuid.UUID]losync.Desired{
		idA: want(t, idA, "site-1/host-1", "a"), idB: want(t, idB, "site-1/host-2", "b"),
	}, withClock(clock))
	f.sender.blocking = true
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	f.r.OnInventory(f.ctx, host2, inventory(host2, start))

	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- f.r.Run(ctx) }()
	recv(t, clock.timers, "the safety timer")

	// Two hosts do not wait for each other: both passes are sending before either is released.
	first, second := recv(t, f.sender.started, "the first pass"), recv(t, f.sender.started, "the second pass")
	if hosts := []contract.HostID{first.host, second.host}; !slices.Contains(hosts, host1) || !slices.Contains(hosts, host2) {
		t.Fatalf("passes are sending to %v, want one to each host at the same time", hosts)
	}

	f.r.ReconcileAll(f.ctx)
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(time.Second)))
	clock.Advance(interval)
	recv(t, clock.timers, "the safety timer to have reconciled all hosts")
	if n := len(f.sender.to(host1)); n != 1 {
		t.Fatalf("%d commands were sent to host-1 while its pass was sending, want only that pass's one", n)
	}

	// Marked three times during its pass, the host gets one more pass, not three.
	f.sender.release(t, host1)
	if s := recv(t, f.sender.started, "the second pass of host-1"); s.host != host1 {
		t.Fatalf("the next command went to %s, want host-1", s.host)
	}
	f.sender.release(t, host1)
	f.waitIdle(host1)
	if n := len(f.sender.to(host1)); n != 2 {
		t.Errorf("host-1 got %d passes, want 2: its first and one for the triggers during it", n)
	}

	// host-2 was marked during its pass too.
	f.sender.release(t, host2)
	recv(t, f.sender.started, "the second pass of host-2")
	f.sender.release(t, host2)
	f.waitIdle(host2)

	cancel()
	if err := recv(t, done, "Run to return"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	f.sender.mu.Lock()
	defer f.sender.mu.Unlock()
	for _, h := range []contract.HostID{host1, host2} {
		if n := f.sender.maxInFlight[h]; n != 1 {
			t.Errorf("%d commands were being sent to %s at one time, want 1", n, h)
		}
	}
}

// A host the store already holds is `Unknown` when the LO starts: what it ran before is not acted
// on until it publishes an Inventory (SPEC §7.3 interim rule, ADR 0015). That Inventory is applied
// whatever its `at`: here the stored state is from a later time than it (SPEC §7.5).
func TestLoadedHostGetsNoCommandUntilItsInventory(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a}, func(f *fixture) {
		stale := store.HostActual{ReportedAt: start.Add(time.Hour), Deployments: []contract.InventoryDeployment{
			deployment(idC, otherDigest, component("web", contract.StateInstalled)),
		}}
		if err := f.store.Memory.PutActual(f.ctx, host1, stale); err != nil {
			t.Fatalf("test setup: PutActual: %v", err)
		}
	})

	f.r.ReconcileAll(f.ctx)
	equal(t, "pass before any inventory", f.pass(host1), nil)

	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	equal(t, "pass after the inventory", f.pass(host1), []string{apply(idA, a)})
	if got := f.actualOf(host1); got == nil || !got.ReportedAt.Equal(start) || len(got.Deployments) != 0 {
		t.Errorf("stored actual state %+v, want the inventory's", got)
	}
}

// A status event updates actual state and starts no pass; the next pass plans from it (SPEC §7.5
// interim rule).
func TestStatusEventStartsNoPass(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, deployment(idA, a.Digest, component("web", contract.StateInstalled))))
	equal(t, "pass after the inventory", f.pass(host1), nil)

	at := start.Add(time.Minute)
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: a.Digest, Component: "web",
		State: contract.StateFailed, Error: &contract.StatusError{Code: "IEO-PULL", Message: "gone"}, At: at})
	equal(t, "pass after the status event", f.pass(host1), nil)
	wantActual := &store.HostActual{ReportedAt: at, Deployments: []contract.InventoryDeployment{
		deployment(idA, a.Digest, contract.ComponentStatus{Name: "web", State: contract.StateFailed, Error: &contract.StatusError{Code: "IEO-PULL", Message: "gone"}}),
	}}
	if got := f.actualOf(host1); !reflect.DeepEqual(got, wantActual) {
		t.Errorf("stored actual state:\n%+v\nwant\n%+v", got, wantActual)
	}

	// SPEC §8.5 step 3: a failed component at the desired digest is applied again on the next pass.
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass after ReconcileAll", f.pass(host1), []string{apply(idA, a)})
}

// SPEC §11.2: a payload `hostId` that differs from the subject's host is logged and dropped, and
// changes no state.
func TestInventoryNamingAnotherHostIsDropped(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})

	f.r.OnInventory(f.ctx, host1, inventory(host2, start))
	if f.actualOf(host1) != nil || f.actualOf(host2) != nil || f.store.writes() != 0 {
		t.Error("an inventory naming another host changed the store")
	}
	f.r.ReconcileAll(f.ctx)
	equal(t, "commands to the subject's host", f.pass(host1), nil)
	equal(t, "commands to the payload's host", f.sender.take(host2), nil)
	lines := f.lines("inventory dropped")
	if len(lines) != 1 || lines[0]["site_id"] != "site-1" || lines[0]["host_id"] != "host-1" || lines[0]["payload_host_id"] != "host-2" {
		t.Errorf("log lines for the dropped inventory: %v", lines)
	}
}

// SPEC §11.2: a status event from a host the LO holds no actual state for is logged and dropped.
func TestStatusFromHostWithoutActualStateIsDropped(t *testing.T) {
	f := newFixture(t, nil)
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: otherDigest, Component: "web", State: contract.StateInstalled, At: start})
	if f.actualOf(host1) != nil || f.store.writes() != 0 {
		t.Error("a status event from a host without actual state changed the store")
	}
	lines := f.lines("status event dropped")
	if len(lines) != 1 || lines[0]["host_id"] != "host-1" || lines[0]["deployment_id"] != idA.String() || lines[0]["digest"] != string(otherDigest) {
		t.Errorf("log lines for the dropped event: %v", lines)
	}
}

// ADR 0015: every change to a host's actual state from one message is one write, and a message
// that changes nothing writes nothing.
func TestOneWritePerChange(t *testing.T) {
	f := newFixture(t, nil)
	f.r.OnInventory(f.ctx, host1, inventory(host1, start,
		deployment(idA, otherDigest, component("web", contract.StateInstalled), component("db", contract.StateInstalled))))
	if n := f.store.writes(); n != 1 {
		t.Fatalf("an inventory made %d writes, want 1", n)
	}
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: otherDigest, State: contract.StateRemoving, At: start.Add(time.Second)})
	if n := f.store.writes(); n != 2 {
		t.Errorf("`removing` for two components made %d writes, want 1", n-1)
	}
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idB, Digest: otherDigest, State: contract.StateRemoved, At: start.Add(2 * time.Second)})
	if n := f.store.writes(); n != 2 {
		t.Errorf("`removed` for a deployment not held made %d writes, want none", n-2)
	}
}

// A message the store does not take is dropped whole: the state the Reconciler plans from never
// gets ahead of the store.
func TestStoreFailureLeavesStateUnchanged(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	broken := errors.New("disk full")

	// A first inventory the store refuses leaves the host `Unknown`.
	f.store.fail(broken, nil)
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass after a first inventory that was not stored", f.pass(host1), nil)

	f.store.fail(nil, nil)
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	equal(t, "pass after the stored inventory", f.pass(host1), []string{apply(idA, a)})
	stored := f.actualOf(host1)

	// Neither report below is stored, so the host still looks as it did: A is absent.
	f.store.fail(broken, nil)
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(time.Minute), deployment(idA, a.Digest, component("web", contract.StateInstalled))))
	equal(t, "pass after an inventory that was not stored", f.pass(host1), nil)
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: a.Digest, Component: "web", State: contract.StateInstalled, At: start.Add(2 * time.Minute)})
	f.store.fail(nil, nil)
	if got := f.actualOf(host1); !reflect.DeepEqual(got, stored) {
		t.Errorf("the store's actual state changed:\n%+v\nwas\n%+v", got, stored)
	}
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass planned from the state held", f.pass(host1), []string{apply(idA, a)})

	if n := len(f.lines("inventory dropped")); n != 2 {
		t.Errorf("%d inventories were logged as dropped, want 2", n)
	}
	if n := len(f.lines("status event dropped")); n != 1 {
		t.Errorf("%d status events were logged as dropped, want 1", n)
	}
}

// SPEC §11.2, §16.4: an Apply carries the desired digest and the decoded deployment; a Remove
// carries the digest the host reported and no deployment. Each command has its own ID and a
// deadline for its ack.
func TestCommandsCarryWhatTheENNeeds(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, deployment(idC, otherDigest, component("web", contract.StateInstalled))))
	f.r.RunPending(f.ctx)

	calls := f.sender.to(host1)
	if len(calls) != 2 {
		t.Fatalf("%d commands sent, want an Apply and a Remove", len(calls))
	}
	ap, rm := calls[0].cmd, calls[1].cmd
	d := ap.Deployment
	if ap.Action != contract.ActionApply || ap.DeploymentID != idA || ap.Digest != a.Digest || d == nil ||
		d.ID != idA || d.Metadata.Name != "a" || d.Metadata.DeviceID != (contract.DeviceID{Site: site, Host: host1}) ||
		len(d.Spec.DeploymentProfile.Components) != 1 || d.Spec.DeploymentProfile.Components[0].Name != "web" {
		t.Errorf("Apply = %+v, deployment %+v", ap, d)
	}
	if rm.Action != contract.ActionRemove || rm.DeploymentID != idC || rm.Digest != otherDigest || rm.Deployment != nil {
		t.Errorf("Remove = %+v", rm)
	}
	if ap.CommandID == uuid.Nil || rm.CommandID == uuid.Nil || ap.CommandID == rm.CommandID {
		t.Errorf("command IDs %s and %s, want two new ones", ap.CommandID, rm.CommandID)
	}
	if !calls[0].hasDeadline || !calls[1].hasDeadline {
		t.Error("a command was sent without a deadline for its ack")
	}
	// An EN accepts both as site messages.
	for _, c := range calls {
		data, err := contract.EncodeSiteMessage(c.cmd)
		if err != nil {
			t.Fatalf("encode %s: %v", c.what(), err)
		}
		if _, err := contract.DecodeCommand(data); err != nil {
			t.Errorf("an EN refuses %s: %v", c.what(), err)
		}
	}
}

// SPEC §8.4 interim rule: a desired deployment the LO cannot resolve to a host of its site, or
// whose target is autonomous, gets no command on any host: no Apply, and no Remove where a host
// runs it.
func TestUnresolvedDeploymentsGetNoCommand(t *testing.T) {
	ids := map[string]uuid.UUID{}
	for i, name := range []string{"autonomous", "other site", "the LO itself", "not a deployment", "another id"} {
		ids[name] = uuid.MustParse("00000000-0000-4000-8000-00000000000" + string(rune('1'+i)))
	}
	notYAML := []byte("id: [")
	desired := map[uuid.UUID]losync.Desired{
		ids["autonomous"]:       want(t, ids["autonomous"], "site-1/*", "x"),
		ids["other site"]:       want(t, ids["other site"], "site-2/host-1", "x"),
		ids["the LO itself"]:    want(t, ids["the LO itself"], "site-1", "x"),
		ids["not a deployment"]: {Digest: contract.DigestOf(notYAML), YAML: notYAML, AdoptedVersion: 1},
		ids["another id"]:       want(t, idC, "site-1/host-1", "x"),
		idA:                     want(t, idA, "site-1/host-1", "a"),
	}
	f := newFixture(t, desired)

	// The host runs every one of them at another digest, and A as desired.
	held := []contract.InventoryDeployment{deployment(idA, desired[idA].Digest, component("web", contract.StateInstalled))}
	for _, id := range ids {
		held = append(held, deployment(id, otherDigest, component("web", contract.StateInstalled)))
	}
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, held...))
	equal(t, "commands", f.pass(host1), nil)

	// Each is logged with its deployment and digest when all hosts are reconciled (SPEC §13.1).
	f.r.ReconcileAll(f.ctx)
	logged := map[string]int{}
	for _, l := range f.lines("desired deployment gets no command") {
		id, _ := l["deployment_id"].(string)
		if l["site_id"] != "site-1" || l["digest"] != string(desired[uuid.MustParse(id)].Digest) || l["reason"] == "" {
			t.Errorf("log line %v", l)
		}
		logged[id]++
	}
	for name, id := range ids {
		// Once by New and once by ReconcileAll.
		if logged[id.String()] != 2 {
			t.Errorf("the deployment with target %q was logged %d times, want 2", name, logged[id.String()])
		}
	}
	if logged[idA.String()] != 0 {
		t.Error("a resolved deployment was logged as getting no command")
	}
}

// A deployment whose target moves to another host is removed from the first and applied on the
// second (SPEC §8.5 steps 3 and 4).
func TestMovedTargetIsRemovedAndApplied(t *testing.T) {
	onOne := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: onOne})
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, deployment(idA, onOne.Digest, component("web", contract.StateInstalled))))
	f.r.OnInventory(f.ctx, host2, inventory(host2, start))
	f.r.RunPending(f.ctx)
	equal(t, "host-1 before the move", f.sender.take(host1), nil)
	equal(t, "host-2 before the move", f.sender.take(host2), nil)

	onTwo := want(t, idA, "site-1/host-2", "a")
	f.setDesired(map[uuid.UUID]losync.Desired{idA: onTwo})
	f.r.ReconcileAll(f.ctx)
	f.r.RunPending(f.ctx)
	equal(t, "host-1 after the move", f.sender.take(host1), []string{remove(idA, onOne.Digest)})
	equal(t, "host-2 after the move", f.sender.take(host2), []string{apply(idA, onTwo)})
}

// SPEC §7.5 interim rule, §13.1: a rejected or unacknowledged command is logged with its site,
// host, deployment and digest, and the next pass sends it again.
func TestRejectedAndUnacknowledgedCommandsAreLogged(t *testing.T) {
	a, b, c := want(t, idA, "site-1/host-1", "a"), want(t, idB, "site-1/host-1", "b"), want(t, idC, "site-1/host-1", "c")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a, idB: b, idC: c})
	f.sender.reply = func(s sent) (contract.CommandAck, error) {
		switch s.cmd.DeploymentID {
		case idA:
			return contract.CommandAck{CommandID: s.cmd.CommandID, Error: &contract.AckError{Code: contract.CodeInvalidCommand, Message: "bad profile"}}, nil
		case idB:
			return contract.CommandAck{}, errors.New("no reply to the request")
		default: // an ack for some other command is no ack for this one
			return contract.CommandAck{CommandID: uuid.New(), Accepted: true}, nil
		}
	}
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	all := []string{apply(idA, a), apply(idB, b), apply(idC, c)}
	equal(t, "first pass", f.pass(host1), all)

	check := func(l map[string]any, id uuid.UUID, d losync.Desired) {
		t.Helper()
		if l["level"] != "WARN" || l["site_id"] != "site-1" || l["host_id"] != "host-1" || l["deployment_id"] != id.String() ||
			l["digest"] != string(d.Digest) || l["action"] != "apply" || l["command_id"] == nil {
			t.Errorf("log line %v", l)
		}
	}
	rejected, unacked := f.lines("command rejected"), f.lines("command not acknowledged")
	if len(rejected) != 1 || len(unacked) != 2 {
		t.Fatalf("%d rejected and %d unacknowledged commands logged, want 1 and 2", len(rejected), len(unacked))
	}
	check(rejected[0], idA, a)
	if rejected[0]["code"] != contract.CodeInvalidCommand || rejected[0]["reason"] != "bad profile" {
		t.Errorf("rejected command logged as %v", rejected[0])
	}
	check(unacked[0], idB, b)
	if unacked[0]["reason"] != "no reply to the request" {
		t.Errorf("unacknowledged command logged as %v", unacked[0])
	}
	check(unacked[1], idC, c)

	f.r.ReconcileAll(f.ctx)
	equal(t, "next pass", f.pass(host1), all)
}

// Run waits for the passes in progress before it returns, and a command stopped by shutdown is not
// logged as unacknowledged (G-E5, SPEC §14.2).
func TestRunReturnsAfterItsPasses(t *testing.T) {
	clock := notifyClock{platformtest.NewFakeClock(start), make(chan time.Duration)}
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: want(t, idA, "site-1/host-1", "a")}, withClock(clock))
	f.sender.blocking = true
	f.sender.onCancel = make(chan struct{})
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))

	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- f.r.Run(ctx) }()
	recv(t, clock.timers, "the safety timer")
	recv(t, f.sender.started, "the pass")

	cancel()
	select {
	case err := <-done:
		t.Fatalf("Run returned %v while a command was still being sent", err)
	case <-time.After(50 * time.Millisecond): // a short watch that it has not returned (G-F4)
	}
	close(f.sender.onCancel)
	if err := recv(t, done, "Run to return"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if lines := f.lines("command not acknowledged"); len(lines) != 0 {
		t.Errorf("a command stopped by shutdown was logged as unacknowledged: %v", lines)
	}
}

// The safety timer reconciles every host each `lo.reconcile.interval` (SPEC §7.5, §16.2).
func TestSafetyTimerReconcilesAllHosts(t *testing.T) {
	clock := notifyClock{platformtest.NewFakeClock(start), make(chan time.Duration)}
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a}, withClock(clock))
	f.sender.blocking = true
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))

	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- f.r.Run(ctx) }()
	if d := recv(t, clock.timers, "the safety timer"); d != interval {
		t.Fatalf("the safety timer waits %v, want %v", d, interval)
	}
	recv(t, f.sender.started, "the pass for the inventory")
	f.sender.release(t, host1)
	f.waitIdle(host1)

	// A host marked while Run has nothing to do gets its pass at once, not at the next timer.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(time.Second)))
	recv(t, f.sender.started, "the pass for an inventory that arrives while Run is idle")
	f.sender.release(t, host1)
	f.waitIdle(host1)

	clock.Advance(interval - time.Nanosecond)
	clock.Advance(time.Nanosecond)
	// Run sets the next timer before it starts the pass the timer asked for.
	if d := recv(t, clock.timers, "the next safety timer"); d != interval {
		t.Errorf("the next safety timer waits %v, want %v", d, interval)
	}
	if s := recv(t, f.sender.started, "the pass for the safety timer"); s.what() != apply(idA, a) {
		t.Errorf("the safety timer's pass sent %s", s.what())
	}
	if n := len(f.sender.to(host1)); n != 3 {
		t.Errorf("%d commands sent, want one per pass", n)
	}
	f.sender.release(t, host1)

	cancel()
	if err := recv(t, done, "Run to return"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

// When desired state cannot be read, ReconcileAll marks no host and keeps what it had; the next
// one that can read it reconciles (plan D5.3, Q4).
func TestReconcileAllWithoutDesiredStateMarksNoHost(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	f.sender.reply = func(sent) (contract.CommandAck, error) { return contract.CommandAck{}, errors.New("no reply") }
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	equal(t, "first pass", f.pass(host1), []string{apply(idA, a)})

	f.store.fail(nil, errors.New("disk gone"))
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass when desired state cannot be read", f.pass(host1), nil)
	if lines := f.lines("reconcile all hosts failed"); len(lines) != 1 || lines[0]["level"] != "ERROR" || lines[0]["site_id"] != "site-1" {
		t.Errorf("log lines for the failure: %v", lines)
	}

	f.store.fail(nil, nil)
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass once it can be read", f.pass(host1), []string{apply(idA, a)})
}

func TestNewFails(t *testing.T) {
	valid := reconcile.Config{SiteID: site, AckTimeout: time.Second, Interval: time.Minute}
	for name, change := range map[string]func(c *reconcile.Config){
		"no site":        func(c *reconcile.Config) { c.SiteID = "" },
		"no ack timeout": func(c *reconcile.Config) { c.AckTimeout = 0 },
		"no interval":    func(c *reconcile.Config) { c.Interval = 0 },
	} {
		cfg := valid
		change(&cfg)
		if r, err := reconcile.New(context.Background(), cfg, &testStore{Memory: store.NewMemory()}, newSender()); err == nil || r != nil {
			t.Errorf("New with %s = %v, %v; want an error", name, r, err)
		}
	}
	broken := &testStore{Memory: store.NewMemory(), failLoad: errors.New("disk gone")}
	if r, err := reconcile.New(context.Background(), valid, broken, newSender()); err == nil || r != nil {
		t.Errorf("New with a store that cannot be read = %v, %v; want an error", r, err)
	}
}

// SPEC §8.5: an LO that has accepted no State Manifest does not know what should run, so it sends
// no command. Without this, an LO starting with a new or lost store (SPEC §12) would remove
// everything its hosts run, on their first inventories.
func TestNoCommandBeforeAManifestIsAccepted(t *testing.T) {
	f := newFixture(t, nil)
	running := deployment(idC, otherDigest, component("web", contract.StateInstalled))
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, running))
	equal(t, "pass before any manifest", f.pass(host1), nil)
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass after ReconcileAll, still without a manifest", f.pass(host1), nil)
	if lines := f.lines("no State Manifest accepted yet"); len(lines) != 2 || lines[0]["site_id"] != "site-1" {
		t.Errorf("log lines, want one by New and one by ReconcileAll: %v", lines)
	}

	// The sync loop replaces desired state, starts reconciling and only then commits the version
	// (SPEC §8.2 steps 6–8): desired state that holds a deployment is acted on at once.
	a := want(t, idA, "site-1/host-1", "a")
	f.setDesired(map[uuid.UUID]losync.Desired{idA: a})
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass for the first manifest", f.pass(host1), []string{apply(idA, a), remove(idC, otherDigest)})

	// An accepted manifest without deployments does mean that nothing should run.
	f.setDesired(nil)
	if err := f.store.CommitVersion(f.ctx, 2, "etag"); err != nil {
		t.Fatalf("test setup: CommitVersion: %v", err)
	}
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass for an accepted empty manifest", f.pass(host1), []string{remove(idC, otherDigest)})
}

// A deployment directed to a host that has published no Inventory gets no command (SPEC §7.3
// interim rule); it is logged when all hosts are reconciled, so that it leaves a trace until
// status reporting arrives (SPEC §8.4, roadmap slice E).
func TestDeploymentForAHostWithoutInventoryIsLogged(t *testing.T) {
	a, b := want(t, idA, "site-1/host-1", "a"), want(t, idB, "site-1/host-2", "b")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a, idB: b})
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	f.r.ReconcileAll(f.ctx)
	f.r.RunPending(f.ctx)
	equal(t, "commands to the host that reported", f.sender.take(host1), []string{apply(idA, a)})
	equal(t, "commands to the host that did not", f.sender.take(host2), nil)

	lines := f.lines("desired deployment waits for an inventory of its host")
	if len(lines) != 1 || lines[0]["site_id"] != "site-1" || lines[0]["host_id"] != "host-2" ||
		lines[0]["deployment_id"] != idB.String() || lines[0]["digest"] != string(b.Digest) {
		t.Errorf("log lines: %v", lines)
	}
}

// SPEC §8.5: a first manifest that lists no deployment takes effect on the first pass after its
// version is committed. The sync loop starts reconciling before it commits (SPEC §8.2 steps 7–8),
// so that pass is the next one of each host, not the next safety timer.
func TestFirstEmptyManifestTakesEffectOnTheFirstPassAfterItsCommit(t *testing.T) {
	f := newFixture(t, nil)
	running := deployment(idC, otherDigest, component("web", contract.StateInstalled))
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, running))
	f.r.ReconcileAll(f.ctx) // the sync loop, before it commits the version
	equal(t, "pass before the version is committed", f.pass(host1), nil)

	if err := f.store.CommitVersion(f.ctx, 1, "etag"); err != nil {
		t.Fatalf("test setup: CommitVersion: %v", err)
	}
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(time.Minute), running))
	equal(t, "first pass after the version is committed", f.pass(host1), []string{remove(idC, otherDigest)})
}

// Desired state that could not be read is not planned from: it may be older than the store's. The
// next pass of a host reads it again, and sends no command while it cannot.
func TestPassAfterAFailedReadOfDesiredStateReadsItAgain(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	equal(t, "first pass", f.pass(host1), []string{apply(idA, a)})

	// The next manifest deletes A, and the read of it fails.
	f.setDesired(nil)
	if err := f.store.CommitVersion(f.ctx, 2, "etag"); err != nil {
		t.Fatalf("test setup: CommitVersion: %v", err)
	}
	f.store.fail(nil, errors.New("disk gone"))
	f.r.ReconcileAll(f.ctx)

	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(time.Minute)))
	equal(t, "pass while desired state cannot be read", f.pass(host1), nil)
	if lines := f.lines("pass sends no command"); len(lines) != 1 || lines[0]["level"] != "ERROR" || lines[0]["host_id"] != "host-1" {
		t.Errorf("log lines for the pass that could not read desired state: %v", lines)
	}

	f.store.fail(nil, nil)
	installed := deployment(idA, a.Digest, component("web", contract.StateInstalled))
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(2*time.Minute), installed))
	equal(t, "pass once it can be read", f.pass(host1), []string{remove(idA, a.Digest)})
}

// A status event that reports what is already held changes nothing, so it is not written.
func TestRepeatedStatusEventIsNotWritten(t *testing.T) {
	f := newFixture(t, nil)
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, deployment(idA, otherDigest, component("web", contract.StateInstalled))))
	held := f.actualOf(host1)
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: otherDigest, Component: "web", State: contract.StateInstalled, At: start.Add(time.Second)})
	if n := f.store.writes(); n != 1 {
		t.Errorf("a repeated `installed` made %d writes, want none", n-1)
	}
	if got := f.actualOf(host1); !reflect.DeepEqual(got, held) {
		t.Errorf("the store's actual state changed:\n%+v\nwas\n%+v", got, held)
	}
	removing := contract.ComponentStatusEvent{DeploymentID: idA, Digest: otherDigest, State: contract.StateRemoving, At: start.Add(2 * time.Second)}
	f.r.OnStatus(f.ctx, host1, removing)
	removing.At = start.Add(3 * time.Second)
	f.r.OnStatus(f.ctx, host1, removing)
	if n := f.store.writes(); n != 2 {
		t.Errorf("`removing` twice made %d writes, want 1", n-1)
	}
}

// SPEC §17.4: "A report older than the actual state held for its host changes no state"
//
// Inventories and status events arrive on two subscriptions, so not in the order the host
// published them (balaji-balu/ieo#84).
func TestSpec_17_4_ReportOlderThanActualStateChangesNoState(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	installed := deployment(idA, a.Digest, component("web", contract.StateInstalled))

	// A status event after a newer Inventory: the component failed and recovered before the host
	// read the Inventory.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(2*time.Minute), installed))
	held := f.actualOf(host1)
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: a.Digest, Component: "web",
		State: contract.StateFailed, Error: &contract.StatusError{Code: "IEO-PULL", Message: "gone"}, At: start.Add(time.Minute)})
	if got := f.actualOf(host1); !reflect.DeepEqual(got, held) {
		t.Errorf("an older status event changed the store's actual state:\n%+v\nwas\n%+v", got, held)
	}
	if n := f.store.writes(); n != 1 {
		t.Errorf("an older status event made %d writes, want none", n-1)
	}
	// The state the Reconciler plans from did not change either: a failed component is applied again.
	f.r.ReconcileAll(f.ctx)
	equal(t, "pass after the older status event", f.pass(host1), nil)

	// An Inventory after a newer status event: the host listed the deployment and then removed it.
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: a.Digest, State: contract.StateRemoved, At: start.Add(4 * time.Minute)})
	held = f.actualOf(host1)
	if len(held.Deployments) != 0 {
		t.Fatalf("`removed` left %+v in the store", held.Deployments)
	}
	equal(t, "pass after `removed`", f.pass(host1), nil) // SPEC §7.5 interim rule: an event starts none
	writes := f.store.writes()
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(3*time.Minute), installed))
	if got := f.actualOf(host1); !reflect.DeepEqual(got, held) {
		t.Errorf("an older inventory changed the store's actual state:\n%+v\nwas\n%+v", got, held)
	}
	if n := f.store.writes(); n != writes {
		t.Errorf("an older inventory made %d writes, want none", n-writes)
	}
	// SPEC §7.5: the host is still reconciled, from the state held, in which the deployment is absent.
	equal(t, "pass after the older inventory", f.pass(host1), []string{apply(idA, a)})
}

// SPEC §7.5: at the `at` of the state held, a status event is applied and an Inventory is not.
func TestReportAtTheTimeOfTheStateHeld(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	installed := deployment(idA, a.Digest, component("web", contract.StateInstalled))
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, installed))

	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: a.Digest, State: contract.StateRemoved, At: start})
	held := f.actualOf(host1)
	if len(held.Deployments) != 0 {
		t.Fatalf("an event at the time of the state held was not applied: the store holds %+v", held.Deployments)
	}
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, installed))
	if got := f.actualOf(host1); !reflect.DeepEqual(got, held) {
		t.Errorf("an inventory at the time of the state held changed it:\n%+v\nwas\n%+v", got, held)
	}
}

// SPEC §7.5: a host whose clock went back sends reports older than the state held. Its first
// Inventory is not applied, as it may only have crossed a newer event; the next one is, and the LO
// follows the host's clock from then on.
func TestHostWhoseClockWentBackIsFollowedAgain(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	installed := deployment(idA, a.Digest, component("web", contract.StateInstalled))
	minutes := func(n int) time.Time { return start.Add(time.Duration(n) * time.Minute) }

	f.r.OnInventory(f.ctx, host1, inventory(host1, minutes(10)))
	equal(t, "first pass", f.pass(host1), []string{apply(idA, a)})
	held := f.actualOf(host1)

	f.r.OnInventory(f.ctx, host1, inventory(host1, minutes(5), installed))
	if got := f.actualOf(host1); !reflect.DeepEqual(got, held) {
		t.Errorf("the first older inventory changed the store's actual state:\n%+v\nwas\n%+v", got, held)
	}
	equal(t, "pass after the first older inventory", f.pass(host1), []string{apply(idA, a)})
	if lines := f.lines("inventory applied"); len(lines) != 0 {
		t.Errorf("log lines after the first older inventory: %v", lines)
	}

	f.r.OnInventory(f.ctx, host1, inventory(host1, minutes(6), installed))
	wantActual := &store.HostActual{ReportedAt: minutes(6), Deployments: []contract.InventoryDeployment{installed}}
	if got := f.actualOf(host1); !reflect.DeepEqual(got, wantActual) {
		t.Errorf("stored actual state after the second older inventory:\n%+v\nwant\n%+v", got, wantActual)
	}
	equal(t, "pass after the second older inventory", f.pass(host1), nil)
	lines := f.lines("inventory applied")
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["site_id"] != "site-1" || lines[0]["host_id"] != "host-1" ||
		lines[0]["at"] != minutes(6).Format(time.RFC3339Nano) || lines[0]["reported_at"] != minutes(10).Format(time.RFC3339Nano) || lines[0]["reason"] == nil {
		t.Errorf("log lines for the inventory of a host whose clock went back: %v", lines)
	}

	// The host's reports are in order again: an event after that inventory is applied.
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: a.Digest, State: contract.StateRemoved, At: minutes(7)})
	held = f.actualOf(host1)
	if !held.ReportedAt.Equal(minutes(7)) || len(held.Deployments) != 0 {
		t.Fatalf("stored actual state after an event in the host's time: %+v", held)
	}
	// An applied inventory ends the exception: an older one is again one that crossed an event.
	f.r.OnInventory(f.ctx, host1, inventory(host1, minutes(6).Add(time.Second), installed))
	if got := f.actualOf(host1); !reflect.DeepEqual(got, held) {
		t.Errorf("an older inventory after an applied one changed the store's actual state:\n%+v\nwas\n%+v", got, held)
	}
}

// SPEC §7.5: a status event that is not older than the state held shows that the host's clock is
// not behind it, so an Inventory that was not applied had only crossed an event. The host's next
// Inventory is compared again.
func TestAcceptedStatusEventEndsTheExceptionForTheNextInventory(t *testing.T) {
	a := want(t, idA, "site-1/host-1", "a")
	f := newFixture(t, map[uuid.UUID]losync.Desired{idA: a})
	installed := deployment(idA, a.Digest, component("web", contract.StateInstalled))
	minutes := func(n int) time.Time { return start.Add(time.Duration(n) * time.Minute) }
	removed := func(at time.Time) contract.ComponentStatusEvent {
		return contract.ComponentStatusEvent{DeploymentID: idA, Digest: a.Digest, State: contract.StateRemoved, At: at}
	}

	f.r.OnInventory(f.ctx, host1, inventory(host1, minutes(1), installed))
	f.r.OnStatus(f.ctx, host1, removed(minutes(3)))
	f.r.OnInventory(f.ctx, host1, inventory(host1, minutes(2), installed)) // crossed the event: not applied
	// The event that follows reports what is held, and is not older than it.
	f.r.OnStatus(f.ctx, host1, removed(minutes(4)))
	held := f.actualOf(host1)
	f.r.OnInventory(f.ctx, host1, inventory(host1, minutes(2).Add(time.Second), installed))
	if got := f.actualOf(host1); !reflect.DeepEqual(got, held) {
		t.Errorf("an older inventory was applied after a status event was accepted:\n%+v\nwas\n%+v", got, held)
	}
	if lines := f.lines("inventory applied"); len(lines) != 0 {
		t.Errorf("the host's clock was logged as gone back: %v", lines)
	}
}

// A first Inventory the store does not take leaves nothing held for its host: a status event from
// it is still one from a host without actual state (SPEC §11.2).
func TestHostOfAnInventoryThatWasNotStoredIsNotHeld(t *testing.T) {
	f := newFixture(t, nil)
	f.store.fail(errors.New("disk full"), nil)
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	f.store.fail(nil, nil)
	if f.r.Holds(host1) {
		t.Error("the Reconciler holds a host whose only inventory was not stored")
	}
}

// SPEC §7.5, §13.1: a report that is dropped for its age is logged with its host and both times,
// and a status event with its deployment and digest too.
func TestReportsDroppedForTheirAgeAreLogged(t *testing.T) {
	f := newFixture(t, nil)
	held, older := start.Add(time.Minute), start
	f.r.OnInventory(f.ctx, host1, inventory(host1, held))
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{DeploymentID: idA, Digest: otherDigest, Component: "web", State: contract.StateInstalled, At: older})
	f.r.OnInventory(f.ctx, host1, inventory(host1, older))

	for msg, event := range map[string]bool{"status event dropped": true, "inventory dropped": false} {
		lines := f.lines(msg)
		if len(lines) != 1 {
			t.Errorf("%d lines %q, want 1: %v", len(lines), msg, lines)
			continue
		}
		l := lines[0]
		if l["level"] != "INFO" || l["site_id"] != "site-1" || l["host_id"] != "host-1" || l["reason"] == nil ||
			l["at"] != older.Format(time.RFC3339Nano) || l["reported_at"] != held.Format(time.RFC3339Nano) {
			t.Errorf("log line %v", l)
		}
		if event && (l["deployment_id"] != idA.String() || l["digest"] != string(otherDigest)) {
			t.Errorf("log line of the event %v", l)
		}
	}
}
