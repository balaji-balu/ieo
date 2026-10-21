package reconcile_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

// fakeSink is a reconcile.StatusSink that records every status it is given.
type fakeSink struct {
	mu       sync.Mutex
	statuses []contract.DeploymentStatus
	digests  []contract.Digest
}

func (s *fakeSink) Put(_ context.Context, st contract.DeploymentStatus, digest contract.Digest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, st)
	s.digests = append(s.digests, digest)
}

// take returns the statuses put since the last take, and forgets them.
func (s *fakeSink) take() []contract.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.statuses
	s.statuses, s.digests = nil, nil
	return out
}

func withSink(sink *fakeSink) func(f *fixture) {
	return func(f *fixture) { f.cfg.Status = sink }
}

var (
	loDevice    = contract.DeviceID{Site: site}
	host1Device = contract.DeviceID{Site: site, Host: host1}
)

// checkGateway checks that s is the gateway status of deployment id at device, with code and a
// failed entry per component (SPEC §8.7).
func checkGateway(t *testing.T, s contract.DeploymentStatus, id uuid.UUID, device contract.DeviceID, code string, components int) {
	t.Helper()
	if s.DeploymentID != id || s.DeviceID != device || s.AdoptedManifestVersion != 1 {
		t.Errorf("status %+v: want deploymentId %s, deviceId %s, adoptedManifestVersion 1", s, id, device)
	}
	e := s.Status.Error
	if s.Status.State != contract.StateFailed || e == nil || e.Code != code || e.Source != string(site) || e.Message == "" {
		t.Errorf("deployment %s: status %+v error %+v, want failed with code %s, source %s and a message", id, s.Status, e, code, site)
	}
	if len(s.Components) != components {
		t.Errorf("deployment %s: %d components, want %d", id, len(s.Components), components)
	}
	for _, c := range s.Components {
		if c.State != contract.StateFailed || c.Error == nil || *c.Error != *e {
			t.Errorf("deployment %s: component %+v, want failed with the status's error", id, c)
		}
	}
}

// SPEC §17.4: "Directed deployment to an unknown host → `failed`, code `101`, source = LO device
// ID."
func TestSpec_17_4_DirectedDeploymentToUnknownHostReports101(t *testing.T) {
	desired := map[uuid.UUID]losync.Desired{idA: want(t, idA, "site-1/host-1", "a")}
	sink := &fakeSink{}
	f := newFixture(t, desired, withSink(sink))

	got := sink.take()
	if len(got) != 1 {
		t.Fatalf("statuses after New: %+v, want one", got)
	}
	checkGateway(t, got[0], idA, host1Device, contract.CodeUnknownDevice, 1)

	f.r.ReconcileAll(f.ctx)
	if got := sink.take(); len(got) != 0 {
		t.Errorf("statuses after another pass over all hosts: %+v, want none: the 101 was reported", got)
	}

	// The host's first Inventory does not list the deployment, so it changes nothing of it (SPEC
	// §8.7); the Apply it gets brings the host's own statuses, which replace the 101.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start))
	if got := sink.take(); len(got) != 0 {
		t.Errorf("statuses after an Inventory without the deployment: %+v, want none", got)
	}
	equal(t, "commands", f.pass(host1), []string{apply(idA, desired[idA])})
	f.r.OnStatus(f.ctx, host1, contract.ComponentStatusEvent{
		DeploymentID: idA, Digest: desired[idA].Digest, Component: "web",
		State: contract.StateInstalling, At: start.Add(1),
	})
	got = sink.take()
	if len(got) != 1 || got[0].DeviceID != host1Device || got[0].Status.State != contract.StateInstalling || got[0].Status.Error != nil {
		t.Fatalf("statuses after the host reported the deployment: %+v, want one, installing, from %s", got, host1Device)
	}
	f.r.ReconcileAll(f.ctx)
	if got := sink.take(); len(got) != 0 {
		t.Errorf("statuses after the host is known: %+v, want no 101", got)
	}
}

// A host the store holds is known, though it has not reported since the LO started: its
// deployments wait for its Inventory, and are not reported 101 (SPEC §8.4, §4.1.11).
func TestStoredHostIsNotUnknown(t *testing.T) {
	desired := map[uuid.UUID]losync.Desired{idA: want(t, idA, "site-1/host-1", "a")}
	sink := &fakeSink{}
	newFixture(t, desired, withSink(sink), func(f *fixture) {
		if err := f.store.Memory.PutActual(f.ctx, host1, store.HostActual{ReportedAt: start}); err != nil {
			t.Fatalf("test setup: %v", err)
		}
	})
	if got := sink.take(); len(got) != 0 {
		t.Errorf("statuses: %+v, want none", got)
	}
}

// SPEC §17.5: "A desired deployment the LO cannot resolve (§8.4) is reported once per LO run at
// its digest and error code, …"
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
	wantStatus := map[uuid.UUID]struct {
		device     contract.DeviceID
		code       string
		components int
	}{
		ids["autonomous"]:       {loDevice, contract.CodeAutonomousNotSupported, 1},
		ids["other site"]:       {loDevice, contract.CodeInvalidDeployment, 1},
		ids["the LO itself"]:    {loDevice, contract.CodeInvalidDeployment, 1},
		ids["not a deployment"]: {loDevice, contract.CodeInvalidDeployment, 0},
		// Under the ID the manifest lists, at the host its YAML names.
		ids["another id"]: {host1Device, contract.CodeInvalidDeployment, 1},
	}
	sink := &fakeSink{}
	f := newFixture(t, desired, withSink(sink))

	got := sink.take()
	if len(got) != len(desired) {
		t.Fatalf("statuses after New: %d, want one per deployment, %d", len(got), len(desired))
	}
	seen := map[uuid.UUID]bool{}
	for _, s := range got {
		w, ok := wantStatus[s.DeploymentID]
		if !ok || seen[s.DeploymentID] {
			t.Errorf("unexpected status %+v", s)
			continue
		}
		seen[s.DeploymentID] = true
		checkGateway(t, s, s.DeploymentID, w.device, w.code, w.components)
	}

	// Once per run: not again while the digest and the code stay the same.
	f.r.ReconcileAll(f.ctx)
	f.r.ReconcileAll(f.ctx)
	if got := sink.take(); len(got) != 0 {
		t.Errorf("statuses after two more passes: %+v, want none", got)
	}

	// Again on a new digest, and on a new code.
	desired[ids["autonomous"]] = want(t, ids["autonomous"], "site-1/*", "y")
	desired[ids["other site"]] = want(t, ids["other site"], "site-1/*", "x")
	f.setDesired(desired)
	f.r.ReconcileAll(f.ctx)
	got = sink.take()
	if len(got) != 2 {
		t.Fatalf("statuses after a new digest and a new code: %+v, want two", got)
	}
	for _, s := range got {
		if s.Status.Error == nil || s.Status.Error.Code != contract.CodeAutonomousNotSupported {
			t.Errorf("status %+v, want code %s", s, contract.CodeAutonomousNotSupported)
		}
	}
}

// A report that changes a desired deployment's state on its host gives a DeploymentStatus of that
// deployment, and only of that one (SPEC §8.7).
func TestStatusOnlyWhenAReportChangesADesiredDeployment(t *testing.T) {
	desired := map[uuid.UUID]losync.Desired{
		idA: want(t, idA, "site-1/host-1", "a"),
		idB: want(t, idB, "site-1/host-1", "b"),
	}
	sink := &fakeSink{}
	f := newFixture(t, desired, withSink(sink), func(f *fixture) {
		// Known, so neither is reported 101.
		if err := f.store.Memory.PutActual(f.ctx, host1, store.HostActual{ReportedAt: start}); err != nil {
			t.Fatalf("test setup: %v", err)
		}
	})

	// A appears, B stays absent, C is not desired.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(1),
		deployment(idA, desired[idA].Digest, component("web", contract.StateInstalled)),
		deployment(idC, otherDigest, component("web", contract.StateInstalled))))
	got := sink.take()
	if len(got) != 1 || got[0].DeploymentID != idA || got[0].Status.State != contract.StateInstalled || got[0].AdoptedManifestVersion != 1 {
		t.Fatalf("statuses after the first Inventory: %+v, want one, A installed", got)
	}

	ev := contract.ComponentStatusEvent{
		DeploymentID: idB, Digest: desired[idB].Digest, Component: "web",
		State: contract.StateInstalling, At: start.Add(2),
	}
	f.r.OnStatus(f.ctx, host1, ev)
	got = sink.take()
	if len(got) != 1 || got[0].DeploymentID != idB || got[0].Status.State != contract.StateInstalling {
		t.Fatalf("statuses after B's event: %+v, want one, B installing", got)
	}
	f.r.OnStatus(f.ctx, host1, ev)
	if got := sink.take(); len(got) != 0 {
		t.Errorf("statuses after the same event again: %+v, want none", got)
	}

	// C changes, A and B do not.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(3),
		deployment(idA, desired[idA].Digest, component("web", contract.StateInstalled)),
		deployment(idB, desired[idB].Digest, component("web", contract.StateInstalling))))
	if got := sink.take(); len(got) != 0 {
		t.Errorf("statuses after an Inventory that changes only an undesired deployment: %+v, want none", got)
	}

	// A disappears: every component is pending again.
	f.r.OnInventory(f.ctx, host1, inventory(host1, start.Add(4),
		deployment(idB, desired[idB].Digest, component("web", contract.StateInstalling))))
	got = sink.take()
	if len(got) != 1 || got[0].DeploymentID != idA || got[0].Status.State != contract.StatePending {
		t.Errorf("statuses after A disappeared: %+v, want one, A pending", got)
	}
}
