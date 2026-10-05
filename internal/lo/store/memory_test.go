package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

// Memory copies state on the way in and out, so a caller never shares maps or slices with it.
func TestMemoryCopiesState(t *testing.T) {
	ctx := context.Background()
	m := store.NewMemory()
	id := uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	yaml := []byte("id: a\n")
	in := map[uuid.UUID]losync.Desired{id: {Digest: contract.DigestOf(yaml), YAML: yaml, AdoptedVersion: 1}}
	if err := m.ReplaceDesired(ctx, in); err != nil {
		t.Fatalf("ReplaceDesired: %v", err)
	}
	if err := m.CommitVersion(ctx, 1, `"etag-1"`); err != nil {
		t.Fatalf("CommitVersion: %v", err)
	}
	want := losync.State{
		Version: 1, ETag: `"etag-1"`,
		Desired: map[uuid.UUID]losync.Desired{id: {Digest: contract.DigestOf(yaml), YAML: []byte("id: a\n"), AdoptedVersion: 1}},
	}

	// Change what was passed in.
	yaml[0] = 'X'
	in[uuid.New()] = losync.Desired{}
	got, err := m.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after changing the input: Load = %+v, want %+v", got, want)
	}

	// Change what was returned.
	got.Desired[id].YAML[0] = 'X'
	got.Desired[uuid.New()] = losync.Desired{}
	again, err := m.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("after changing the output: Load = %+v, want %+v", again, want)
	}
}

func TestMemoryStartsEmpty(t *testing.T) {
	st, err := store.NewMemory().Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.Version != 0 || st.ETag != "" || len(st.Desired) != 0 {
		t.Errorf("Load = %+v, want the zero state", st)
	}
}
