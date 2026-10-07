package sync_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

// boltFile is a Bolt store file that a test can close and reopen, as an LO restart does. The open
// store is closed when the test ends.
type boltFile struct {
	t    *testing.T
	path string
	open *store.Bolt
}

func newBoltFile(t *testing.T) *boltFile {
	t.Helper()
	b := &boltFile{t: t, path: filepath.Join(t.TempDir(), "lo.db")}
	t.Cleanup(func() {
		if b.open != nil {
			if err := b.open.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}
	})
	return b
}

// reopen closes the store if it is open, then opens it again.
func (b *boltFile) reopen() *store.Bolt {
	b.t.Helper()
	if b.open != nil {
		if err := b.open.Close(); err != nil {
			b.t.Fatalf("Close: %v", err)
		}
		b.open = nil
	}
	s, err := store.OpenBolt(b.path)
	if err != nil {
		b.t.Fatalf("OpenBolt: %v", err)
	}
	b.open = s
	return s
}

// SPEC §17.3: "Version and ETag survive LO restart; rollback protection holds after restart."
func TestSpec_17_3_VersionAndETagSurviveRestart(t *testing.T) {
	f := newFixture(t)
	file := newBoltFile(t)
	f.useStore(file.reopen())
	f.accept(1, yamls("1", idA))
	f.accept(2, yamls("2", idA, idB))
	before := f.state()

	f.useStore(file.reopen())
	f.requireUnchanged(before)

	t.Run("stored ETag sent", func(t *testing.T) {
		f.t = t
		f.tick(losync.NotModified)
		reqs := f.co.takeRequests()
		if len(reqs) != 1 || reqs[0].Header.Get("If-None-Match") != before.ETag {
			t.Errorf("requests = %v, want one manifest request with If-None-Match %q", paths(reqs), before.ETag)
		}
	})
	for _, v := range []contract.ManifestVersion{2, 1} {
		t.Run(fmt.Sprint("version ", v, " after restart"), func(t *testing.T) {
			f.t = t
			f.co.publish(v, yamls("3", idA, idB, idC)) // other content, so another ETag
			f.useStore(file.reopen())
			f.log.Reset()

			f.tick(losync.RejectedRollback)

			if got := paths(f.co.takeRequests()); !slices.Equal(got, []string{manifestPath}) {
				t.Errorf("requests = %v, want only the manifest", got)
			}
			f.requireUnchanged(before)
			requireFields(t, f.outcomeLine(), map[string]any{
				"level": "WARN", "manifest_version": float64(v), "stored_manifest_version": 2.0,
			})
		})
	}
}

// SPEC §12: "If the LO loses its store, it re-syncs from the CO (full manifest) … The version it
// then reports is the one it re-synced against."
func TestEmptyStoreResyncs(t *testing.T) {
	f := newFixture(t)
	f.useStore(newBoltFile(t).reopen())
	f.accept(1, yamls("1", idA, idB))
	v3 := yamls("3", idA, idB)
	f.accept(3, v3)

	f.useStore(newBoltFile(t).reopen()) // the store is lost
	f.tick(losync.Accepted)

	if got := paths(f.co.takeRequests()); countPrefix(got, bundlePrefix) != 1 {
		t.Errorf("requests = %v, want the bundle, as on a first sync", got)
	}
	want := losync.State{Version: 3, ETag: contract.ETag(f.co.manifest), Desired: desired(3, v3)}
	if got := f.state(); !reflect.DeepEqual(got, want) {
		t.Errorf("state = %+v\nwant %+v", got, want)
	}
}

var errStore = errors.New("store failed")

// failingStore is a store whose method named fail returns errStore.
type failingStore struct {
	losync.Store
	fail string
}

func (s *failingStore) Load(ctx context.Context) (losync.State, error) {
	if s.fail == "Load" {
		return losync.State{}, errStore
	}
	return s.Store.Load(ctx)
}

func (s *failingStore) ReplaceDesired(ctx context.Context, desired map[uuid.UUID]losync.Desired) error {
	if s.fail == "ReplaceDesired" {
		return errStore
	}
	return s.Store.ReplaceDesired(ctx, desired)
}

func (s *failingStore) CommitVersion(ctx context.Context, v contract.ManifestVersion, etag string) error {
	if s.fail == "CommitVersion" {
		return errStore
	}
	return s.Store.CommitVersion(ctx, v, etag)
}

// SPEC §14.2: a store failure during a sync attempt ends it with no §7.4 outcome, logged at error
// level; desired state, version and ETag stay as last committed, and the next poll retries.
func TestStoreFailureHasNoOutcome(t *testing.T) {
	for _, method := range []string{"Load", "ReplaceDesired", "CommitVersion"} {
		t.Run(method, func(t *testing.T) {
			f := newFixture(t)
			mem := store.NewMemory()
			f.useStore(mem)
			v1 := yamls("1", idA, idB)
			f.accept(1, v1)
			before := f.state()
			v2 := with(v1, idB, "2")
			f.co.publish(2, v2)
			failing := &failingStore{Store: mem, fail: method}
			f.useStore(failing)

			got, err := f.sync.Tick(f.ctx)

			if !errors.Is(err, errStore) || got != "" {
				t.Fatalf("Tick = %q, %v; want no outcome and the store's error", got, err)
			}
			l := f.outcomeLine()
			if l["level"] != "ERROR" || l["outcome"] != nil {
				t.Errorf("log line = %v, want ERROR with no outcome", l)
			}
			after, err := mem.Load(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := before
			if method == "CommitVersion" { // desired state was replaced; the version was not committed
				want.Desired = desired(2, v2)
				want.Desired[idA] = before.Desired[idA]
			}
			if !reflect.DeepEqual(after, want) {
				t.Errorf("state = %+v\nwant %+v", after, want)
			}

			failing.fail = ""
			f.co.takeRequests()
			f.tick(losync.Accepted)
			if got := paths(f.co.takeRequests()); method == "CommitVersion" && !slices.Equal(got, []string{manifestPath}) {
				t.Errorf("requests = %v, want only the manifest: every digest is already held", got)
			}
			if st := f.state(); st.Version != 2 {
				t.Errorf("after the retry: Version = %d, want 2", st.Version)
			}
		})
	}
}
