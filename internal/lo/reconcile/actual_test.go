package reconcile

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
)

var (
	idA = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	idB = uuid.MustParse("0b0b0b0b-0000-4000-8000-00000000000b")

	digest1 = contract.DigestOf([]byte("one"))
	digest2 = contract.DigestOf([]byte("two"))

	reported = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	later    = reported.Add(time.Minute)
	earlier  = reported.Add(-time.Minute)
)

func component(name string, state contract.ComponentState) contract.ComponentStatus {
	return contract.ComponentStatus{Name: name, State: state}
}

func failed(name string) contract.ComponentStatus {
	return contract.ComponentStatus{Name: name, State: contract.StateFailed, Error: &contract.StatusError{Code: "IEO-PULL", Message: "no such image"}}
}

func deployment(id uuid.UUID, digest contract.Digest, components ...contract.ComponentStatus) contract.InventoryDeployment {
	if components == nil {
		components = []contract.ComponentStatus{}
	}
	return contract.InventoryDeployment{DeploymentID: id, Digest: digest, Components: components}
}

func actual(at time.Time, deployments ...contract.InventoryDeployment) store.HostActual {
	if deployments == nil {
		deployments = []contract.InventoryDeployment{}
	}
	return store.HostActual{ReportedAt: at, Deployments: deployments}
}

var effectNames = map[effect]string{unchanged: "unchanged", changed: "changed", stale: "stale", clockBack: "clockBack"}

// The status-event rules of ADR 0015 (SPEC §7.5), with the order of reports (balaji-balu/ieo#84).
func TestApplyStatus(t *testing.T) {
	held := func() store.HostActual {
		return actual(reported,
			deployment(idA, digest1, component("web", contract.StateInstalled), failed("db")),
			deployment(idB, digest1, component("web", contract.StateInstalled)))
	}
	event := func(id uuid.UUID, digest contract.Digest, name string, state contract.ComponentState) contract.ComponentStatusEvent {
		return contract.ComponentStatusEvent{DeploymentID: id, Digest: digest, Component: name, State: state, At: later}
	}
	// at returns ev as made at another time.
	at := func(at time.Time, ev contract.ComponentStatusEvent) contract.ComponentStatusEvent {
		ev.At = at
		return ev
	}
	other := deployment(idB, digest1, component("web", contract.StateInstalled))
	tests := []struct {
		name string
		have store.HostActual
		ev   contract.ComponentStatusEvent
		want store.HostActual
		same bool // the event changes nothing
		old  bool // the event is older than the state held
	}{
		{name: "a component at the held digest is set, and the others are kept",
			have: held(), ev: event(idA, digest1, "web", contract.StateInstalling),
			want: actual(later, deployment(idA, digest1, component("web", contract.StateInstalling), failed("db")), other)},
		{name: "a component the LO does not hold yet is added at the held digest",
			have: held(), ev: event(idA, digest1, "cache", contract.StatePending),
			want: actual(later, deployment(idA, digest1, component("web", contract.StateInstalled), failed("db"), component("cache", contract.StatePending)), other)},
		{name: "a failed component keeps the event's error",
			have: held(), ev: contract.ComponentStatusEvent{DeploymentID: idB, Digest: digest1, Component: "web", State: contract.StateFailed,
				Error: &contract.StatusError{Code: "IEO-PULL", Message: "no such image"}, At: later},
			want: actual(later, deployment(idA, digest1, component("web", contract.StateInstalled), failed("db")), deployment(idB, digest1, failed("web")))},
		{name: "a component that recovers loses its error",
			have: held(), ev: event(idA, digest1, "db", contract.StateInstalled),
			want: actual(later, deployment(idA, digest1, component("web", contract.StateInstalled), component("db", contract.StateInstalled)), other)},
		{name: "a component at another digest replaces the entry with that one component",
			have: held(), ev: event(idA, digest2, "db", contract.StateInstalling),
			want: actual(later, deployment(idA, digest2, component("db", contract.StateInstalling)), other)},
		{name: "a component of a deployment not held adds the entry",
			have: actual(reported, other), ev: event(idA, digest2, "web", contract.StatePending),
			want: actual(later, other, deployment(idA, digest2, component("web", contract.StatePending)))},
		{name: "removing sets every held component, drops the errors and keeps the held digest",
			have: held(), ev: event(idA, digest2, "", contract.StateRemoving),
			want: actual(later, deployment(idA, digest1, component("web", contract.StateRemoving), component("db", contract.StateRemoving)), other)},
		{name: "removed drops the deployment, whatever the event's digest",
			have: held(), ev: event(idA, digest2, "", contract.StateRemoved),
			want: actual(later, other)},
		{name: "removed of the last deployment leaves an empty list",
			have: actual(reported, other), ev: event(idB, digest1, "", contract.StateRemoved),
			want: actual(later)},
		{name: "a component already in the reported state changes nothing",
			have: held(), ev: event(idA, digest1, "web", contract.StateInstalled), same: true},
		{name: "a failed component reported again with the same error changes nothing",
			have: held(), ev: contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest1, Component: "db", State: contract.StateFailed,
				Error: &contract.StatusError{Code: "IEO-PULL", Message: "no such image"}, At: later}, same: true},
		{name: "a failed component reported again with another error is set",
			have: actual(reported, deployment(idA, digest1, failed("db"))),
			ev: contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest1, Component: "db", State: contract.StateFailed,
				Error: &contract.StatusError{Code: "IEO-START", Message: "exited"}, At: later},
			want: actual(later, deployment(idA, digest1, contract.ComponentStatus{Name: "db", State: contract.StateFailed,
				Error: &contract.StatusError{Code: "IEO-START", Message: "exited"}}))},
		{name: "removing a deployment whose components are all removing changes nothing",
			have: actual(reported, deployment(idA, digest1, component("web", contract.StateRemoving))),
			ev:   event(idA, digest2, "", contract.StateRemoving), same: true},
		{name: "removing a deployment not held changes nothing",
			have: actual(reported, other), ev: event(idA, digest1, "", contract.StateRemoving), same: true},
		{name: "removed for a deployment not held changes nothing",
			have: actual(reported, other), ev: event(idA, digest1, "", contract.StateRemoved), same: true},
		{name: "an event older than the state held is stale, though it names another state",
			have: held(), ev: at(earlier, event(idA, digest1, "web", contract.StateFailed)), old: true},
		{name: "an older removed is stale",
			have: held(), ev: at(earlier, event(idA, digest1, "", contract.StateRemoved)), old: true},
		{name: "an older event at another digest is stale",
			have: held(), ev: at(earlier, event(idA, digest2, "web", contract.StateInstalling)), old: true},
		{name: "an older event that reports what is held is stale",
			have: held(), ev: at(earlier, event(idA, digest1, "web", contract.StateInstalled)), old: true},
		{name: "an event at the time of the state held is applied",
			have: held(), ev: at(reported, event(idA, digest1, "web", contract.StateInstalling)),
			want: actual(reported, deployment(idA, digest1, component("web", contract.StateInstalling), failed("db")), other)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := clone(tt.have)
			got, effect := applyStatus(tt.have, tt.ev)
			if !reflect.DeepEqual(tt.have, before) {
				t.Errorf("applyStatus modified its argument:\n%+v\nwas\n%+v", tt.have, before)
			}
			wantState, wantEffect := tt.want, changed
			switch {
			case tt.same:
				wantState, wantEffect = before, unchanged
			case tt.old:
				wantState, wantEffect = before, stale
			}
			if effect != wantEffect || !reflect.DeepEqual(got, wantState) {
				t.Errorf("applyStatus = %s,\n%+v\nwant %s,\n%+v", effectNames[effect], got, effectNames[wantEffect], wantState)
			}
		})
	}
}

// The state applyStatus returns shares nothing with its argument or the event: the Reconciler keeps
// both.
func TestApplyStatusSharesNothing(t *testing.T) {
	have := actual(reported, deployment(idA, digest1, component("web", contract.StateInstalled), failed("db")))
	ev := contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest1, Component: "web", State: contract.StateFailed,
		Error: &contract.StatusError{Code: "IEO-PULL"}, At: later}
	got, _ := applyStatus(have, ev)
	want := clone(got)

	have.Deployments[0].Components[1].Error.Code = "changed"
	have.Deployments[0].Components[0].Name = "changed"
	ev.Error.Code = "changed"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the result changed with its inputs:\n%+v\nwant\n%+v", got, want)
	}
}

// SPEC §7.5: an Inventory replaces the state held when its `at` is after that state's, when the
// host's last Inventory was not applied, and when no state is held to compare it with.
func TestApplyInventory(t *testing.T) {
	held := actual(reported, deployment(idA, digest1, component("web", contract.StateInstalled)))
	listed := deployment(idB, digest2, failed("db"))
	tests := []struct {
		name           string
		held           *store.HostActual
		at             time.Time
		lastNotApplied bool
		want           effect
	}{
		{name: "a newer one is applied", held: &held, at: later, want: changed},
		{name: "an older one is not", held: &held, at: earlier, want: stale},
		{name: "one at the time of the state held is not", held: &held, at: reported, want: stale},
		{name: "an older one after one that was not applied is applied: the clock went back",
			held: &held, at: earlier, lastNotApplied: true, want: clockBack},
		{name: "one at the time of the state held, after one that was not applied, is applied",
			held: &held, at: reported, lastNotApplied: true, want: clockBack},
		{name: "a newer one after one that was not applied is applied", held: &held, at: later, lastNotApplied: true, want: changed},
		{name: "one with nothing to compare it with is applied", at: earlier, want: changed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := clone(held)
			inv := contract.Inventory{HostID: "host-1", At: tt.at, Deployments: []contract.InventoryDeployment{listed}}
			got, effect := applyInventory(tt.held, inv, tt.lastNotApplied)
			if !reflect.DeepEqual(held, before) {
				t.Errorf("applyInventory modified the state held:\n%+v\nwas\n%+v", held, before)
			}
			want := actual(tt.at, listed)
			if tt.want == stale {
				want = before
			}
			if effect != tt.want || !reflect.DeepEqual(got, want) {
				t.Errorf("applyInventory = %s,\n%+v\nwant %s,\n%+v", effectNames[effect], got, effectNames[tt.want], want)
			}
		})
	}
}

// The state applyInventory returns shares nothing with the Inventory, and its lists are empty, not
// nil, when they hold nothing.
func TestApplyInventorySharesNothing(t *testing.T) {
	inv := contract.Inventory{HostID: "host-1", At: later, Deployments: []contract.InventoryDeployment{
		{DeploymentID: idA, Digest: digest1, Components: []contract.ComponentStatus{failed("db")}},
		{DeploymentID: idB, Digest: digest1},
	}}
	got, _ := applyInventory(nil, inv, false)
	want := actual(later, deployment(idA, digest1, failed("db")), deployment(idB, digest1))
	inv.Deployments[0].Components[0].Error.Code = "changed"
	inv.Deployments[1].Digest = digest2
	if !reflect.DeepEqual(got, want) {
		t.Errorf("applyInventory:\n%+v\nwant\n%+v", got, want)
	}
	if empty, _ := applyInventory(nil, contract.Inventory{HostID: "host-1", At: later}, false); empty.Deployments == nil {
		t.Error("applyInventory of an Inventory that lists nothing returns a nil list")
	}
}

// clone returns a deep copy of a, made without the code under test.
func clone(a store.HostActual) store.HostActual {
	out := store.HostActual{ReportedAt: a.ReportedAt, Deployments: []contract.InventoryDeployment{}}
	for _, d := range a.Deployments {
		c := contract.InventoryDeployment{DeploymentID: d.DeploymentID, Digest: d.Digest, Components: []contract.ComponentStatus{}}
		for _, s := range d.Components {
			if s.Error != nil {
				e := *s.Error
				s.Error = &e
			}
			c.Components = append(c.Components, s)
		}
		out.Deployments = append(out.Deployments, c)
	}
	return out
}
