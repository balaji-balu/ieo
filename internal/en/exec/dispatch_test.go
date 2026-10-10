package exec_test

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/compose"
	"github.com/balaji-balu/ieo/internal/en/exec"
	"github.com/balaji-balu/ieo/internal/en/store"
)

var (
	otherDeploymentID = uuid.MustParse("0b4f5c1e-6a0e-4d7c-9a51-3f2f8c1d2e01")
	digestC           = contract.DigestOf([]byte("deployment C"))
)

func (f *fixture) dispatcher(ctx context.Context) *exec.Dispatcher {
	return exec.NewDispatcher(ctx, f.exec, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// encode returns cmd as the LO sends it.
func encode(t *testing.T, cmd contract.Command) []byte {
	t.Helper()
	b, err := contract.EncodeSiteMessage(cmd)
	if err != nil {
		t.Fatalf("encode command: %v", err)
	}
	return b
}

// accept hands a command to the dispatcher and fails the test unless it is acked accepted.
func accept(t *testing.T, d *exec.Dispatcher, cmd contract.Command) {
	t.Helper()
	ack, reply := d.Handle(encode(t, cmd))
	if !reply || !ack.Accepted || ack.Error != nil || ack.CommandID != cmd.CommandID {
		t.Fatalf("ack = %+v (reply %v), want accepted for command %s", ack, reply, cmd.CommandID)
	}
	if _, err := contract.EncodeSiteMessage(ack); err != nil {
		t.Errorf("the ack is not one the LO would accept: %v", err)
	}
}

// gate holds every Up of the fake runtime until the test lets it go, so a test decides what
// arrives while a command is in flight. No test sleeps.
type gate struct {
	t       *testing.T
	entered chan string   // project names, as their Up starts
	release chan struct{} // one receive lets one Up go on
	running atomic.Int32
	overlap atomic.Bool // two Ups ran at once
}

func (f *fixture) gate() *gate {
	g := &gate{t: f.t, entered: make(chan string, 16), release: make(chan struct{})}
	f.runner.beforeUp = func(p compose.Project) error {
		if g.running.Add(1) > 1 {
			g.overlap.Store(true)
		}
		g.entered <- p.Name
		<-g.release
		g.running.Add(-1)
		return nil
	}
	f.t.Cleanup(func() { close(g.release) })
	return g
}

// waitFor returns the project whose Up started next.
func (g *gate) waitFor() string {
	g.t.Helper()
	select {
	case name := <-g.entered:
		return name
	case <-time.After(10 * time.Second):
		g.t.Fatal("no component was brought up; the command did not run")
		return ""
	}
}

// let lets the Up of the named project, which must be the one waiting, go on.
func (g *gate) let(name string) {
	g.t.Helper()
	if got := g.waitFor(); got != name {
		g.t.Fatalf("Up of %s started, want %s", got, name)
	}
	g.release <- struct{}{}
}

// eventsWithDigest returns the events as "component state digest-letter".
func eventsWithDigest(events []contract.ComponentStatusEvent) []string {
	letter := map[contract.Digest]string{digestA: "A", digestB: "B", digestC: "C"}
	var out []string
	for _, e := range events {
		out = append(out, strings.TrimSpace(e.Component+" "+string(e.State))+" "+letter[e.Digest])
	}
	return out
}

// SPEC §17.6: "Commands for the same deployment are processed one at a time."
func TestSpec_17_6_OneCommandPerDeployment(t *testing.T) {
	t.Run("same deployment", func(t *testing.T) {
		f := newFixture(t)
		f.push("web")
		g := f.gate()
		d := f.dispatcher(ctx)

		accept(t, d, applyCommand(digestA, deployment("web")))
		g.waitFor()                                            // A is bringing web up
		accept(t, d, applyCommand(digestB, deployment("web"))) // acked while A is in flight
		g.release <- struct{}{}                                // A goes on
		g.let(project("web"))                                  // B brings web up, only now
		d.Wait()

		want := []string{"web installing A", "web installed A", "web installing B", "web installed B"}
		if got := eventsWithDigest(f.pub.all()); !reflect.DeepEqual(got, want) {
			t.Errorf("events = %q, want %q: the second command must run after the first", got, want)
		}
		if g.overlap.Load() {
			t.Error("two commands of one deployment ran at once")
		}
		f.assertStored(digestB, []string{project("web")}, "web installed")
	})
	t.Run("different deployments run side by side", func(t *testing.T) {
		f := newFixture(t)
		f.push("web")
		g := f.gate()
		d := f.dispatcher(ctx)
		other := deployment("web")
		other.ID = otherDeploymentID

		accept(t, d, applyCommand(digestA, deployment("web")))
		accept(t, d, applyCommand(digestA, other))
		// Both are in Up at once: neither has been let go.
		got := map[string]bool{g.waitFor(): true, g.waitFor(): true}
		want := map[string]bool{project("web"): true, contract.ComposeProjectName(otherDeploymentID, "web"): true}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("projects being brought up = %v, want %v", got, want)
		}
		g.release <- struct{}{}
		g.release <- struct{}{}
		d.Wait()
		if n := len(f.runner.Projects()); n != 2 {
			t.Errorf("%d projects up, want 2", n)
		}
	})
}

// SPEC §17.6: "A command for a deployment with an Apply in flight runs after the in-flight
// component finishes; the superseded Apply starts no later component."
func TestSpec_17_6_SupersededApply(t *testing.T) {
	t.Run("a Remove during the first of three components", func(t *testing.T) {
		f := newFixture(t)
		f.push("a", "b", "c")
		g := f.gate()
		d := f.dispatcher(ctx)

		accept(t, d, applyCommand(digestA, deployment("a", "b", "c")))
		g.waitFor() // component a is being brought up
		accept(t, d, removeCommand(digestA))
		g.release <- struct{}{}
		d.Wait()

		// a's outcome is published, nothing for b and c, then the Remove runs from its start.
		f.assertEvents("a installing", "a installed", "removing", "removed")
		f.assertCalls("services a", "up a", "down a")
		if _, ok := f.stored(); ok {
			t.Error("the deployment is still in the EN store after the Remove")
		}
	})
	t.Run("only the latest waiting command runs", func(t *testing.T) {
		f := newFixture(t)
		f.push("a", "b", "x", "y")
		g := f.gate()
		d := f.dispatcher(ctx)

		accept(t, d, applyCommand(digestA, deployment("a", "b")))
		g.waitFor()
		accept(t, d, applyCommand(digestB, deployment("x")))
		accept(t, d, removeCommand(digestB))
		accept(t, d, applyCommand(digestC, deployment("y")))
		g.release <- struct{}{}
		g.let(project("y"))
		d.Wait()

		want := []string{"a installing A", "a installed A", "y installing C", "y installed C"}
		if got := eventsWithDigest(f.pub.all()); !reflect.DeepEqual(got, want) {
			t.Errorf("events = %q, want %q", got, want)
		}
		f.assertCalls("services a", "up a", "services y", "up y")
		// The superseded Apply recorded what it brought up (SPEC §8.9 step 6), and b stayed pending.
		f.assertStored(digestC, []string{project("a"), project("y")}, "a installed", "b pending", "y installed")
	})
	t.Run("a command during a Remove waits for the whole Remove", func(t *testing.T) {
		f := newFixture(t)
		f.push("a", "b")
		f.apply(digestA, deployment("a", "b"))
		f.pub.take()
		d := f.dispatcher(ctx)

		accept(t, d, removeCommand(digestA))
		accept(t, d, applyCommand(digestB, deployment("a")))
		d.Wait()

		// Whether the Apply arrived before or after the Remove started, the Remove is one step.
		want := []string{"removing A", "removed A", "a installing B", "a installed B"}
		if got := eventsWithDigest(f.pub.all()); !reflect.DeepEqual(got, want) {
			t.Errorf("events = %q, want %q", got, want)
		}
	})
}

// SPEC §17.6: "An Apply whose `deployment` fails validation (Margo schema, `id` other than
// `deploymentId`, profile type not `compose`) acks `accepted: false` with `IEO-INVALID-COMMAND`
// and changes nothing."
func TestSpec_17_6_InvalidCommand(t *testing.T) {
	valid := func(t *testing.T) (contract.Command, string) {
		cmd := applyCommand(digestB, deployment("web"))
		return cmd, string(encode(t, cmd))
	}
	replace := func(t *testing.T, s, old, new string) string {
		t.Helper()
		if !strings.Contains(s, old) {
			t.Fatalf("test setup: %q is not in the command", old)
		}
		return strings.Replace(s, old, new, 1)
	}
	cases := map[string]func(t *testing.T) (contract.Command, string){
		"fails the Margo schema": func(t *testing.T) (contract.Command, string) {
			cmd, s := valid(t)
			return cmd, replace(t, s, `"applicationId"`, `"appId"`)
		},
		"id other than deploymentId": func(t *testing.T) (contract.Command, string) {
			cmd := applyCommand(digestB, deployment("web"))
			cmd.DeploymentID = otherDeploymentID
			return cmd, string(encode(t, cmd))
		},
		"profile type not compose": func(t *testing.T) (contract.Command, string) {
			cmd, s := valid(t)
			return cmd, replace(t, s, `"type":"compose"`, `"type":"helm"`)
		},
		"two components with one project name": func(t *testing.T) (contract.Command, string) {
			cmd := applyCommand(digestB, deployment("web", "WEB"))
			return cmd, string(encode(t, cmd))
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.push("web")
			f.apply(digestA, deployment("web")) // something to leave unchanged
			f.pub.take()
			calls := f.calls()
			d := f.dispatcher(ctx)
			cmd, data := build(t)

			ack, reply := d.Handle([]byte(data))
			d.Wait()

			if !reply || ack.Accepted || ack.CommandID != cmd.CommandID {
				t.Fatalf("ack = %+v (reply %v), want accepted false for command %s", ack, reply, cmd.CommandID)
			}
			if ack.Error == nil || ack.Error.Code != contract.CodeInvalidCommand || ack.Error.Message == "" {
				t.Errorf("ack error = %+v, want code %s and a message", ack.Error, contract.CodeInvalidCommand)
			}
			if _, err := contract.EncodeSiteMessage(ack); err != nil {
				t.Errorf("the ack is not one the LO would accept: %v", err)
			}
			f.assertEvents()
			f.assertCalls(calls...)
			f.assertStored(digestA, []string{project("web")}, "web installed")
		})
	}
}

// A message that is not a Command is dropped: no ack, no change (SPEC §11.2, §17.1).
func TestDispatcherDropsInvalidMessages(t *testing.T) {
	f := newFixture(t)
	d := f.dispatcher(ctx)
	remove := string(encode(t, removeCommand(digestA)))
	for name, data := range map[string]string{
		"not JSON":       `{"commandId":`,
		"unknown action": strings.Replace(remove, `"remove"`, `"restart"`, 1),
		"no digest":      strings.Replace(remove, `"digest"`, `"dgst"`, 1),
	} {
		if ack, reply := d.Handle([]byte(data)); reply {
			t.Errorf("%s: replied %+v, want no reply", name, ack)
		}
	}
	d.Wait()
	f.assertEvents()
	f.assertCalls()
}

// SPEC §17.6: "Apply of an already installed digest does nothing and acks `accepted: true`."
// SPEC §17.6: "Remove of an unknown deployment acks `accepted: true` and reports `removed` with
// the command's digest."
func TestSpec_17_6_IdempotentCommandsAreAcked(t *testing.T) {
	f := newFixture(t)
	f.push("web")
	d := f.dispatcher(ctx)
	accept(t, d, applyCommand(digestA, deployment("web")))
	d.Wait()
	f.assertEvents("web installing", "web installed")
	calls := f.calls()

	accept(t, d, applyCommand(digestA, deployment("web")))
	d.Wait()
	f.assertEvents()
	f.assertCalls(calls...)

	unknown := removeCommand(digestB)
	unknown.DeploymentID = otherDeploymentID
	accept(t, d, unknown)
	d.Wait()
	events := f.pub.all()
	f.assertEvents("removed")
	if e := events[0]; e.DeploymentID != otherDeploymentID || e.Digest != digestB {
		t.Errorf("removed event = %+v, want the unknown deployment and the command's digest", e)
	}
	f.assertCalls(calls...)
}

// When the EN stops, the command in flight ends where it is, nothing waiting is started, and
// later commands get no reply, so the LO retries them (SPEC §8.9).
func TestDispatcherStops(t *testing.T) {
	f := newFixture(t)
	f.push("a", "b")
	g := f.gate()
	runCtx, cancel := context.WithCancel(ctx)
	d := f.dispatcher(runCtx)

	accept(t, d, applyCommand(digestA, deployment("a", "b")))
	g.waitFor()
	accept(t, d, removeCommand(digestA)) // waiting
	cancel()
	g.release <- struct{}{}
	d.Wait()

	// The fake runtime refuses a call whose context has ended, as the Compose CLI is killed.
	f.assertEvents("a installing")
	f.assertStored(digestA, []string{project("a")}, "a installing", "b pending")
	if ack, reply := d.Handle(encode(t, removeCommand(digestA))); reply {
		t.Errorf("a stopped dispatcher replied %+v, want no reply", ack)
	}
	d.Wait()
}

// Inventory lists what the EN has recorded, in a fixed order (SPEC §4.1.12, §11.2).
func TestInventory(t *testing.T) {
	f := newFixture(t)
	host := contract.HostID("host-03")

	empty, err := f.exec.Inventory(ctx, host)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if empty.HostID != host || !empty.At.Equal(start) || len(empty.Deployments) != 0 {
		t.Errorf("inventory of an empty store = %+v", empty)
	}
	if _, err := contract.EncodeSiteMessage(empty); err != nil {
		t.Errorf("the empty inventory is not one the LO would accept: %v", err)
	}

	failure := &contract.StatusError{Code: contract.CodePullFailed, Source: "db", Message: "registry unreachable"}
	put := func(id uuid.UUID, s contract.ComponentStatus) {
		t.Helper()
		if err := f.store.PutComponentStatus(ctx, id, s); err != nil {
			t.Fatal(err)
		}
	}
	for id, digest := range map[uuid.UUID]contract.Digest{deploymentID: digestA, otherDeploymentID: digestB} {
		if err := f.store.Bolt.PutApplied(ctx, id, store.Applied{Digest: digest}); err != nil {
			t.Fatal(err)
		}
	}
	put(deploymentID, contract.ComponentStatus{Name: "web", State: contract.StateInstalled})
	put(deploymentID, contract.ComponentStatus{Name: "db", State: contract.StateFailed, Error: failure})
	put(deploymentID, contract.ComponentStatus{Name: "cache", State: contract.StatePending})
	// A first Apply that has recorded no digest yet: not listed.
	put(uuid.MustParse("ffffffff-ffff-4fff-8fff-ffffffffffff"), contract.ComponentStatus{Name: "web", State: contract.StateInstalling})
	f.clock.Advance(time.Minute)

	got, err := f.exec.Inventory(ctx, host)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	want := contract.Inventory{HostID: host, At: start.Add(time.Minute), Deployments: []contract.InventoryDeployment{
		{DeploymentID: otherDeploymentID, Digest: digestB, Components: []contract.ComponentStatus{}},
		{DeploymentID: deploymentID, Digest: digestA, Components: []contract.ComponentStatus{
			{Name: "cache", State: contract.StatePending},
			{Name: "db", State: contract.StateFailed, Error: failure},
			{Name: "web", State: contract.StateInstalled},
		}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Inventory =\n  %+v\nwant\n  %+v", got, want)
	}
	if _, err := contract.EncodeSiteMessage(got); err != nil {
		t.Errorf("the inventory is not one the LO would accept: %v", err)
	}
}
