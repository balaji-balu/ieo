package status_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/status"
)

var (
	id     = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	digest = contract.DigestOf([]byte("desired"))
	other  = contract.DigestOf([]byte("another digest"))
	host   = contract.DeviceID{Site: "site-1", Host: "host-1"}
	lo     = contract.DeviceID{Site: "site-1"}
)

func desired(components ...string) status.Desired {
	return status.Desired{ID: id, Digest: digest, AdoptedVersion: 7, Components: components}
}

func reported(d contract.Digest, components ...contract.ComponentStatus) *contract.InventoryDeployment {
	return &contract.InventoryDeployment{DeploymentID: id, Digest: d, Components: components}
}

func comp(name string, state contract.ComponentState) contract.ComponentStatus {
	return contract.ComponentStatus{Name: name, State: state}
}

// SPEC §17.5: "DeploymentStatus has exactly one component entry per component; unreported
// components are `pending`."
func TestSpec_17_5_OneComponentEntryPerComponentUnreportedArePending(t *testing.T) {
	for _, tc := range []struct {
		name   string
		actual *contract.InventoryDeployment
		want   []contract.ComponentStatus
	}{
		{
			name: "nothing reported",
			want: []contract.ComponentStatus{comp("db", contract.StatePending), comp("web", contract.StatePending)},
		},
		{
			name:   "one of two reported, and one the deployment does not have",
			actual: reported(digest, comp("web", contract.StateInstalled), comp("cache", contract.StateInstalled)),
			want:   []contract.ComponentStatus{comp("db", contract.StatePending), comp("web", contract.StateInstalled)},
		},
		{
			name:   "reported only at another digest",
			actual: reported(other, comp("db", contract.StateInstalled), comp("web", contract.StateInstalled)),
			want:   []contract.ComponentStatus{comp("db", contract.StatePending), comp("web", contract.StatePending)},
		},
		{
			name:   "both reported, in another order",
			actual: reported(digest, comp("web", contract.StateInstalling), comp("db", contract.StateInstalled)),
			want:   []contract.ComponentStatus{comp("db", contract.StateInstalled), comp("web", contract.StateInstalling)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := status.FromHost(host, desired("db", "web"), tc.actual)
			if !reflect.DeepEqual(got.Components, tc.want) {
				t.Errorf("components:\n  got  %+v\n  want %+v (in YAML order)", got.Components, tc.want)
			}
			if got.DeploymentID != id || got.DeviceID != host || got.AdoptedManifestVersion != 7 {
				t.Errorf("status %+v: want deploymentId %s, deviceId %s, adoptedManifestVersion 7", got, id, host)
			}
		})
	}
}

// SPEC §17.5: "`status.state` follows the precedence in §7.2 for every combination of two
// component states."
func TestSpec_17_5_StateFollowsPrecedenceForEveryPairOfComponentStates(t *testing.T) {
	// SPEC §7.2, most severe first.
	precedence := []contract.ComponentState{
		contract.StateFailed, contract.StateRemoving, contract.StateInstalling,
		contract.StatePending, contract.StateInstalled, contract.StateRemoved,
	}
	rank := map[contract.ComponentState]int{}
	for i, s := range precedence {
		rank[s] = i
	}
	for _, a := range precedence {
		for _, b := range precedence {
			want := a
			if rank[b] < rank[a] {
				want = b
			}
			got := status.FromHost(host, desired("one", "two"), reported(digest, comp("one", a), comp("two", b)))
			if got.Status.State != want {
				t.Errorf("components %s and %s: state %s, want %s", a, b, got.Status.State, want)
			}
		}
	}
}

// SPEC §17.5: "Error `source` follows §8.7."
func TestSpec_17_5_ErrorSourceFollows_8_7(t *testing.T) {
	t.Run("a component error has the component's name, whatever the EN sent", func(t *testing.T) {
		failed := contract.ComponentStatus{Name: "web", State: contract.StateFailed,
			Error: &contract.StatusError{Code: contract.CodePullFailed, Source: "host-1", Message: "registry down"}}
		got := status.FromHost(host, desired("web"), reported(digest, failed))
		want := contract.StatusError{Code: contract.CodePullFailed, Source: "web", Message: "registry down"}
		if e := got.Components[0].Error; e == nil || *e != want {
			t.Errorf("component error %+v, want %+v", e, want)
		}
		if failed.Error.Source != "host-1" {
			t.Error("FromHost changed the error it was given")
		}
	})
	t.Run("a gateway error has the LO's device ID", func(t *testing.T) {
		for _, code := range []string{contract.CodeUnknownDevice, contract.CodeAutonomousNotSupported, contract.CodeInvalidDeployment} {
			got := status.Gateway(lo, host, desired("db", "web"), code, "why")
			want := contract.StatusError{Code: code, Source: "site-1", Message: "why"}
			if got.Status.State != contract.StateFailed || got.Status.Error == nil || *got.Status.Error != want {
				t.Errorf("%s: status %+v, want failed with %+v", code, got.Status, want)
			}
			if len(got.Components) != 2 {
				t.Fatalf("%s: %d components, want one per component", code, len(got.Components))
			}
			for _, c := range got.Components {
				if c.State != contract.StateFailed || c.Error == nil || *c.Error != want {
					t.Errorf("%s: component %+v, want failed with %+v", code, c, want)
				}
			}
		}
	})
}

// A gateway status for a YAML the LO cannot read for component names has no component entries
// and is still `failed` (SPEC §7.2, §8.7).
func TestGatewayStatusWithoutComponentsIsFailed(t *testing.T) {
	got := status.Gateway(lo, lo, desired(), contract.CodeInvalidDeployment, "not YAML")
	if got.Status.State != contract.StateFailed || len(got.Components) != 0 || got.DeviceID != lo {
		t.Errorf("status %+v, want failed with no components at %s", got, lo)
	}
}

// The status FromHost returns shares no error with the report it was built from.
func TestFromHostCopiesErrors(t *testing.T) {
	failed := contract.ComponentStatus{Name: "web", State: contract.StateFailed, Error: &contract.StatusError{Code: "x"}}
	actual := reported(digest, failed)
	got := status.FromHost(host, desired("web"), actual)
	got.Components[0].Error.Code = "changed"
	if actual.Components[0].Error.Code != "x" {
		t.Errorf("the report's error became %q", actual.Components[0].Error.Code)
	}
}
