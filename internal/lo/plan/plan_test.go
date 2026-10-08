package plan_test

import (
	"maps"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/plan"
)

var (
	idA = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	idB = uuid.MustParse("0b0b0b0b-0000-4000-8000-00000000000b")
	idC = uuid.MustParse("0c0c0c0c-0000-4000-8000-00000000000c")
	idD = uuid.MustParse("0d0d0d0d-0000-4000-8000-00000000000d")
	idE = uuid.MustParse("0e0e0e0e-0000-4000-8000-00000000000e")
	idF = uuid.MustParse("0f0f0f0f-0000-4000-8000-00000000000f")

	digest1 = contract.DigestOf([]byte("1"))
	digest2 = contract.DigestOf([]byte("2"))
)

// components maps component names to states.
type components = map[string]contract.ComponentState

func actual(digest contract.Digest, c components) plan.Actual {
	return plan.Actual{Digest: digest, Components: c}
}

func apply(id uuid.UUID, digest contract.Digest) plan.Command {
	return plan.Command{Action: contract.ActionApply, DeploymentID: id, Digest: digest}
}

func remove(id uuid.UUID, digest contract.Digest) plan.Command {
	return plan.Command{Action: contract.ActionRemove, DeploymentID: id, Digest: digest}
}

// SPEC §17.4: "Desired but absent → Apply sent; present with other digest → Apply sent; present
// but not desired → Remove sent; equal → nothing sent."
func TestSpec_17_4_DiffRules(t *testing.T) {
	tests := []struct {
		name string
		want map[uuid.UUID]contract.Digest
		have map[uuid.UUID]plan.Actual
		cmds []plan.Command
	}{
		{
			name: "desired but absent",
			want: map[uuid.UUID]contract.Digest{idA: digest1},
			cmds: []plan.Command{apply(idA, digest1)},
		},
		{
			name: "present with other digest",
			want: map[uuid.UUID]contract.Digest{idA: digest2},
			have: map[uuid.UUID]plan.Actual{idA: actual(digest1, components{"web": contract.StateInstalled})},
			cmds: []plan.Command{apply(idA, digest2)},
		},
		{
			name: "present but not desired",
			have: map[uuid.UUID]plan.Actual{idA: actual(digest1, components{"web": contract.StateInstalled})},
			cmds: []plan.Command{remove(idA, digest1)},
		},
		{
			name: "equal, all installed",
			want: map[uuid.UUID]contract.Digest{idA: digest1},
			have: map[uuid.UUID]plan.Actual{
				idA: actual(digest1, components{"web": contract.StateInstalled, "db": contract.StateInstalled}),
			},
		},
		{
			// SPEC §8.5 step 3.
			name: "equal with a failed component",
			want: map[uuid.UUID]contract.Digest{idA: digest1},
			have: map[uuid.UUID]plan.Actual{
				idA: actual(digest1, components{"web": contract.StateInstalled, "db": contract.StateFailed}),
			},
			cmds: []plan.Command{apply(idA, digest1)},
		},
		{
			name: "equal, pending and installing",
			want: map[uuid.UUID]contract.Digest{idA: digest1},
			have: map[uuid.UUID]plan.Actual{
				idA: actual(digest1, components{"web": contract.StatePending, "db": contract.StateInstalling}),
			},
		},
		{
			name: "equal, no components",
			want: map[uuid.UUID]contract.Digest{idA: digest1},
			have: map[uuid.UUID]plan.Actual{idA: {Digest: digest1}},
		},
		{
			name: "other digest with a failed component",
			want: map[uuid.UUID]contract.Digest{idA: digest2},
			have: map[uuid.UUID]plan.Actual{idA: actual(digest1, components{"web": contract.StateFailed})},
			cmds: []plan.Command{apply(idA, digest2)},
		},
		{
			// SPEC §8.5 step 3 names only failed; removed is not re-applied.
			name: "equal, removing and removed",
			want: map[uuid.UUID]contract.Digest{idA: digest1},
			have: map[uuid.UUID]plan.Actual{
				idA: actual(digest1, components{"web": contract.StateRemoving, "db": contract.StateRemoved}),
			},
		},
		{
			name: "not desired and removing",
			have: map[uuid.UUID]plan.Actual{idA: actual(digest1, components{"web": contract.StateRemoving})},
			cmds: []plan.Command{remove(idA, digest1)},
		},
		{
			name: "nil inputs",
		},
		{
			name: "empty inputs",
			want: map[uuid.UUID]contract.Digest{},
			have: map[uuid.UUID]plan.Actual{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plan.ForHost(tt.want, tt.have)
			if len(got) == 0 && len(tt.cmds) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.cmds) {
				t.Errorf("%s: ForHost = %+v, want %+v", tt.name, got, tt.cmds)
			}
		})
	}
}

// SPEC §17.4: "Desired but absent → Apply sent; present with other digest → Apply sent; present
// but not desired → Remove sent; equal → nothing sent." The commands of a host with every case come
// in SPEC §8.5 step order (Applies, then Removes), each sorted by deployment ID, on every call.
func TestSpec_17_4_DiffRulesOrder(t *testing.T) {
	want := map[uuid.UUID]contract.Digest{
		idF: digest1, // absent → Apply
		idB: digest2, // other digest → Apply
		idD: digest1, // equal, failed → Apply
		idA: digest1, // equal → nothing
	}
	have := map[uuid.UUID]plan.Actual{
		idE: actual(digest2, components{"web": contract.StateInstalled}), // not desired → Remove
		idB: actual(digest1, components{"web": contract.StateInstalled}),
		idD: actual(digest1, components{"web": contract.StateInstalled, "db": contract.StateFailed}),
		idA: actual(digest1, components{"web": contract.StateInstalled}),
		idC: actual(digest1, components{"web": contract.StateRemoving}), // not desired → Remove
	}
	wantBefore, haveBefore := maps.Clone(want), cloneHave(have)
	cmds := []plan.Command{
		apply(idB, digest2),
		apply(idD, digest1),
		apply(idF, digest1),
		remove(idC, digest1),
		remove(idE, digest2),
	}

	// Map iteration order is random, so one call could be sorted by chance.
	for i := range 50 {
		if got := plan.ForHost(want, have); !reflect.DeepEqual(got, cmds) {
			t.Fatalf("call %d: ForHost = %+v, want %+v", i, got, cmds)
		}
	}
	if !reflect.DeepEqual(want, wantBefore) || !reflect.DeepEqual(have, haveBefore) {
		t.Errorf("ForHost modified its arguments: want %+v, have %+v", want, have)
	}
}

// cloneHave copies have deeply enough to detect changes to the component maps.
func cloneHave(have map[uuid.UUID]plan.Actual) map[uuid.UUID]plan.Actual {
	out := make(map[uuid.UUID]plan.Actual, len(have))
	for id, a := range have {
		out[id] = plan.Actual{Digest: a.Digest, Components: maps.Clone(a.Components)}
	}
	return out
}
