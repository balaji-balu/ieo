package store_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/store"
)

// The layout of ADR 0016, which these tests read and write directly.
var (
	bucketMeta       = []byte("meta")
	bucketApplied    = []byte("applied")
	bucketComponents = []byte("component_states")
	keySchema        = []byte("schema")
)

var (
	idA     = uuid.MustParse("0b4f5c1e-6a0e-4d7c-9a51-3f2f8c1d2e01")
	idB     = uuid.MustParse("7d1c2b3a-4e5f-4a6b-8c7d-9e0f1a2b3c4d")
	digestA = contract.DigestOf([]byte("a"))
	digestB = contract.DigestOf([]byte("b"))
	ctx     = context.Background()
)

func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func open(t *testing.T, dataDir string) *store.Bolt {
	t.Helper()
	b, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b
}

func closeStore(t *testing.T, b *store.Bolt) {
	t.Helper()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func load(t *testing.T, b *store.Bolt) map[uuid.UUID]store.Deployment {
	t.Helper()
	got, err := b.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return got
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// rawUpdate changes the closed store file in dataDir with fn, bypassing the store.
func rawUpdate(t *testing.T, dataDir string, fn func(tx *bolt.Tx) error) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(dataDir, store.StateFile), 0o600, nil)
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Update(fn); err != nil {
		t.Fatalf("update state.db: %v", err)
	}
}

// rawView reads the closed store file in dataDir with fn.
func rawView(t *testing.T, dataDir string, fn func(tx *bolt.Tx) error) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(dataDir, store.StateFile), 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.View(fn); err != nil {
		t.Fatalf("view state.db: %v", err)
	}
}

// What the EN applied and reported survives a restart (SPEC §4.1.12, §12).
func TestStoreSurvivesReopen(t *testing.T) {
	dataDir := t.TempDir()
	b := open(t, dataDir)
	failed := contract.ComponentStatus{Name: "db/primary", State: contract.StateFailed,
		Error: &contract.StatusError{Code: "IEO-COMPOSE-FAILED", Source: "db/primary", Message: "exit 1"}}
	must(t, b.PutApplied(ctx, idA, store.Applied{Digest: digestA, ComposeProjects: []string{"p1", "p2"}}))
	must(t, b.PutComponentStatus(ctx, idA, contract.ComponentStatus{Name: "web", State: contract.StateInstalled}))
	must(t, b.PutComponentStatus(ctx, idA, failed))
	must(t, b.PutApplied(ctx, idB, store.Applied{Digest: digestB, ComposeProjects: []string{}}))
	closeStore(t, b)

	b = open(t, dataDir)
	defer closeStore(t, b)
	want := map[uuid.UUID]store.Deployment{
		idA: {
			Applied: store.Applied{Digest: digestA, ComposeProjects: []string{"p1", "p2"}},
			Components: map[string]contract.ComponentStatus{
				"web":        {Name: "web", State: contract.StateInstalled},
				"db/primary": failed,
			},
		},
		idB: {
			Applied:    store.Applied{Digest: digestB, ComposeProjects: []string{}},
			Components: map[string]contract.ComponentStatus{},
		},
	}
	if got := load(t, b); !reflect.DeepEqual(got, want) {
		t.Errorf("Load after reopen = %+v, want %+v", got, want)
	}
	if runtime.GOOS != "windows" {
		assertMode(t, filepath.Join(dataDir, store.StateFile), 0o600)
	}
}

// PutApplied replaces the Applied record and keeps the component states; PutComponentStatus
// replaces one component's status (SPEC §8.9 steps 4.4 and 6).
func TestStorePutReplaces(t *testing.T) {
	b := open(t, t.TempDir())
	defer closeStore(t, b)
	must(t, b.PutApplied(ctx, idA, store.Applied{Digest: digestA, ComposeProjects: []string{"p1"}}))
	must(t, b.PutComponentStatus(ctx, idA, contract.ComponentStatus{Name: "web", State: contract.StateInstalling}))
	must(t, b.PutApplied(ctx, idA, store.Applied{Digest: digestB, ComposeProjects: []string{"p1", "p2"}}))
	must(t, b.PutComponentStatus(ctx, idA, contract.ComponentStatus{Name: "web", State: contract.StateInstalled}))

	want := map[uuid.UUID]store.Deployment{idA: {
		Applied:    store.Applied{Digest: digestB, ComposeProjects: []string{"p1", "p2"}},
		Components: map[string]contract.ComponentStatus{"web": {Name: "web", State: contract.StateInstalled}},
	}}
	if got := load(t, b); !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

// An Apply in flight writes `installing` before it records any project (SPEC §8.9 step 4): Load
// leaves it out while it runs, and Open deletes it after a restart (ADR 0016).
func TestStoreComponentStatesWithoutApplied(t *testing.T) {
	dataDir := t.TempDir()
	b := open(t, dataDir)
	must(t, b.PutApplied(ctx, idA, store.Applied{Digest: digestA, ComposeProjects: []string{"p1"}}))
	must(t, b.PutComponentStatus(ctx, idA, contract.ComponentStatus{Name: "web", State: contract.StateInstalled}))
	must(t, b.PutComponentStatus(ctx, idB, contract.ComponentStatus{Name: "web", State: contract.StateInstalling}))
	if got := load(t, b); len(got) != 1 || got[idB].Components != nil {
		t.Errorf("Load = %+v, want only %s", got, idA)
	}
	closeStore(t, b)

	closeStore(t, open(t, dataDir))
	rawView(t, dataDir, func(tx *bolt.Tx) error {
		components := tx.Bucket(bucketComponents)
		if components.Bucket([]byte(idB.String())) != nil {
			t.Errorf("Open kept the component states of %s, which has no applied record", idB)
		}
		if components.Bucket([]byte(idA.String())) == nil {
			t.Errorf("Open deleted the component states of %s, which has an applied record", idA)
		}
		return nil
	})
}

// Remove deletes a deployment's applied record and component states together; deleting an unknown
// deployment succeeds (SPEC §8.9 Remove, §7.6).
func TestStoreDeleteDeployment(t *testing.T) {
	b := open(t, t.TempDir())
	defer closeStore(t, b)
	must(t, b.PutApplied(ctx, idA, store.Applied{Digest: digestA, ComposeProjects: []string{"p1"}}))
	must(t, b.PutComponentStatus(ctx, idA, contract.ComponentStatus{Name: "web", State: contract.StateRemoving}))
	must(t, b.PutApplied(ctx, idB, store.Applied{Digest: digestB, ComposeProjects: []string{"p2"}}))

	must(t, b.DeleteDeployment(ctx, idA))
	must(t, b.DeleteDeployment(ctx, idA))
	must(t, b.DeleteDeployment(ctx, uuid.MustParse("00000000-0000-4000-8000-000000000000")))
	got := load(t, b)
	if _, ok := got[idA]; ok || len(got) != 1 {
		t.Errorf("Load after delete = %+v, want only %s", got, idB)
	}
	// A later status for the deleted deployment must not bring back the old components.
	must(t, b.PutApplied(ctx, idA, store.Applied{Digest: digestA}))
	if c := load(t, b)[idA].Components; len(c) != 0 {
		t.Errorf("components after delete and re-apply = %+v, want none", c)
	}
}

// A new file gets every bucket of layout version 1 (ADR 0016).
func TestStoreNewFileLayout(t *testing.T) {
	dataDir := t.TempDir()
	closeStore(t, open(t, dataDir))
	rawView(t, dataDir, func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		if meta == nil {
			t.Fatal("no meta bucket")
		}
		if got := meta.Get(keySchema); !bytes.Equal(got, u64(1)) {
			t.Errorf("schema = %x, want %x", got, u64(1))
		}
		for _, name := range [][]byte{bucketApplied, bucketComponents} {
			if tx.Bucket(name) == nil {
				t.Errorf("no %s bucket", name)
			}
		}
		return nil
	})
}

// A file that is not an EN store of this layout is refused at open, with an error naming it, and
// left as it is (ADR 0016).
func TestStoreRefusesUnknownLayout(t *testing.T) {
	cases := []struct {
		name string
		fn   func(tx *bolt.Tx) error
	}{
		{"buckets but no meta", func(tx *bolt.Tx) error {
			_, err := tx.CreateBucket([]byte("desired"))
			return err
		}},
		{"other schema", func(tx *bolt.Tx) error {
			meta, err := tx.CreateBucket(bucketMeta)
			if err != nil {
				return err
			}
			return meta.Put(keySchema, u64(2))
		}},
		{"schema not 8 bytes", func(tx *bolt.Tx) error {
			meta, err := tx.CreateBucket(bucketMeta)
			if err != nil {
				return err
			}
			return meta.Put(keySchema, []byte{1})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			rawUpdate(t, dataDir, tc.fn)
			path := filepath.Join(dataDir, store.StateFile)
			before := readFile(t, path)
			b, err := store.Open(dataDir)
			if err == nil {
				_ = b.Close()
				t.Fatal("Open succeeded, want an error")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name %s", err, path)
			}
			if readFile(t, path) != before {
				t.Error("Open changed the refused file")
			}
		})
	}
}

// A record that does not decode fails Load; the store never returns a partial result (ADR 0016).
func TestStoreLoadFailsOnDamagedRecord(t *testing.T) {
	applied := func(k, v string) func(tx *bolt.Tx) error {
		return func(tx *bolt.Tx) error { return tx.Bucket(bucketApplied).Put([]byte(k), []byte(v)) }
	}
	component := func(id, k, v string) func(tx *bolt.Tx) error {
		return func(tx *bolt.Tx) error {
			bk, err := tx.Bucket(bucketComponents).CreateBucketIfNotExists([]byte(id))
			if err != nil {
				return err
			}
			return bk.Put([]byte(k), []byte(v))
		}
	}
	good := `{"digest":"` + digestA.String() + `","composeProjects":["p1"]}`
	cases := []struct {
		name string
		fn   func(tx *bolt.Tx) error
	}{
		{"applied value not JSON", applied(idB.String(), "{")},
		{"applied key not a deployment ID", applied("not-a-uuid", good)},
		{"applied key not lowercase", applied(strings.ToUpper(idB.String()), good)},
		{"applied digest invalid", applied(idB.String(), `{"digest":"md5:00","composeProjects":[]}`)},
		{"applied digest missing", applied(idB.String(), `{"composeProjects":[]}`)},
		{"component value not JSON", component(idA.String(), "web", "{")},
		{"component state unknown", component(idA.String(), "web", `{"name":"web","state":"running"}`)},
		{"component key differs from name", component(idA.String(), "web", `{"name":"db","state":"installed"}`)},
		{"component entry not a bucket", func(tx *bolt.Tx) error {
			return tx.Bucket(bucketComponents).Put([]byte(idA.String()), []byte("x"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			b := open(t, dataDir)
			must(t, b.PutApplied(ctx, idA, store.Applied{Digest: digestA, ComposeProjects: []string{"p1"}}))
			closeStore(t, b)
			rawUpdate(t, dataDir, tc.fn)

			b, err := store.Open(dataDir)
			if err != nil { // refusing at open is as good as failing Load
				return
			}
			defer closeStore(t, b)
			got, err := b.Load(ctx)
			if err == nil {
				t.Fatalf("Load = %+v, want an error", got)
			}
			if got != nil {
				t.Errorf("Load returned %+v with its error, want nil", got)
			}
		})
	}
}

// A second EN on the same data directory fails at startup instead of waiting for the lock
// (ADR 0016).
func TestStoreSecondOpenFails(t *testing.T) {
	dataDir := t.TempDir()
	b := open(t, dataDir)
	defer closeStore(t, b)
	if b2, err := store.Open(dataDir); err == nil {
		_ = b2.Close()
		t.Fatal("second Open on an open store succeeded, want an error")
	}
}

// The data directory must exist: HostID creates it before the store opens (SPEC §16.7).
func TestStoreOpenMissingDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "missing")
	if b, err := store.Open(dataDir); err == nil {
		_ = b.Close()
		t.Fatal("Open of a missing directory succeeded, want an error")
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Errorf("Open created the data directory: %v", err)
	}
}

// Get returns one deployment as Load would, and nothing for a deployment without an applied
// record (SPEC §4.1.12, ADR 0016).
func TestStoreGet(t *testing.T) {
	b := open(t, t.TempDir())
	defer closeStore(t, b)
	must(t, b.PutApplied(ctx, idA, store.Applied{Digest: digestA}))
	must(t, b.PutComponentStatus(ctx, idA, contract.ComponentStatus{Name: "web", State: contract.StateInstalled}))
	must(t, b.PutComponentStatus(ctx, idA, contract.ComponentStatus{
		Name: "a/b", State: contract.StateFailed, Error: &contract.StatusError{Code: "IEO-PULL-FAILED", Source: "a/b", Message: "m"},
	}))
	must(t, b.PutComponentStatus(ctx, idB, contract.ComponentStatus{Name: "web", State: contract.StateInstalling}))

	got, ok, err := b.Get(ctx, idA)
	if err != nil || !ok {
		t.Fatalf("Get(%s) = ok %v, error %v", idA, ok, err)
	}
	if want := load(t, b)[idA]; !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %+v, want what Load returns: %+v", got, want)
	}
	if len(got.Components) != 2 || got.Applied.ComposeProjects == nil {
		t.Errorf("Get = %+v, want two components and an empty project list", got)
	}
	for name, id := range map[string]uuid.UUID{"states but no applied record": idB, "unknown": uuid.New()} {
		if d, ok, err := b.Get(ctx, id); err != nil || ok || d.Components != nil {
			t.Errorf("Get of a deployment with %s = %+v, ok %v, error %v; want nothing", name, d, ok, err)
		}
	}
}
