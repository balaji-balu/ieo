package reconcile_test

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/reconcile"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

// put is one status the Reconciler handed on.
type put struct {
	status contract.DeploymentStatus
	digest contract.Digest
}

// fakeSink records every status the Reconciler hands on.
type fakeSink struct {
	mu   sync.Mutex
	puts []put
}

func (s *fakeSink) Put(_ context.Context, st contract.DeploymentStatus, digest contract.Digest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts = append(s.puts, put{st, digest})
}

// take returns the statuses handed on since the last take.
func (s *fakeSink) take() []put {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.puts
	s.puts = nil
	return p
}

func withSink(s reconcile.StatusSink) func(f *fixture) {
	return func(f *fixture) { f.cfg.Status = s }
}

// gateway is the gateway status the Reconciler must hand on for deployment id at d: failed with
// code on deviceID, with every component failed with the same error (SPEC §8.7).
func gateway(id uuid.UUID, d losync.Desired, deviceID string, code string, components ...string) contract.DeploymentStatus {
	device, err := contract.ParseDeviceID(deviceID)
	if err != nil {
		panic(err)
	}
	e := &contract.StatusError{Code: code, Source: string(site)}
	s := contract.DeploymentStatus{
		DeploymentID: id, DeviceID: device, AdoptedManifestVersion: d.AdoptedVersion,
		Status:     contract.DeploymentState{State: contract.StateFailed, Error: e},
		Components: []contract.ComponentStatus{},
	}
	for _, c := range components {
		s.Components = append(s.Components, contract.ComponentStatus{Name: c, State: contract.StateFailed, Error: e})
	}
	return s
}

// withoutMessages returns puts with every error message cleared: the message is free text.
func withoutMessages(puts []put) []put {
	clear := func(e *contract.StatusError) *contract.StatusError {
		if e == nil {
			return nil
		}
		c := *e
		c.Message = ""
		return &c
	}
	out := make([]put, len(puts))
	for i, p := range puts {
		s := p.status
		s.Status.Error = clear(s.Status.Error)
		s.Components = slices.Clone(s.Components)
		for j := range s.Components {
			s.Components[j].Error = clear(s.Components[j].Error)
		}
		out[i] = put{s, p.digest}
	}
	return out
}

func samePuts(t *testing.T, what string, got, want []put) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	got = withoutMessages(got)
	slices.SortFunc(got, func(x, y put) int { return slices.Compare(x.status.DeploymentID[:], y.status.DeploymentID[:]) })
	slices.SortFunc(want, func(x, y put) int { return slices.Compare(x.status.DeploymentID[:], y.status.DeploymentID[:]) })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got  %+v\n want %+v", what, got, want)
	}
}

// SPEC §17.4: "Directed deployment to an unknown host → `failed`, code `101`, source = LO device
// ID."
func TestSpec_17_4_DirectedDeploymentToUnknownHostReports101(t *testing.T) {
	desired := map[uuid.UUID]losync.Desired{idA: want(t, idA, "site-1/host-1", "a")}
	sink := &fakeSink{}
	f := newFixture(t, desired, withSink(sink))

	samePuts(t, "statuses after New", sink.take(), []put{
		{gateway(idA, desired[idA], "site-1/host-1", contract.CodeUnknownChildDevice, "web"), desired[idA].Digest},
	})
	f.r.ReconcileAll(f.ctx)
	samePuts(t, "statuses after ReconcileAll", sink.take(), nil)

	// Once the host reports, its own statuses replace the 101 (SPEC §8.4).
	f.r.OnInventory(f.ctx, host1, inventory(host1, start, deployment(idA, desired[idA].Digest, component("web", contract.StateInstalled))))
	samePuts(t, "statuses after the host's Inventory", sink.take(), []put{{contract.DeploymentStatus{
		DeploymentID: idA, DeviceID: contract.DeviceID{Site: site, Host: host1}, AdoptedManifestVersion: 1,
		Status:     contract.DeploymentState{State: contract.StateInstalled},
		Components: []contract.ComponentStatus{component("web", contract.StateInstalled)},
	}, desired[idA].Digest}})
	f.r.ReconcileAll(f.ctx)
	samePuts(t, "statuses once the host is known", sink.take(), nil)
}

// A host the store holds is not unknown, even before its first Inventory since the LO started
// (SPEC §8.4: the LO's `hosts`).
func TestStoredHostIsNotUnknown(t *testing.T) {
	desired := map[uuid.UUID]losync.Desired{idA: want(t, idA, "site-1/host-1", "a")}
	sink := &fakeSink{}
	newFixture(t, desired, withSink(sink), func(f *fixture) {
		if err := f.store.PutActual(f.ctx, host1, store.HostActual{ReportedAt: start, Deployments: []contract.InventoryDeployment{}}); err != nil {
			t.Fatalf("test setup: %v", err)
		}
	})
	samePuts(t, "statuses after New", sink.take(), nil)
}

// SPEC §17.5: "A desired deployment the LO cannot resolve (§8.4) is reported once per LO run at its
// digest and error code"
func TestSpec_17_5_UnresolvableDeploymentReportedOncePerRun(t *testing.T) {
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
	}
	expected := func() []put {
		return []put{
			{gateway(ids["autonomous"], desired[ids["autonomous"]], "site-1", contract.CodeAutonomousPlacementNotSupported, "web"), desired[ids["autonomous"]].Digest},
			{gateway(ids["other site"], desired[ids["other site"]], "site-1", contract.CodeInvalidDeployment, "web"), desired[ids["other site"]].Digest},
			{gateway(ids["the LO itself"], desired[ids["the LO itself"]], "site-1", contract.CodeInvalidDeployment, "web"), desired[ids["the LO itself"]].Digest},
			// A YAML that does not decode names no components (SPEC §8.7).
			{gateway(ids["not a deployment"], desired[ids["not a deployment"]], "site-1", contract.CodeInvalidDeployment), desired[ids["not a deployment"]].Digest},
			// Under the manifest's ID, on the directed host of this site.
			{gateway(ids["another id"], desired[ids["another id"]], "site-1/host-1", contract.CodeInvalidDeployment, "web"), desired[ids["another id"]].Digest},
		}
	}
	sink := &fakeSink{}
	f := newFixture(t, desired, withSink(sink))
	samePuts(t, "statuses after New", sink.take(), expected())
	f.r.ReconcileAll(f.ctx)
	f.r.ReconcileAll(f.ctx)
	samePuts(t, "statuses after two more ReconcileAlls", sink.take(), nil)

	// A new digest is reported again.
	moved := ids["other site"]
	desired[moved] = want(t, moved, "site-2/host-1", "y")
	f.setDesired(desired)
	f.r.ReconcileAll(f.ctx)
	samePuts(t, "statuses after a new digest", sink.take(), []put{
		{gateway(moved, desired[moved], "site-1", contract.CodeInvalidDeployment, "web"), desired[moved].Digest},
	})

	// Another code at the same digest is reported again: placement is not implemented (roadmap
	// slice H), so an autonomous deployment's digest and code stay; the reported set is per run.
	sink2 := &fakeSink{}
	newFixture(t, desired, withSink(sink2))
	samePuts(t, "statuses of a new LO run", sink2.take(), expected())
}

// Host reports hand on a status only for a deployment whose state on that host changed and that is
// desired on that host (SPEC §8.7).
func TestHostReportsHandOnChangedDesiredDeployments(t *testing.T) {
	desired := map[uuid.UUID]losync.Desired{
		idA: want(t, idA, "site-1/host-1", "a"),
		idB: want(t, idB, "site-1/host-2", "b"),
	}
	sink := &fakeSink{}
	f := newFixture(t, desired, withSink(sink), func(f *fixture) {
		for _, h := range []contract.HostID{host1, host2} {
			if err := f.store.PutActual(f.ctx, h, store.HostActual{ReportedAt: start, Deployments: []contract.InventoryDeployment{}}); err != nil {
				t.Fatalf("test setup: %v", err)
			}
		}
	})
	sink.take()

	statusOf := func(id uuid.UUID, h contract.HostID, state contract.ComponentState) put {
		return put{contract.DeploymentStatus{
			DeploymentID: id, DeviceID: contract.DeviceID{Site: site, Host: h}, AdoptedManifestVersion: 1,
			Status:     contract.DeploymentState{State: state},
			Components: []contract.ComponentStatus{component("web", state)},
		}, desired[id].Digest}
	}

	// B reported on host-1, where it is not desired, and C, which is not desired at all: nothing.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(1),
		deployment(idB, desired[idB].Digest, component("web", contract.StateInstalled)),
		deployment(idC, otherDigest, component("web", contract.StateInstalled))))
	samePuts(t, "statuses for deployments not desired on the host", sink.take(), nil)

	// A status event that changes A.
	ev := contract.ComponentStatusEvent{DeploymentID: idA, Digest: desired[idA].Digest, Component: "web", State: contract.StateInstalling, At: start.Add(2)}
	f.r.OnStatus(f.ctx, host1, ev)
	samePuts(t, "statuses after an installing event", sink.take(), []put{statusOf(idA, host1, contract.StateInstalling)})

	// The same event again changes nothing.
	ev.At = start.Add(3)
	f.r.OnStatus(f.ctx, host1, ev)
	samePuts(t, "statuses after a repeated event", sink.take(), nil)

	// An Inventory that changes A and leaves the rest as they were.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(4),
		deployment(idA, desired[idA].Digest, component("web", contract.StateInstalled)),
		deployment(idB, desired[idB].Digest, component("web", contract.StateInstalled)),
		deployment(idC, otherDigest, component("web", contract.StateInstalled))))
	samePuts(t, "statuses after an Inventory", sink.take(), []put{statusOf(idA, host1, contract.StateInstalled)})

	// A deployment the host no longer reports is `pending` again.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(5)))
	samePuts(t, "statuses after an empty Inventory", sink.take(), []put{statusOf(idA, host1, contract.StatePending)})
}
