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

// The status-event rules of ADR 0015 (SPEC §7.5).
func TestApplyStatus(t *testing.T) {
	held := func() store.HostActual {
		return actual(reported,
			deployment(idA, digest1, component("web", contract.StateInstalled), failed("db")),
			deployment(idB, digest1, component("web", contract.StateInstalled)))
	}
	event := func(id uuid.UUID, digest contract.Digest, name string, state contract.ComponentState) contract.ComponentStatusEvent {
		return contract.ComponentStatusEvent{DeploymentID: id, Digest: digest, Component: name, State: state, At: later}
	}
	other := deployment(idB, digest1, component("web", contract.StateInstalled))
	tests := []struct {
		name string
		have store.HostActual
		ev   contract.ComponentStatusEvent
		want store.HostActual
		same bool // the event changes nothing
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
		{name: "removing a deployment not held changes nothing",
			have: actual(reported, other), ev: event(idA, digest1, "", contract.StateRemoving), same: true},
		{name: "removed for a deployment not held changes nothing",
			have: actual(reported, other), ev: event(idA, digest1, "", contract.StateRemoved), same: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := clone(tt.have)
			got, changed := applyStatus(tt.have, tt.ev)
			if !reflect.DeepEqual(tt.have, before) {
				t.Errorf("applyStatus modified its argument:\n%+v\nwas\n%+v", tt.have, before)
			}
			if tt.same {
				if changed || !reflect.DeepEqual(got, before) {
					t.Errorf("applyStatus = %+v, changed %v; want the state unchanged", got, changed)
				}
				return
			}
			if !changed || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("applyStatus = changed %v,\n%+v\nwant\n%+v", changed, got, tt.want)
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
