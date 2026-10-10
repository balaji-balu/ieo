package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

// The buckets layout version 2 adds (ADR 0015), which these tests write directly.
var (
	bucketHosts  = []byte("hosts")
	bucketActual = []byte("actual")
)

const (
	host1 = contract.HostID("host-01")
	host2 = contract.HostID("host-02")
)

var (
	reportedAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	digestA    = contract.DigestOf([]byte("a"))
	digestB    = contract.DigestOf([]byte("b"))
)

// hostStore is the part of a store that keeps hosts and their actual state.
type hostStore interface {
	LoadHosts(ctx context.Context) (map[contract.HostID]store.HostState, error)
	PutActual(ctx context.Context, host contract.HostID, a store.HostActual) error
}

// eachHostStore runs test against every store backend, each test with an empty store that lives
// until t ends.
func eachHostStore(t *testing.T, test func(t *testing.T, s hostStore)) {
	t.Run("Memory", func(t *testing.T) { test(t, store.NewMemory()) })
	t.Run("Bolt", func(t *testing.T) { test(t, openBolt(t, filepath.Join(t.TempDir(), "lo.db"))) })
}

func loadHosts(t *testing.T, s hostStore) map[contract.HostID]store.HostState {
	t.Helper()
	hosts, err := s.LoadHosts(t.Context())
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	return hosts
}

func putActual(t *testing.T, s hostStore, host contract.HostID, a store.HostActual) {
	t.Helper()
	if err := s.PutActual(t.Context(), host, a); err != nil {
		t.Fatalf("PutActual(%s): %v", host, err)
	}
}

// actualOf returns actual state reported at reportedAt.
func actualOf(deployments ...contract.InventoryDeployment) store.HostActual {
	if deployments == nil {
		deployments = []contract.InventoryDeployment{}
	}
	return store.HostActual{ReportedAt: reportedAt, Deployments: deployments}
}

// twoDeployments is actual state with a failed component, an error and a deployment with no
// component.
func twoDeployments() store.HostActual {
	return actualOf(
		contract.InventoryDeployment{DeploymentID: idA, Digest: digestA, Components: []contract.ComponentStatus{
			{Name: "web", State: contract.StateInstalled},
			{Name: "db/main", State: contract.StateFailed, Error: &contract.StatusError{
				Code: "IEO-PULL-FAILED", Source: "db/main", Message: "registry unreachable",
			}},
		}},
		contract.InventoryDeployment{DeploymentID: idB, Digest: digestB, Components: []contract.ComponentStatus{}},
	)
}

// newHost is the host record the first PutActual creates.
func newHost(a store.HostActual) store.HostState {
	return store.HostState{Host: store.Host{Labels: map[string]string{}}, Actual: &a}
}

// The zero Memory is an empty store, as it was before it kept hosts.
func TestMemoryZeroValueKeepsHosts(t *testing.T) {
	var m store.Memory
	putActual(t, &m, host1, actualOf())
	if got := loadHosts(t, &m); len(got) != 1 {
		t.Errorf("LoadHosts = %+v, want host-01", got)
	}
}

func TestHostsStartEmpty(t *testing.T) {
	eachHostStore(t, func(t *testing.T, s hostStore) {
		if got := loadHosts(t, s); len(got) != 0 {
			t.Errorf("LoadHosts = %+v, want no hosts", got)
		}
	})
}

// SPEC §4.1.11: `hosts` and `actual` are kept per host, with every component's state and error.
func TestPutActualRoundTrips(t *testing.T) {
	eachHostStore(t, func(t *testing.T, s hostStore) {
		putActual(t, s, host1, twoDeployments())
		putActual(t, s, host2, actualOf())
		want := map[contract.HostID]store.HostState{host1: newHost(twoDeployments()), host2: newHost(actualOf())}
		if got := loadHosts(t, s); !reflect.DeepEqual(got, want) {
			t.Errorf("LoadHosts = %+v\nwant %+v", got, want)
		}
	})
}

// SPEC §4.1.11: actual state is "replaced by each inventory", so a deployment left out is gone.
func TestPutActualReplacesWholeRecord(t *testing.T) {
	eachHostStore(t, func(t *testing.T, s hostStore) {
		putActual(t, s, host1, twoDeployments())
		putActual(t, s, host2, twoDeployments())
		later := store.HostActual{ReportedAt: reportedAt.Add(time.Minute), Deployments: []contract.InventoryDeployment{
			{DeploymentID: idB, Digest: digestA, Components: []contract.ComponentStatus{{Name: "web", State: contract.StatePending}}},
		}}
		putActual(t, s, host1, later)
		want := map[contract.HostID]store.HostState{host1: newHost(later), host2: newHost(twoDeployments())}
		if got := loadHosts(t, s); !reflect.DeepEqual(got, want) {
			t.Errorf("LoadHosts = %+v\nwant %+v", got, want)
		}
	})
}

// A store copies state on the way in and out, so a caller never shares maps or slices with it.
func TestHostStoreCopiesState(t *testing.T) {
	eachHostStore(t, func(t *testing.T, s hostStore) {
		in := twoDeployments()
		putActual(t, s, host1, in)
		want := map[contract.HostID]store.HostState{host1: newHost(twoDeployments())}

		in.Deployments[0].Components[0].State = contract.StateFailed
		in.Deployments[0].Components[1].Error.Code = "changed"
		in.Deployments[1].DeploymentID = idA
		got := loadHosts(t, s)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("after changing the input: LoadHosts = %+v\nwant %+v", got, want)
		}

		got[host1].Actual.Deployments[0].Components[1].Error.Code = "changed"
		got[host1].Actual.Deployments[1].Digest = digestA
		got[host1].Host.Labels["line"] = "2"
		got[host2] = store.HostState{}
		if again := loadHosts(t, s); !reflect.DeepEqual(again, want) {
			t.Fatalf("after changing the output: LoadHosts = %+v\nwant %+v", again, want)
		}
	})
}

// Times come back in UTC whatever zone they were put in, so both backends return the same value.
func TestPutActualKeepsTheInstantInUTC(t *testing.T) {
	eachHostStore(t, func(t *testing.T, s hostStore) {
		local := reportedAt.In(time.FixedZone("IST", 5*3600+1800))
		putActual(t, s, host1, store.HostActual{ReportedAt: local})
		got := loadHosts(t, s)[host1].Actual
		if got == nil || !reflect.DeepEqual(got.ReportedAt, reportedAt) {
			t.Errorf("Actual = %+v, want ReportedAt %v in UTC", got, reportedAt)
		}
	})
}

// PutActual never writes what LoadHosts would refuse (ADR 0015).
func TestPutActualRefusesInvalidState(t *testing.T) {
	component := func(name string, state contract.ComponentState) contract.InventoryDeployment {
		return contract.InventoryDeployment{DeploymentID: idA, Digest: digestA, Components: []contract.ComponentStatus{{Name: name, State: state}}}
	}
	tests := []struct {
		name string
		host contract.HostID
		a    store.HostActual
	}{
		{"host ID with a slash", "site/host", actualOf()},
		{"empty host ID", "", actualOf()},
		{"no report time", host1, store.HostActual{}},
		{"digest not sha256", host1, actualOf(contract.InventoryDeployment{DeploymentID: idA, Digest: "md5:00"})},
		{"deployment without an ID", host1, actualOf(contract.InventoryDeployment{Digest: digestA})},
		{"unknown state", host1, actualOf(component("web", "running"))},
		{"component without a name", host1, actualOf(component("", contract.StateInstalled))},
		{"deployment listed twice", host1, actualOf(component("web", contract.StateInstalled), component("db", contract.StateInstalled))},
		{"component listed twice", host1, actualOf(contract.InventoryDeployment{DeploymentID: idA, Digest: digestA, Components: []contract.ComponentStatus{
			{Name: "web", State: contract.StateInstalled}, {Name: "web", State: contract.StateFailed},
		}})},
	}
	eachHostStore(t, func(t *testing.T, s hostStore) {
		for _, tc := range tests {
			if err := s.PutActual(t.Context(), tc.host, tc.a); err == nil {
				t.Errorf("%s: PutActual succeeded, want an error", tc.name)
			}
		}
		if got := loadHosts(t, s); len(got) != 0 {
			t.Errorf("after refused writes: LoadHosts = %+v, want no hosts", got)
		}
	})
}

// SPEC §4.1.11: `hosts` and `actual` are durable.
func TestBoltHostsSurviveReopen(t *testing.T) {
	path := hostsAt(t)
	want := map[contract.HostID]store.HostState{host1: newHost(twoDeployments())}
	if got := loadHosts(t, openBolt(t, path)); !reflect.DeepEqual(got, want) {
		t.Errorf("after reopening: LoadHosts = %+v\nwant %+v", got, want)
	}
}

// hostsAt returns a closed store file in which host1 reported twoDeployments.
func hostsAt(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lo.db")
	b, err := store.OpenBolt(path)
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	putActual(t, b, host1, twoDeployments())
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

// in returns the bucket name of tx, or an error if the file has none: the layout is not version 2.
func in(tx *bolt.Tx, name []byte) (*bolt.Bucket, error) {
	b := tx.Bucket(name)
	if b == nil {
		return nil, fmt.Errorf("the store file has no %s bucket", name)
	}
	return b, nil
}

// put writes one raw record.
func put(bucket []byte, key, value string) func(tx *bolt.Tx) error {
	return func(tx *bolt.Tx) error {
		b, err := in(tx, bucket)
		if err != nil {
			return err
		}
		return b.Put([]byte(key), []byte(value))
	}
}

// The first PutActual for a host adds the host; later ones leave what the store holds about the
// host itself (capabilities, labels, last heartbeat) as it is. Roadmap slices H and I write those.
func TestBoltPutActualKeepsTheHostRecord(t *testing.T) {
	path := hostsAt(t)
	rawUpdate(t, path, put(bucketHosts, "host-01", `{
		"capabilities": {"id": "site-1/host-01", "vendor": "v", "modelNumber": "m", "serialNumber": "s", "memory": "8Gi"},
		"labels": {"line": "2"},
		"lastHeartbeatAt": "2026-10-10T17:29:50+05:30"}`))
	b := openBolt(t, path)
	putActual(t, b, host1, actualOf())

	id, err := contract.ParseDeviceID("site-1/host-01")
	if err != nil {
		t.Fatal(err)
	}
	empty := actualOf()
	want := map[contract.HostID]store.HostState{host1: {
		Host: store.Host{
			Capabilities:    &contract.DeviceCapabilities{ID: id, Vendor: "v", ModelNumber: "m", SerialNumber: "s", Memory: "8Gi"},
			Labels:          map[string]string{"line": "2"},
			LastHeartbeatAt: time.Date(2026, 10, 10, 11, 59, 50, 0, time.UTC),
		},
		Actual: &empty,
	}}
	if got := loadHosts(t, b); !reflect.DeepEqual(got, want) {
		t.Errorf("LoadHosts = %+v\nwant %+v", got, want)
	}
}

// dump returns every bucket of the closed store file at path, as bucket → key → value.
func dump(t *testing.T, path string) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true}) // reading must not change the file
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	err = db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			records := map[string]string{}
			out[string(name)] = records
			return b.ForEach(func(k, v []byte) error {
				records[string(k)] = string(v)
				return nil
			})
		})
	})
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return out
}

// layout1 writes at path a store file as roadmap slice C wrote it (layout version 1, ADR 0014),
// without using the store, and returns the state it holds.
func layout1(t *testing.T, path string) losync.State {
	t.Helper()
	want := losync.State{Version: 3, ETag: `"e3"`, Desired: desiredOf(2, map[uuid.UUID]string{idA: "a2", idB: "b2"})}
	rawUpdate(t, path, func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucket(bucketMeta)
		if err != nil {
			return err
		}
		syncBucket, err := tx.CreateBucket(bucketSync)
		if err != nil {
			return err
		}
		desired, err := tx.CreateBucket(bucketDesired)
		if err != nil {
			return err
		}
		for id, d := range want.Desired {
			v, err := json.Marshal(map[string]any{"digest": d.Digest, "adoptedManifestVersion": d.AdoptedVersion, "yaml": d.YAML})
			if err != nil {
				return err
			}
			if err := desired.Put([]byte(id.String()), v); err != nil {
				return err
			}
		}
		return errors.Join(
			meta.Put(keySchema, u64(1)),
			syncBucket.Put(keyVersion, u64(uint64(want.Version))),
			syncBucket.Put([]byte("etag"), []byte(want.ETag)),
		)
	})
	return want
}

// A store file of layout version 1 (roadmap slice C) is migrated when it is opened: it gets the
// empty `hosts` and `actual` buckets, and keeps everything it held (ADR 0015).
func TestBoltMigratesLayoutVersion1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lo.db")
	want := layout1(t, path)
	before := dump(t, path)

	b, err := store.OpenBolt(path)
	if err != nil {
		t.Fatalf("OpenBolt of a version 1 file: %v", err)
	}
	if got := b.MigratedFrom(); got != 1 {
		t.Errorf("MigratedFrom = %d, want 1", got)
	}
	if got := load(t, b); !reflect.DeepEqual(got, want) {
		t.Errorf("after migrating: Load = %+v, want %+v", got, want)
	}
	if got := loadHosts(t, b); len(got) != 0 {
		t.Errorf("after migrating: LoadHosts = %+v, want no hosts", got)
	}
	putActual(t, b, host1, twoDeployments())
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if again := openBolt(t, path); again.MigratedFrom() != 0 {
		t.Errorf("second open: MigratedFrom = %d, want 0: the file is already version 2", again.MigratedFrom())
	} else if err := again.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	after := dump(t, path)
	if got := after["meta"]["schema"]; got != string(u64(2)) {
		t.Errorf("schema = %x, want version 2", got)
	}
	for _, name := range []string{"sync", "desired"} {
		if !reflect.DeepEqual(after[name], before[name]) {
			t.Errorf("bucket %s changed in the migration:\n got %v\nwant %v", name, after[name], before[name])
		}
	}
	if len(after["hosts"]) != 1 || len(after["actual"]) != 1 {
		t.Errorf("after PutActual: %d hosts and %d actual records, want 1 and 1", len(after["hosts"]), len(after["actual"]))
	}
}

// A new store file is created at layout version 2, with every bucket (ADR 0015).
func TestBoltCreatesLayoutVersion2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lo.db")
	b, err := store.OpenBolt(path)
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	if v := b.MigratedFrom(); v != 0 {
		t.Errorf("MigratedFrom = %d for a new file, want 0", v)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := dump(t, path)
	if got["meta"]["schema"] != string(u64(2)) {
		t.Errorf("schema = %x, want version 2", got["meta"]["schema"])
	}
	for _, name := range []string{"meta", "sync", "desired", "hosts", "actual"} {
		if _, ok := got[name]; !ok {
			t.Errorf("a new store file has no %s bucket", name)
		}
	}
}

// LoadHosts fails on anything it cannot trust, never returns some of the hosts in place of all,
// and leaves the file as it is for the operator (SPEC §14.2, ADR 0014, ADR 0015).
func TestBoltLoadHostsFailsOnDamagedStore(t *testing.T) {
	const goodHost = `{"capabilities":null,"labels":{},"lastHeartbeatAt":null}`
	actual := func(deployments string) string {
		return `{"reportedAt":"2026-10-10T12:00:00Z","deployments":[` + deployments + `]}`
	}
	deployment := func(digest contract.Digest, components string) string {
		return fmt.Sprintf(`{"deploymentId":%q,"digest":%q,"components":[%s]}`, idA, digest, components)
	}
	const web = `{"name":"web","state":"installed"}`
	deleteBucket := func(name []byte) func(tx *bolt.Tx) error {
		return func(tx *bolt.Tx) error { return tx.DeleteBucket(name) }
	}
	tests := []struct {
		name   string
		damage func(tx *bolt.Tx) error
	}{
		{"host record not JSON", put(bucketHosts, "host-01", "{")},
		{"host record without a capabilities key", put(bucketHosts, "host-01", `{"labels":{},"lastHeartbeatAt":null}`)},
		{"host record without a heartbeat key", put(bucketHosts, "host-01", `{"capabilities":null,"labels":{}}`)},
		{"capabilities without a device ID", put(bucketHosts, "host-01", `{"capabilities":{},"labels":{},"lastHeartbeatAt":null}`)},
		{"deployment without an ID", put(bucketActual, "host-01", `{"reportedAt":"2026-10-10T12:00:00Z","deployments":[{"digest":"`+string(digestA)+`","components":[]}]}`)},
		{"host record without labels", put(bucketHosts, "host-01", `{"capabilities":null,"labels":null,"lastHeartbeatAt":null}`)},
		{"host key not a host ID", put(bucketHosts, "site/host", goodHost)},
		{"actual record not JSON", put(bucketActual, "host-01", "{")},
		{"actual record without a report time", put(bucketActual, "host-01", `{"deployments":[]}`)},
		{"actual record without deployments", put(bucketActual, "host-01", `{"reportedAt":"2026-10-10T12:00:00Z"}`)},
		{"digest not sha256", put(bucketActual, "host-01", actual(deployment("md5:00", web)))},
		{"unknown state", put(bucketActual, "host-01", actual(deployment(digestA, `{"name":"web","state":"running"}`)))},
		{"component without a name", put(bucketActual, "host-01", actual(deployment(digestA, `{"name":"","state":"installed"}`)))},
		{"component listed twice", put(bucketActual, "host-01", actual(deployment(digestA, web+","+web)))},
		{"deployment listed twice", put(bucketActual, "host-01", actual(deployment(digestA, web)+","+deployment(digestB, web)))},
		{"actual state for a host the store does not hold", put(bucketActual, "host-09", actual(""))},
		{"hosts bucket missing", deleteBucket(bucketHosts)},
		{"actual bucket missing", deleteBucket(bucketActual)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := hostsAt(t)
			rawUpdate(t, path, tc.damage)
			before := dump(t, path)
			b, err := store.OpenBolt(path)
			if err != nil { // the damage is past the layout check: LoadHosts must catch it
				t.Fatalf("OpenBolt: %v; want it to open and LoadHosts to fail", err)
			}
			hosts, err := b.LoadHosts(t.Context())
			if err == nil {
				t.Errorf("LoadHosts = %+v, nil; want an error", hosts)
			}
			if len(hosts) != 0 {
				t.Errorf("LoadHosts returned %d hosts with its error, want none", len(hosts))
			}
			if err := b.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if after := dump(t, path); !reflect.DeepEqual(after, before) {
				t.Errorf("the store file changed:\n got %v\nwant %v", after, before)
			}
		})
	}
}
