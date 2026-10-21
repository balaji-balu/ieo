package status_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/status"
)

var (
	host = contract.DeviceID{Site: "site-a", Host: "host-1"}
	lo   = contract.DeviceID{Site: "site-a"}
	id   = uuid.MustParse("00000000-0000-4000-8000-0000000000aa")
)

const (
	digest = contract.Digest("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	other  = contract.Digest("sha256:2222222222222222222222222222222222222222222222222222222222222222")
)

func desired(components ...string) status.Desired {
	return status.Desired{ID: id, Digest: digest, Adopted: 7, Components: components}
}

func reported(d contract.Digest, cs ...contract.ComponentStatus) *contract.InventoryDeployment {
	return &contract.InventoryDeployment{DeploymentID: id, Digest: d, Components: cs}
}

// SPEC §17.5: "DeploymentStatus has exactly one component entry per component; unreported
// components are `pending`."
func TestSpec_17_5_OneComponentEntryPerComponentUnreportedArePending(t *testing.T) {
	cases := []struct {
		name   string
		actual *contract.InventoryDeployment
		want   []contract.ComponentStatus
	}{
		{
			name:   "nothing reported",
			actual: nil,
			want:   []contract.ComponentStatus{{Name: "web", State: contract.StatePending}, {Name: "db", State: contract.StatePending}},
		},
		{
			name:   "one of two reported, a component not in the deployment ignored",
			actual: reported(digest, contract.ComponentStatus{Name: "db", State: contract.StateInstalled}, contract.ComponentStatus{Name: "old", State: contract.StateInstalled}),
			want:   []contract.ComponentStatus{{Name: "web", State: contract.StatePending}, {Name: "db", State: contract.StateInstalled}},
		},
		{
			name:   "reported only at another digest",
			actual: reported(other, contract.ComponentStatus{Name: "web", State: contract.StateInstalled}, contract.ComponentStatus{Name: "db", State: contract.StateInstalled}),
			want:   []contract.ComponentStatus{{Name: "web", State: contract.StatePending}, {Name: "db", State: contract.StatePending}},
		},
		{
			name:   "both reported, in the deployment's order",
			actual: reported(digest, contract.ComponentStatus{Name: "db", State: contract.StateInstalling}, contract.ComponentStatus{Name: "web", State: contract.StateInstalled}),
			want:   []contract.ComponentStatus{{Name: "web", State: contract.StateInstalled}, {Name: "db", State: contract.StateInstalling}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := status.FromHost(host, desired("web", "db"), tc.actual)
			if !reflect.DeepEqual(got.Components, tc.want) {
				t.Fatalf("components = %+v, want %+v", got.Components, tc.want)
			}
			if got.DeploymentID != id || got.DeviceID != host || got.AdoptedManifestVersion != 7 {
				t.Fatalf("deploymentId, deviceId, adoptedManifestVersion = %s, %s, %d; want %s, %s, 7",
					got.DeploymentID, got.DeviceID, got.AdoptedManifestVersion, id, host)
			}
		})
	}
}

// SPEC §17.5: "`status.state` follows the precedence in §7.2 for every combination of two
// component states."
func TestSpec_17_5_StateFollowsPrecedenceForEveryPairOfComponentStates(t *testing.T) {
	// SPEC §7.2: failed > removing > installing > pending > installed > removed.
	order := []contract.ComponentState{contract.StateFailed, contract.StateRemoving, contract.StateInstalling,
		contract.StatePending, contract.StateInstalled, contract.StateRemoved}
	for i, a := range order {
		for j, b := range order {
			want := order[min(i, j)]
			t.Run(fmt.Sprintf("%s+%s", a, b), func(t *testing.T) {
				got := status.FromHost(host, desired("x", "y"), reported(digest,
					contract.ComponentStatus{Name: "x", State: a}, contract.ComponentStatus{Name: "y", State: b}))
				if got.Status.State != want {
					t.Fatalf("state of %s and %s = %q, want %q", a, b, got.Status.State, want)
				}
			})
		}
	}
}

// SPEC §17.5: "Error `source` follows §8.7."
func TestSpec_17_5_ErrorSourceFollows_8_7(t *testing.T) {
	t.Run("component error: the component's name", func(t *testing.T) {
		got := status.FromHost(host, desired("web"), reported(digest, contract.ComponentStatus{
			Name: "web", State: contract.StateFailed,
			Error: &contract.StatusError{Code: contract.CodePullFailed, Source: "host-1", Message: "registry unreachable"},
		}))
		want := &contract.StatusError{Code: contract.CodePullFailed, Source: "web", Message: "registry unreachable"}
		if len(got.Components) != 1 || !reflect.DeepEqual(got.Components[0].Error, want) {
			t.Fatalf("components = %+v, want one with error %+v", got.Components, want)
		}
	})
	t.Run("gateway error: the LO's device ID", func(t *testing.T) {
		got := status.Gateway(lo, host, desired("web", "db"), contract.CodeUnknownChildDevice, "unknown host host-1")
		want := &contract.StatusError{Code: contract.CodeUnknownChildDevice, Source: "site-a", Message: "unknown host host-1"}
		if !reflect.DeepEqual(got.Status.Error, want) {
			t.Fatalf("status.error = %+v, want %+v", got.Status.Error, want)
		}
		if got.Status.State != contract.StateFailed || got.DeviceID != host || got.AdoptedManifestVersion != 7 || got.DeploymentID != id {
			t.Fatalf("status = %+v, want failed on %s at adopted version 7", got, host)
		}
		if len(got.Components) != 2 {
			t.Fatalf("components = %+v, want web and db", got.Components)
		}
		for i, name := range []string{"web", "db"} {
			c := got.Components[i]
			if c.Name != name || c.State != contract.StateFailed || !reflect.DeepEqual(c.Error, want) {
				t.Fatalf("component %d = %+v, want %s failed with %+v", i, c, name, want)
			}
		}
	})
	t.Run("gateway error with no components: still failed", func(t *testing.T) {
		got := status.Gateway(lo, lo, desired(), contract.CodeInvalidDeployment, "the YAML does not decode")
		if got.Status.State != contract.StateFailed || len(got.Components) != 0 || got.DeviceID != lo {
			t.Fatalf("status = %+v, want failed on %s with no components", got, lo)
		}
	})
}

// The mapping shares nothing with its input: a later change to what the host reported does not
// change a status already built.
func TestFromHostCopiesErrors(t *testing.T) {
	e := &contract.StatusError{Code: contract.CodeStartTimeout, Message: "timeout"}
	actual := reported(digest, contract.ComponentStatus{Name: "web", State: contract.StateFailed, Error: e})
	got := status.FromHost(host, desired("web"), actual)
	e.Message = "changed"
	if len(got.Components) != 1 || got.Components[0].Error == nil || got.Components[0].Error.Message != "timeout" {
		t.Fatalf("components = %+v, want web with a copy of the error, message timeout", got.Components)
	}
}
