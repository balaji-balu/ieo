package sync_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
	"github.com/balaji-balu/ieo/internal/lo/sync/synctest"
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

	// The stored ETag is sent after the restart.
	f.tick(losync.NotModified)
	reqs := f.co.TakeRequests()
	if len(reqs) != 1 || reqs[0].Header.Get("If-None-Match") != before.ETag {
		t.Errorf("requests = %v, want one manifest request with If-None-Match %q", paths(reqs), before.ETag)
	}

	// An equal, then a lower version, each after another restart.
	for _, v := range []contract.ManifestVersion{2, 1} {
		f.co.Publish(v, yamls("3", idA, idB, idC)) // other content, so another ETag
		f.useStore(file.reopen())
		f.log.Reset()

		f.tick(losync.RejectedRollback)

		if got := paths(f.co.TakeRequests()); !slices.Equal(got, []string{manifestPath}) {
			t.Errorf("version %d: requests = %v, want only the manifest", v, got)
		}
		f.requireUnchanged(before)
		requireFields(t, f.outcomeLine(), map[string]any{
			"level": "WARN", "manifest_version": float64(v), "stored_manifest_version": 2.0,
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

	if got := paths(f.co.TakeRequests()); countPrefix(got, bundlePrefix) != 1 {
		t.Errorf("requests = %v, want the bundle, as on a first sync", got)
	}
	want := losync.State{Version: 3, ETag: contract.ETag(f.co.ManifestBody()), Desired: desired(3, v3)}
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
			f.co.Publish(2, v2)
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
			f.co.TakeRequests()
			f.tick(losync.Accepted)
			if got := paths(f.co.TakeRequests()); method == "CommitVersion" && !slices.Equal(got, []string{manifestPath}) {
				t.Errorf("requests = %v, want only the manifest: every digest is already held", got)
			}
			if st := f.state(); st.Version != 2 {
				t.Errorf("after the retry: Version = %d, want 2", st.Version)
			}
		})
	}
}

// blockingTransport passes requests to the CO, except those for a path with the prefix block,
// which wait until their context ends. It reports each request it blocks on started.
type blockingTransport struct {
	co      *synctest.CO
	block   string
	started chan<- struct{}
}

func (b blockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(req.URL.Path, b.block) {
		return b.co.RoundTrip(req)
	}
	b.started <- struct{}{}
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// SPEC §14.2: "an attempt cut short by LO shutdown" ends with no §7.4 outcome and is not logged
// as a failure; stored state stays as last committed.
func TestShutdownDuringPollHasNoOutcome(t *testing.T) {
	for _, block := range []string{manifestPath, bundlePrefix} {
		for _, via := range []string{"Tick", "Run"} {
			t.Run(via+" blocked on "+block, func(t *testing.T) {
				started := make(chan struct{}, 1)
				f := newFixture(t)
				f.cfg.Transport = blockingTransport{co: f.co, block: block, started: started}
				f.useStore(store.NewMemory())
				f.co.Publish(1, yamls("1", idA, idB))

				ctx, cancel := context.WithCancel(context.Background())
				type ticked struct {
					outcome losync.Outcome
					err     error
				}
				done := make(chan ticked, 1)
				go func() {
					if via == "Tick" {
						o, err := f.sync.Tick(ctx)
						done <- ticked{o, err}
						return
					}
					done <- ticked{err: f.sync.Run(ctx)}
				}()
				recv(t, started, "the blocked request")
				cancel()
				got := recv(t, done, via+" to return")

				if got.outcome != "" || !errors.Is(got.err, context.Canceled) {
					t.Errorf("%s = %q, %v; want no outcome and context.Canceled", via, got.outcome, got.err)
				}
				for _, l := range f.lines() {
					if l["outcome"] != nil || l["level"] != "INFO" && l["level"] != "DEBUG" {
						t.Errorf("log line %v: the shutdown is logged as an outcome or a failure", l)
					}
				}
				if st := f.state(); st.Version != 0 || len(st.Desired) != 0 {
					t.Errorf("state = %+v, want nothing accepted", st)
				}
			})
		}
	}
}
