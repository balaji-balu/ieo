package reconciler_test

package reconciler_test

import (
	"testing"
	"path/filepath"
	"rec/store"
	"rec/models"
	"rec/reconciler"
)

// temp store helper
func tempStore(t *testing.T) *store.StateStore {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := store.NewStateStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create temp store: %v", err)
	}

	// Add two hosts
	hosts := map[string]models.Host{
		"hostAA": {ID: "hostAA", Alive: true},
		"hostBB": {ID: "hostBB", Alive: false},
	}
	for id, host := range hosts {
		if err := s.SaveState([]string{"hosts"}, id, host); err != nil {
			t.Fatalf("failed to insert hosts: %v", err)
		}
	}

	t.Cleanup(func() { s.Close() })
	return s
}

// Compare operations ignoring timestamps, randomness.
func opNames(ops []models.DiffOp) []string {
	out := make([]string, 0, len(ops))
	for _, o := range ops {
		out = append(out, string(o.Action))
	}
	return out
}

func Test_Reconciler_Table(t *testing.T) {

	tests := []struct {
		name      string
		desired   models.App
		depid     string
		wantOps   []string // expected list of op.Action
	}{
		{
			name:  "Add app v2 fresh",
			depid: "deploy-1",
			desired: models.App{
				ID:      "app1",
				Version: "v2",
				Components: map[string]models.Component{
					"comp1": {Name: "comp1", Version: "v2", Content: "hello-world-v2"},
					"comp2": {Name: "comp2", Version: "v1", Content: "utility-v1"},
				},
			},
			wantOps: []string{
				"add_app",
				//"add_comp",
				//"add_comp",
			},
		},
		{
			name:  "Update app v3 → change both components",
			depid: "deploy-2",
			desired: models.App{
				ID:      "app1",
				Version: "v3",
				Components: map[string]models.Component{
					"comp1": {Name: "comp1", Version: "v3", Content: "hello-world-v3"},
					"comp2": {Name: "comp2", Version: "v3", Content: "utility-v3"},
				},
			},
			wantOps: []string{
				"update_app",
				//"add_comp",
				//"add_comp",
			},
		},
		{
			name:  "No-op when desired == actual",
			depid: "deploy-3",
			desired: models.App{
				ID:      "app1",
				Version: "v3",
				Components: map[string]models.Component{
					"comp1": {Name: "comp1", Version: "v3", Content: "hello-world-v3"},
					"comp2": {Name: "comp2", Version: "v3", Content: "utility-v3"},
				},
			},
			wantOps: []string{},
		},
		{
			name:  "Update one component only (comp1 v4)",
			depid: "deploy-4",
			desired: models.App{
				ID:      "app1",
				Version: "v3",
				Components: map[string]models.Component{
					"comp1": {Name: "comp1", Version: "v4", Content: "hello-world-v4"},
					"comp2": {Name: "comp2", Version: "v3", Content: "utility-v3"},
				},
			},
			wantOps: []string{
				"update_comp",
			},
		},
		{
			name:  "Remove comp2",
			depid: "deploy-5",
			desired: models.App{
				ID:      "app1",
				Version: "v3",
				Components: map[string]models.Component{
					"comp1": {Name: "comp1", Version: "v3", Content: "hello-world-v3"},
				},
			},
			wantOps: []string{
				"update_comp",
				"remove_comp",
			},
		},
		{
			name:  "Remove entire app",
			depid: "deploy-6",
			desired: models.App{
				ID:      "",
				Version: "",
				Components: map[string]models.Component{},
			},
			wantOps: []string{
				"remove_app",
			},
		},
	}

	s := tempStore(t)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			// Save desired state
			path := reconciler.PathForDesired(tc.depid)
			if err := s.SaveState(path, "app", tc.desired); err != nil {
				t.Fatalf("failed to save desired: %v", err)
			}

			ops := reconciler.ReconcileMulti(s, tc.depid, 2, true)
			got := opNames(ops)

			if len(got) != len(tc.wantOps) {
				t.Fatalf("expected ops=%v, got=%v", tc.wantOps, got)
			}

			for i := range got {
				if got[i] != tc.wantOps[i] {
					t.Fatalf("expected op=%s, got=%s", tc.wantOps[i], got[i])
				}
			}

			// OPTIONAL STRONGER VALIDATION:
			// Load actual state and validate version/components
			// Only if you want that added — I can generate it.
		})
	}
}
