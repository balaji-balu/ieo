package store_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

var (
	idA = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	idB = uuid.MustParse("0b0b0b0b-0000-4000-8000-00000000000b")
)

// each runs test against every store backend, each test with an empty store that lives until t
// ends: the rules of losync.Store hold for all of them.
func each(t *testing.T, test func(t *testing.T, newStore func(t *testing.T) losync.Store)) {
	t.Run("Memory", func(t *testing.T) {
		test(t, func(*testing.T) losync.Store { return store.NewMemory() })
	})
	t.Run("Bolt", func(t *testing.T) {
		test(t, func(t *testing.T) losync.Store { return openBolt(t, filepath.Join(t.TempDir(), "lo.db")) })
	})
}

// openBolt opens the Bolt store at path, closed when t ends.
func openBolt(t *testing.T, path string) *store.Bolt {
	t.Helper()
	b, err := store.OpenBolt(path)
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return b
}

func desiredOf(v contract.ManifestVersion, yamls map[uuid.UUID]string) map[uuid.UUID]losync.Desired {
	out := map[uuid.UUID]losync.Desired{}
	for id, y := range yamls {
		out[id] = losync.Desired{Digest: contract.DigestOf([]byte(y)), YAML: []byte(y), AdoptedVersion: v}
	}
	return out
}

func load(t *testing.T, s losync.Store) losync.State {
	t.Helper()
	st, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return st
}

func replace(t *testing.T, s losync.Store, desired map[uuid.UUID]losync.Desired) {
	t.Helper()
	if err := s.ReplaceDesired(context.Background(), desired); err != nil {
		t.Fatalf("ReplaceDesired: %v", err)
	}
}

func commit(t *testing.T, s losync.Store, v contract.ManifestVersion, etag string) {
	t.Helper()
	if err := s.CommitVersion(context.Background(), v, etag); err != nil {
		t.Fatalf("CommitVersion: %v", err)
	}
}

func TestStoreStartsEmpty(t *testing.T) {
	each(t, func(t *testing.T, newStore func(*testing.T) losync.Store) {
		want := losync.State{Desired: map[uuid.UUID]losync.Desired{}}
		if got := load(t, newStore(t)); !reflect.DeepEqual(got, want) {
			t.Errorf("Load = %+v, want the zero state", got)
		}
	})
}

// A store copies state on the way in and out, so a caller never shares maps or slices with it.
func TestStoreCopiesState(t *testing.T) {
	each(t, func(t *testing.T, newStore func(*testing.T) losync.Store) {
		s := newStore(t)
		yaml := []byte("id: a\n")
		in := map[uuid.UUID]losync.Desired{idA: {Digest: contract.DigestOf(yaml), YAML: yaml, AdoptedVersion: 1}}
		replace(t, s, in)
		commit(t, s, 1, `"etag-1"`)
		want := losync.State{Version: 1, ETag: `"etag-1"`, Desired: desiredOf(1, map[uuid.UUID]string{idA: "id: a\n"})}

		yaml[0] = 'X'
		in[idB] = losync.Desired{}
		got := load(t, s)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("after changing the input: Load = %+v, want %+v", got, want)
		}

		got.Desired[idA].YAML[0] = 'X'
		got.Desired[idB] = losync.Desired{}
		if again := load(t, s); !reflect.DeepEqual(again, want) {
			t.Fatalf("after changing the output: Load = %+v, want %+v", again, want)
		}
	})
}

// SPEC §8.2 step 6: desired state is replaced as a whole, so a deployment left out is gone.
func TestReplaceDesiredReplacesWholeSet(t *testing.T) {
	each(t, func(t *testing.T, newStore func(*testing.T) losync.Store) {
		s := newStore(t)
		replace(t, s, desiredOf(1, map[uuid.UUID]string{idA: "a1", idB: "b1"}))
		want := desiredOf(2, map[uuid.UUID]string{idB: "b2"})
		replace(t, s, want)
		if got := load(t, s).Desired; !reflect.DeepEqual(got, want) {
			t.Errorf("Desired = %+v, want %+v", got, want)
		}
		replace(t, s, map[uuid.UUID]losync.Desired{})
		if got := load(t, s).Desired; len(got) != 0 {
			t.Errorf("after replacing with nothing: Desired = %+v, want empty", got)
		}
	})
}

// SPEC §8.2 steps 6 and 8 are separate writes: each changes only its own fields.
func TestVersionAndDesiredAreWrittenSeparately(t *testing.T) {
	each(t, func(t *testing.T, newStore func(*testing.T) losync.Store) {
		s := newStore(t)
		desired := desiredOf(1, map[uuid.UUID]string{idA: "a1"})
		replace(t, s, desired)
		if st := load(t, s); st.Version != 0 || st.ETag != "" {
			t.Errorf("after ReplaceDesired: version %d, ETag %q; want neither written", st.Version, st.ETag)
		}
		commit(t, s, 7, `"e7"`)
		want := losync.State{Version: 7, ETag: `"e7"`, Desired: desired}
		if got := load(t, s); !reflect.DeepEqual(got, want) {
			t.Errorf("after CommitVersion: Load = %+v, want %+v", got, want)
		}
	})
}
