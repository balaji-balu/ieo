package store_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

// The layout of ADR 0014, which these tests write directly to damage a store file.
var (
	bucketMeta    = []byte("meta")
	bucketSync    = []byte("sync")
	bucketDesired = []byte("desired")
	keySchema     = []byte("schema")
	keyVersion    = []byte("version")
)

func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// rawUpdate changes the closed store file at path with fn, bypassing the store.
func rawUpdate(t *testing.T, path string, fn func(tx *bolt.Tx) error) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Update(fn); err != nil {
		t.Fatalf("update %s: %v", path, err)
	}
}

// committed returns a closed store file at path holding version 3 and two deployments.
func committed(t *testing.T) (path string, want losync.State) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "lo.db")
	b, err := store.OpenBolt(path)
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	want = losync.State{Version: 3, ETag: `"e3"`, Desired: desiredOf(2, map[uuid.UUID]string{idA: "a2", idB: "b2"})}
	replace(t, b, want.Desired)
	commit(t, b, want.Version, want.ETag)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path, want
}

// SPEC §12: "The LO MUST persist `manifestVersion` and ETag durably" — and desired state.
func TestBoltStateSurvivesReopen(t *testing.T) {
	path, want := committed(t)
	if got := load(t, openBolt(t, path)); !reflect.DeepEqual(got, want) {
		t.Errorf("after reopening: Load = %+v, want %+v", got, want)
	}
}

// A second LO on the same store file fails at startup instead of waiting for the lock.
func TestBoltRefusesSecondOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lo.db")
	openBolt(t, path)
	if b, err := store.OpenBolt(path); err == nil {
		_ = b.Close()
		t.Fatal("second OpenBolt on an open store succeeded, want an error")
	}
}

// Load fails on anything it cannot trust, and never returns an empty state instead: that would
// reset the accepted version to 0 and turn rollback protection off (SPEC §12, §15.3).
func TestBoltLoadFailsOnDamagedStore(t *testing.T) {
	tests := []struct {
		name   string
		damage func(tx *bolt.Tx) error
	}{
		{"desired record not JSON", func(tx *bolt.Tx) error {
			return tx.Bucket(bucketDesired).Put([]byte(idA.String()), []byte("{"))
		}},
		{"desired record with a bad digest", func(tx *bolt.Tx) error {
			return tx.Bucket(bucketDesired).Put([]byte(idA.String()),
				[]byte(`{"digest":"sha256:00","adoptedManifestVersion":2,"yaml":"YTI="}`))
		}},
		{"YAML does not match its digest", func(tx *bolt.Tx) error {
			k := []byte(idA.String())
			v := tx.Bucket(bucketDesired).Get(k)
			// "YTI=" is base64 of "a2"; "YTM=" of "a3".
			return tx.Bucket(bucketDesired).Put(k, []byte(strings.Replace(string(v), "YTI=", "YTM=", 1)))
		}},
		{"desired key not a UUID", func(tx *bolt.Tx) error {
			return tx.Bucket(bucketDesired).Put([]byte("not-a-uuid"), tx.Bucket(bucketDesired).Get([]byte(idA.String())))
		}},
		{"version not 8 bytes", func(tx *bolt.Tx) error {
			return tx.Bucket(bucketSync).Put(keyVersion, []byte{3})
		}},
		{"version missing", func(tx *bolt.Tx) error { return tx.Bucket(bucketSync).Delete(keyVersion) }},
		{"sync bucket missing", func(tx *bolt.Tx) error { return tx.DeleteBucket(bucketSync) }},
		{"desired bucket missing", func(tx *bolt.Tx) error { return tx.DeleteBucket(bucketDesired) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := committed(t)
			rawUpdate(t, path, tc.damage)
			b, err := store.OpenBolt(path)
			if err != nil { // the damage is past the layout check: Load must catch it
				t.Fatalf("OpenBolt: %v; want it to open and Load to fail", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			if st, err := b.Load(t.Context()); err == nil {
				t.Errorf("Load = %+v, nil; want an error", st)
			}
		})
	}
}

// A store file written by a newer layout, or one that is not an LO store, is refused (ADR 0014).
func TestBoltRefusesUnknownLayout(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"newer schema", func(t *testing.T, path string) {
			committedAt(t, path)
			rawUpdate(t, path, func(tx *bolt.Tx) error { return tx.Bucket(bucketMeta).Put(keySchema, u64(3)) })
		}},
		{"version 1 that already has a version 2 bucket", func(t *testing.T, path string) {
			committedAt(t, path) // version 2: it has the hosts bucket, which version 1 never had
			rawUpdate(t, path, func(tx *bolt.Tx) error { return tx.Bucket(bucketMeta).Put(keySchema, u64(1)) })
		}},
		{"schema not 8 bytes", func(t *testing.T, path string) {
			committedAt(t, path)
			rawUpdate(t, path, func(tx *bolt.Tx) error { return tx.Bucket(bucketMeta).Put(keySchema, []byte{1}) })
		}},
		{"no meta bucket but other buckets", func(t *testing.T, path string) {
			rawUpdate(t, path, func(tx *bolt.Tx) error {
				_, err := tx.CreateBucket([]byte("deployments")) // e.g. the legacy boltstore
				return err
			})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lo.db")
			tc.setup(t, path)
			if b, err := store.OpenBolt(path); err == nil {
				_ = b.Close()
				t.Fatal("OpenBolt succeeded, want an error")
			}
		})
	}
}

// committedAt creates a store at path holding one committed version.
func committedAt(t *testing.T, path string) {
	t.Helper()
	b, err := store.OpenBolt(path)
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	commit(t, b, 1, `"e1"`)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// The store holds deployment YAML, so only its owner may read it.
func TestBoltFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix file modes")
	}
	path := filepath.Join(t.TempDir(), "lo.db")
	openBolt(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("store file mode = %v, want 0600", mode)
	}
}
