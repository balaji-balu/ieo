package sync_test

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
	"github.com/balaji-balu/ieo/internal/platform"
	"github.com/balaji-balu/ieo/internal/platform/platformtest"
)

const (
	site         = contract.SiteID("site-1")
	pollInterval = 30 * time.Second
	manifestPath = "/api/v1/deployments"
	yamlPrefix   = "/api/v1/deployments/"
	bundlePrefix = "/api/v1/bundles/"
)

var (
	idA = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	idB = uuid.MustParse("0b0b0b0b-0000-4000-8000-00000000000b")
	idC = uuid.MustParse("0c0c0c0c-0000-4000-8000-00000000000c")
	idD = uuid.MustParse("0d0d0d0d-0000-4000-8000-00000000000d")
)

// fixture is a Syncer, its store, and the CO it syncs with.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	co    *fakeCO
	store *store.Memory
	sync  *losync.Syncer
	log   *bytes.Buffer
}

func newFixture(t *testing.T, change ...func(*losync.Config)) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), co: newFakeCO(t), store: store.NewMemory(), log: &bytes.Buffer{}}
	cfg := losync.Config{
		SiteID: site, COURL: coURL, Token: f.co.token, PollInterval: pollInterval, Transport: f.co,
		Clock: platformtest.NewFakeClock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)),
		Log:   slog.New(slog.NewJSONHandler(f.log, nil)),
	}
	for _, c := range change {
		c(&cfg)
	}
	f.sync = losync.New(cfg, f.store)
	return f
}

// tick runs one sync attempt and fails the test unless it ends in want.
func (f *fixture) tick(want losync.Outcome) {
	f.t.Helper()
	got, err := f.sync.Tick(f.ctx)
	if err != nil {
		f.t.Fatalf("Tick: %v", err)
	}
	if got != want {
		f.t.Fatalf("Tick = %q, want %q; log:\n%s", got, want, f.log)
	}
}

func (f *fixture) state() losync.State {
	f.t.Helper()
	st, err := f.store.Load(f.ctx)
	if err != nil {
		f.t.Fatalf("Load: %v", err)
	}
	return st
}

// accept publishes the deployments at version v and syncs, which must accept them.
func (f *fixture) accept(v contract.ManifestVersion, yamls map[uuid.UUID][]byte) {
	f.t.Helper()
	f.co.publish(v, yamls)
	f.tick(losync.Accepted)
	f.co.takeRequests()
	f.log.Reset()
}

// requireUnchanged fails the test if the stored state differs from before.
func (f *fixture) requireUnchanged(before losync.State) {
	f.t.Helper()
	if after := f.state(); !reflect.DeepEqual(after, before) {
		f.t.Fatalf("state changed:\n before %+v\n after  %+v", before, after)
	}
}

// lines returns the log lines written since the last reset, each decoded.
func (f *fixture) lines() []map[string]any {
	f.t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(f.log.Bytes()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			f.t.Fatalf("log line is not one JSON object: %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// outcomeLine returns the one log line of the last attempt, which must carry the outcome.
func (f *fixture) outcomeLine() map[string]any {
	f.t.Helper()
	lines := f.lines()
	if len(lines) != 1 {
		f.t.Fatalf("got %d log lines, want 1:\n%s", len(lines), f.log)
	}
	return lines[0]
}

// desired returns the desired state for the YAMLs as accepted at version v.
func desired(v contract.ManifestVersion, yamls map[uuid.UUID][]byte) map[uuid.UUID]losync.Desired {
	out := map[uuid.UUID]losync.Desired{}
	for id, y := range yamls {
		out[id] = losync.Desired{Digest: contract.DigestOf(y), YAML: y, AdoptedVersion: v}
	}
	return out
}

func yamls(rev string, ids ...uuid.UUID) map[uuid.UUID][]byte {
	out := map[uuid.UUID][]byte{}
	for _, id := range ids {
		out[id] = yamlOf(id, rev)
	}
	return out
}

func with(m map[uuid.UUID][]byte, id uuid.UUID, rev string) map[uuid.UUID][]byte {
	m = maps.Clone(m)
	m[id] = yamlOf(id, rev)
	return m
}

// SPEC §17.3: "First sync without ETag fetches the bundle and accepts the manifest."
func TestSpec_17_3_FirstSyncFetchesBundleAndAccepts(t *testing.T) {
	f := newFixture(t)
	v1 := yamls("1", idA, idB)
	f.co.publish(1, v1)

	f.tick(losync.Accepted)

	reqs := f.co.takeRequests()
	if got := paths(reqs); len(got) != 2 || got[0] != manifestPath || countPrefix(got, bundlePrefix) != 1 {
		t.Fatalf("requests = %v, want the manifest, then one bundle and no YAML", got)
	}
	if h := reqs[0].Header.Get("If-None-Match"); h != "" {
		t.Errorf("first sync sent If-None-Match %q, want none", h)
	}
	if h := reqs[0].Header.Get("Accept"); h != contract.ManifestMediaType {
		t.Errorf("Accept = %q, want %q (SPEC §11.1)", h, contract.ManifestMediaType)
	}
	want := losync.State{Version: 1, ETag: contract.ETag(f.co.manifest), Desired: desired(1, v1)}
	if got := f.state(); !reflect.DeepEqual(got, want) {
		t.Errorf("state = %+v\nwant %+v", got, want)
	}
}

// SPEC §17.3: "`304` changes nothing."
func TestSpec_17_3_NotModifiedChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.accept(1, yamls("1", idA))
	before := f.state()

	f.tick(losync.NotModified)

	reqs := f.co.takeRequests()
	if got := paths(reqs); !slices.Equal(got, []string{manifestPath}) {
		t.Fatalf("requests = %v, want only the manifest", got)
	}
	if h := reqs[0].Header.Get("If-None-Match"); h != before.ETag {
		t.Errorf("If-None-Match = %q, want the stored ETag %q", h, before.ETag)
	}
	f.requireUnchanged(before)
}

// SPEC §17.3: "A manifest with `manifestVersion` equal to or lower than stored is rejected,
// logged as a security event, and leaves desired state unchanged."
func TestSpec_17_3_RollbackRejected(t *testing.T) {
	for _, v := range []contract.ManifestVersion{2, 1} {
		t.Run(fmt.Sprintf("version %d after 2", v), func(t *testing.T) {
			f := newFixture(t)
			f.accept(2, yamls("1", idA))
			before := f.state()
			f.co.publish(v, yamls("2", idA, idB)) // other content, so another ETag

			f.tick(losync.RejectedRollback)

			if got := paths(f.co.takeRequests()); !slices.Equal(got, []string{manifestPath}) {
				t.Errorf("requests = %v, want only the manifest", got)
			}
			f.requireUnchanged(before)
			if l := f.outcomeLine(); l["level"] != "WARN" {
				t.Errorf("rollback logged at %v, want WARN (security event)", l["level"])
			}
		})
	}
}

// SPEC §17.3: "A digest mismatch on any artifact, or a bundle with a missing, extra or misnamed
// entry, aborts the whole update and leaves desired state unchanged."
func TestSpec_17_3_DigestMismatchAbortsWholeUpdate(t *testing.T) {
	v1 := yamls("1", idA, idB)
	bundle := func(t *testing.T, files map[uuid.UUID][]byte) []byte {
		t.Helper()
		var bf []contract.BundleFile
		for id, y := range files {
			bf = append(bf, contract.BundleFile{DeploymentID: id, YAML: y})
		}
		b, err := contract.EncodeBundle(bf)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	bundleCases := []struct {
		name  string
		setup func(t *testing.T, co *fakeCO, m contract.StateManifest)
	}{
		{"bundle bytes do not match its digest", func(_ *testing.T, co *fakeCO, m contract.StateManifest) {
			co.override(m.Bundle.URL, respond(http.StatusOK, []byte("not the bundle")))
		}},
		{"an entry does not match its deployment's digest", func(t *testing.T, co *fakeCO, _ contract.StateManifest) {
			co.replaceBundle(bundle(t, map[uuid.UUID][]byte{idA: v1[idA], idB: []byte("tampered")}))
		}},
		{"an entry is missing", func(t *testing.T, co *fakeCO, _ contract.StateManifest) {
			co.replaceBundle(bundle(t, map[uuid.UUID][]byte{idA: v1[idA]}))
		}},
		{"an extra entry", func(t *testing.T, co *fakeCO, _ contract.StateManifest) {
			co.replaceBundle(bundle(t, map[uuid.UUID][]byte{idA: v1[idA], idB: v1[idB], idC: yamlOf(idC, "1")}))
		}},
		{"a misnamed entry", func(t *testing.T, co *fakeCO, _ contract.StateManifest) {
			co.replaceBundle(tarGz(t,
				tarEntry{idA.String() + ".yaml", tar.TypeReg, v1[idA]},
				tarEntry{strings.ToUpper(idB.String()) + ".yaml", tar.TypeReg, v1[idB]}))
		}},
		{"a directory entry", func(t *testing.T, co *fakeCO, _ contract.StateManifest) {
			co.replaceBundle(tarGz(t,
				tarEntry{idA.String() + ".yaml", tar.TypeReg, v1[idA]},
				tarEntry{idB.String() + ".yaml", tar.TypeReg, v1[idB]},
				tarEntry{"extra/", tar.TypeDir, nil}))
		}},
	}
	for _, tc := range bundleCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			before := f.state()
			tc.setup(t, f.co, f.co.publish(1, v1))

			f.tick(losync.AbortedDigestMismatch)

			f.requireUnchanged(before)
			if got := paths(f.co.takeRequests()); countPrefix(got, bundlePrefix) != 1 {
				t.Errorf("requests = %v, want one bundle request", got)
			}
		})
	}

	t.Run("one wrong YAML among good ones", func(t *testing.T) {
		f := newFixture(t)
		v1 := yamls("1", idA, idB, idC, idD)
		f.accept(1, v1)
		before := f.state()
		v2 := with(with(v1, idA, "2"), idB, "2") // 2 of 4 not held: fetched one by one
		m := f.co.publish(2, v2)
		for _, d := range m.Deployments {
			if d.DeploymentID == idA {
				f.co.override(d.URL, respond(http.StatusOK, []byte("tampered")))
			}
		}

		f.tick(losync.AbortedDigestMismatch)

		f.requireUnchanged(before)
		if got := paths(f.co.takeRequests()); countPrefix(got, bundlePrefix) != 0 {
			t.Errorf("requests = %v, want no bundle request", got)
		}
	})
}

// SPEC §17.3: "When the deployments whose digest the LO does not hold are more than half of the
// manifest's, the LO fetches the bundle; otherwise it fetches each of them by its content URL."
func TestSpec_17_3_BundleOnlyWhenMoreThanHalfNotHeld(t *testing.T) {
	tests := []struct {
		name        string
		v1, v2      map[uuid.UUID][]byte
		wantYAML    int
		wantBundles int
	}{
		{"1 of 2 not held: by content URL", yamls("1", idA, idB), with(yamls("1", idA, idB), idB, "2"), 1, 0},
		{"2 of 3 not held: bundle", yamls("1", idA, idB, idC), with(with(yamls("1", idA, idB, idC), idB, "2"), idC, "2"), 0, 1},
		{"1 of 2 new: by content URL", yamls("1", idA), yamls("1", idA, idB), 1, 0},
		{"none not held: no fetch", yamls("1", idA, idB), yamls("1", idA, idB), 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.accept(1, tc.v1)
			f.co.publish(2, tc.v2)

			f.tick(losync.Accepted)

			got := paths(f.co.takeRequests())
			if n := countPrefix(got, bundlePrefix); n != tc.wantBundles {
				t.Errorf("requests = %v: %d bundle requests, want %d", got, n, tc.wantBundles)
			}
			if n := countPrefix(got, yamlPrefix); n != tc.wantYAML {
				t.Errorf("requests = %v: %d YAML requests, want %d", got, n, tc.wantYAML)
			}
			if st := f.state(); st.Version != 2 || len(st.Desired) != len(tc.v2) {
				t.Errorf("state = %+v, want version 2 with %d deployments", st, len(tc.v2))
			}
		})
	}
}

// SPEC §17.3: "A `404` on a content URL does not remove the deployment, leaves desired state
// unchanged, and does not stop polling."
func TestSpec_17_3_ContentURL404DoesNotRemoveDeployment(t *testing.T) {
	tests := []struct {
		name string
		v2   map[uuid.UUID][]byte
		url  func(m contract.StateManifest) string
	}{
		{"YAML", with(yamls("1", idA, idB), idB, "2"), func(m contract.StateManifest) string {
			for _, d := range m.Deployments {
				if d.DeploymentID == idB {
					return d.URL
				}
			}
			return ""
		}},
		{"bundle", with(yamls("2", idA, idB, idC), idA, "1"), func(m contract.StateManifest) string { return m.Bundle.URL }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.accept(1, yamls("1", idA, idB))
			before := f.state()
			m := f.co.publish(2, tc.v2)
			url := tc.url(m)
			f.co.override(url, func(w http.ResponseWriter, _ *http.Request) {
				writeProblem(w, http.StatusNotFound, contract.ProblemDeploymentNotFound)
			})

			f.tick(losync.Unreachable)
			f.requireUnchanged(before)

			f.co.override(url, nil)
			f.tick(losync.Accepted)
			if want := desired(2, tc.v2); !reflect.DeepEqual(f.state().Desired, mergeAdopted(want, before.Desired)) {
				t.Errorf("after the CO is fixed, desired = %+v", f.state().Desired)
			}
		})
	}
}

// mergeAdopted keeps the adopted version of each deployment of want whose digest is unchanged
// from prev.
func mergeAdopted(want, prev map[uuid.UUID]losync.Desired) map[uuid.UUID]losync.Desired {
	for id, d := range want {
		if p, ok := prev[id]; ok && p.Digest == d.Digest {
			d.AdoptedVersion = p.AdoptedVersion
			want[id] = d
		}
	}
	return want
}

// SPEC §17.3: "A manifest that fails validation (schema, duplicate `deploymentId`, `bundle: null`
// with deployments) is not accepted and leaves desired state unchanged."
func TestSpec_17_3_InvalidManifestNotAccepted(t *testing.T) {
	encode := func(t *testing.T, m contract.StateManifest) []byte {
		t.Helper()
		b, err := contract.EncodeStateManifest(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	tests := []struct {
		name string
		body func(t *testing.T, valid contract.StateManifest) []byte
	}{
		{"not JSON", func(*testing.T, contract.StateManifest) []byte { return []byte("{") }},
		{"schema", func(*testing.T, contract.StateManifest) []byte { return []byte(`{"manifestVersion":"two"}`) }},
		{"duplicate deploymentId", func(t *testing.T, m contract.StateManifest) []byte {
			m.Deployments = append(m.Deployments, m.Deployments[0])
			return encode(t, m)
		}},
		{"bundle null with deployments", func(t *testing.T, m contract.StateManifest) []byte {
			m.Bundle = nil
			return encode(t, m)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.accept(1, yamls("1", idA))
			before := f.state()
			f.co.setManifestBody(tc.body(t, f.co.publish(2, yamls("2", idA, idB))))

			f.tick(losync.Unreachable)

			f.requireUnchanged(before)
			if l := f.outcomeLine(); l["level"] != "ERROR" || l["reason"] == nil {
				t.Errorf("log line = %v, want ERROR with the reason (SPEC §8.2 step 3)", l)
			}
		})
	}
}

// SPEC §17.3: "`adoptedManifestVersion` stays at the version where the current digest first
// appeared, across later manifests that do not change that deployment."
func TestSpec_17_3_AdoptedVersionStaysWhileDigestUnchanged(t *testing.T) {
	f := newFixture(t)
	v1 := yamls("1", idA, idB)
	f.accept(1, v1)
	v2 := with(v1, idB, "2")
	f.accept(2, v2)
	v3 := with(v2, idC, "3")
	f.accept(3, v3)

	got := f.state().Desired
	for id, want := range map[uuid.UUID]contract.ManifestVersion{idA: 1, idB: 2, idC: 3} {
		if got[id].AdoptedVersion != want {
			t.Errorf("deployment %s: AdoptedVersion = %d, want %d", id, got[id].AdoptedVersion, want)
		}
	}
}

// SPEC §17.3: "After a simulated outage spanning several CO-side changes, one successful poll
// converges desired state to the latest manifest."
func TestSpec_17_3_OnePollConvergesAfterOutage(t *testing.T) {
	f := newFixture(t)
	f.accept(1, yamls("1", idA, idB))

	f.co.setDown(true)
	f.co.publish(2, yamls("2", idA, idB))
	f.tick(losync.Unreachable)
	f.co.publish(3, yamls("3", idB, idC))
	f.tick(losync.Unreachable)
	v4 := yamls("4", idC, idD)
	f.co.publish(4, v4)
	f.co.setDown(false)

	f.tick(losync.Accepted)

	st := f.state()
	if st.Version != 4 || !reflect.DeepEqual(st.Desired, desired(4, v4)) {
		t.Errorf("state = %+v, want version 4 with desired %+v", st, desired(4, v4))
	}
}

// SPEC §17.7: "`RejectedRollback` and `AbortedDigestMismatch` are logged at the security/warning
// level with the offending values: the stored and received `manifestVersion`, the expected and
// computed digest, or the bundle entry that does not match the manifest."
func TestSpec_17_7_RollbackAndDigestMismatchLoggedWithOffendingValues(t *testing.T) {
	v1 := yamls("1", idA, idB)

	t.Run("rollback", func(t *testing.T) {
		f := newFixture(t)
		f.accept(3, v1)
		f.co.publish(2, yamls("2", idA))
		f.tick(losync.RejectedRollback)
		requireFields(t, f.outcomeLine(), map[string]any{
			"level": "WARN", "outcome": "RejectedRollback", "manifest_version": 2.0, "stored_manifest_version": 3.0,
		})
	})

	t.Run("bundle digest", func(t *testing.T) {
		f := newFixture(t)
		m := f.co.publish(1, v1)
		wrong := []byte("not the bundle")
		f.co.override(m.Bundle.URL, respond(http.StatusOK, wrong))
		f.tick(losync.AbortedDigestMismatch)
		requireFields(t, f.outcomeLine(), map[string]any{
			"level": "WARN", "outcome": "AbortedDigestMismatch",
			"bundle_digest": string(m.Bundle.Digest), "computed_digest": string(contract.DigestOf(wrong)),
		})
	})

	t.Run("YAML digest", func(t *testing.T) {
		f := newFixture(t)
		f.accept(1, v1)
		m := f.co.publish(2, with(v1, idB, "2"))
		wrong := []byte("tampered")
		var want contract.DeploymentRef
		for _, d := range m.Deployments {
			if d.DeploymentID == idB {
				want = d
				f.co.override(d.URL, respond(http.StatusOK, wrong))
			}
		}
		f.tick(losync.AbortedDigestMismatch)
		requireFields(t, f.outcomeLine(), map[string]any{
			"level": "WARN", "outcome": "AbortedDigestMismatch", "deployment_id": idB.String(),
			"digest": string(want.Digest), "computed_digest": string(contract.DigestOf(wrong)),
		})
	})

	t.Run("bundle entry digest", func(t *testing.T) {
		f := newFixture(t)
		m := f.co.publish(1, v1)
		wrong := []byte("tampered")
		b, err := contract.EncodeBundle([]contract.BundleFile{{DeploymentID: idA, YAML: v1[idA]}, {DeploymentID: idB, YAML: wrong}})
		if err != nil {
			t.Fatal(err)
		}
		f.co.replaceBundle(b)
		f.tick(losync.AbortedDigestMismatch)
		var digestB contract.Digest
		for _, d := range m.Deployments {
			if d.DeploymentID == idB {
				digestB = d.Digest
			}
		}
		requireFields(t, f.outcomeLine(), map[string]any{
			"level": "WARN", "deployment_id": idB.String(),
			"digest": string(digestB), "computed_digest": string(contract.DigestOf(wrong)),
		})
	})

	t.Run("bundle entry not in the manifest", func(t *testing.T) {
		f := newFixture(t)
		f.co.publish(1, v1)
		b, err := contract.EncodeBundle([]contract.BundleFile{
			{DeploymentID: idA, YAML: v1[idA]}, {DeploymentID: idB, YAML: v1[idB]}, {DeploymentID: idC, YAML: yamlOf(idC, "1")},
		})
		if err != nil {
			t.Fatal(err)
		}
		f.co.replaceBundle(b)
		f.tick(losync.AbortedDigestMismatch)
		requireFields(t, f.outcomeLine(), map[string]any{"level": "WARN", "bundle_entry": idC.String() + ".yaml"})
	})

	t.Run("manifest deployment missing from the bundle", func(t *testing.T) {
		f := newFixture(t)
		m := f.co.publish(1, v1)
		b, err := contract.EncodeBundle([]contract.BundleFile{{DeploymentID: idA, YAML: v1[idA]}})
		if err != nil {
			t.Fatal(err)
		}
		f.co.replaceBundle(b)
		f.tick(losync.AbortedDigestMismatch)
		var digestB contract.Digest
		for _, d := range m.Deployments {
			if d.DeploymentID == idB {
				digestB = d.Digest
			}
		}
		requireFields(t, f.outcomeLine(), map[string]any{
			"level": "WARN", "bundle_entry": idB.String() + ".yaml", "deployment_id": idB.String(), "digest": string(digestB),
		})
	})

	t.Run("misnamed bundle entry", func(t *testing.T) {
		f := newFixture(t)
		f.co.publish(1, v1)
		bad := strings.ToUpper(idB.String()) + ".yaml"
		f.co.replaceBundle(tarGz(t,
			tarEntry{idA.String() + ".yaml", tar.TypeReg, v1[idA]}, tarEntry{bad, tar.TypeReg, v1[idB]}))
		f.tick(losync.AbortedDigestMismatch)
		l := f.outcomeLine()
		requireFields(t, l, map[string]any{"level": "WARN"})
		if r, _ := l["reason"].(string); !strings.Contains(r, bad) {
			t.Errorf("reason = %q, want it to name the entry %q", r, bad)
		}
	})
}

// requireFields fails the test unless line has each of want's fields with its value.
func requireFields(t *testing.T, line map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if line[k] != v {
			t.Errorf("log field %s = %#v, want %#v; line %v", k, line[k], v, line)
		}
	}
}

// Every failed request is Unreachable until roadmap slice K adds Throttled and Retired (SPEC
// §7.4, §15.6).
func TestFailedRequestsAreUnreachable(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc // nil: transport error
	}{
		{"transport error", nil},
		{"500", respond(http.StatusInternalServerError, nil)},
		{"503 problem", func(w http.ResponseWriter, _ *http.Request) {
			writeProblem(w, http.StatusServiceUnavailable, contract.ProblemAboutBlank)
		}},
		{"401", func(w http.ResponseWriter, _ *http.Request) {
			writeProblem(w, http.StatusUnauthorized, contract.ProblemAboutBlank)
		}},
		{"403 not-authorized", func(w http.ResponseWriter, _ *http.Request) {
			writeProblem(w, http.StatusForbidden, contract.ProblemNotAuthorized)
		}},
		{"406", func(w http.ResponseWriter, _ *http.Request) {
			writeProblem(w, http.StatusNotAcceptable, contract.ProblemCannotGenerate)
		}},
		{"429", respond(http.StatusTooManyRequests, nil, "Retry-After", "10")},
		{"404 with Retry-After", respond(http.StatusNotFound, nil, "Retry-After", "10")},
		{"204", respond(http.StatusNoContent, nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.accept(1, yamls("1", idA))
			before := f.state()
			f.co.publish(2, yamls("2", idA))
			if tc.handler == nil {
				f.co.setDown(true)
			} else {
				f.co.override(manifestPath, tc.handler)
			}

			f.tick(losync.Unreachable)

			f.requireUnchanged(before)
			requireFields(t, f.outcomeLine(), map[string]any{"outcome": "Unreachable", "manifest_version": 1.0})
		})
	}
}

// SPEC §15.6: "The LO follows no redirects on Margo API requests. A redirect (`301`, `302`, `303`,
// `307`, `308`) is `Unreachable` (§7.4); `304` stays `NotModified`."
func TestRedirectsAreNotFollowed(t *testing.T) {
	const elsewhere = "https://elsewhere.test"
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, target := range []string{"manifest", "bundle"} {
			t.Run(fmt.Sprintf("%d on the %s", status, target), func(t *testing.T) {
				f := newFixture(t)
				m := f.co.publish(1, yamls("1", idA))
				path := manifestPath
				if target == "bundle" {
					path = m.Bundle.URL
				}
				f.co.override(path, respond(status, nil, "Location", elsewhere+path))

				f.tick(losync.Unreachable)

				for _, r := range f.co.takeRequests() {
					if r.URL.Host != "co.test" {
						t.Errorf("request to %s: the redirect was followed", r.URL)
					}
				}
				if st := f.state(); st.Version != 0 {
					t.Errorf("state = %+v, want nothing accepted", st)
				}
			})
		}
	}
	t.Run("304", func(t *testing.T) {
		f := newFixture(t)
		f.accept(1, yamls("1", idA))
		f.co.override(manifestPath, respond(http.StatusNotModified, nil))
		f.tick(losync.NotModified)
	})
}

// SPEC §15.4, §15.6: the token goes only in the Authorization header and is never logged.
func TestTokenSentOnlyInHeaderAndNeverLogged(t *testing.T) {
	f := newFixture(t)
	var all []*http.Request
	step := func(want losync.Outcome) {
		f.tick(want)
		all = append(all, f.co.takeRequests()...)
	}
	v1 := yamls("1", idA, idB, idC)
	f.co.publish(1, v1)
	step(losync.Accepted) // bundle
	step(losync.NotModified)
	f.co.publish(2, with(v1, idA, "2"))
	step(losync.Accepted) // one YAML
	f.co.publish(1, yamls("3", idA))
	step(losync.RejectedRollback)
	m := f.co.publish(3, with(v1, idB, "3"))
	f.co.override(m.Bundle.URL, respond(http.StatusOK, []byte("tampered")))
	step(losync.AbortedDigestMismatch)
	f.co.override(manifestPath, func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusUnauthorized, contract.ProblemAboutBlank)
	})
	step(losync.Unreachable)
	f.co.setDown(true)
	step(losync.Unreachable)

	if len(all) == 0 {
		t.Fatal("no requests")
	}
	for _, r := range all {
		if got := r.Header.Get("Authorization"); got != "Bearer "+f.co.token {
			t.Errorf("%s: Authorization header does not carry the site token", r.URL.Path)
		}
		if strings.Contains(r.URL.String(), f.co.token) {
			t.Errorf("%s: the token is in the URL", r.URL.Path)
		}
	}
	if strings.Contains(f.log.String(), f.co.token) {
		t.Errorf("the token appears in the log:\n%s", f.log)
	}
}

// SPEC §8.2 steps 6–8: desired state is written, then reconcile is triggered, then the version is
// committed.
func TestVersionCommittedAfterReconcileStarts(t *testing.T) {
	var seen []losync.State
	var f *fixture
	f = newFixture(t, func(c *losync.Config) {
		c.Reconcile = func(ctx context.Context) {
			st, err := f.store.Load(ctx)
			if err != nil {
				t.Errorf("Load: %v", err)
			}
			seen = append(seen, st)
		}
	})
	v1 := yamls("1", idA)
	f.accept(1, v1)
	v2 := with(v1, idB, "2")
	f.co.publish(2, v2)
	f.tick(losync.Accepted)
	f.tick(losync.NotModified)

	if len(seen) != 2 {
		t.Fatalf("Reconcile called %d times, want once per accepted manifest (2)", len(seen))
	}
	if got := seen[1]; got.Version != 1 || !reflect.DeepEqual(got.Desired, mergeAdopted(desired(2, v2), desired(1, v1))) {
		t.Errorf("Reconcile saw %+v, want the new desired state at the old version 1", got)
	}
	if st := f.state(); st.Version != 2 {
		t.Errorf("after the tick, Version = %d, want 2", st.Version)
	}
}

// SPEC §13.1: "Sync attempts MUST log their outcome (§7.4) and `manifest_version`", on one line
// with `site_id`, for the outcomes C1 produces. The §17.7 bullet for every outcome waits for
// roadmap slice K.
func TestEveryAttemptLogsOneOutcomeLine(t *testing.T) {
	f := newFixture(t)
	check := func(want losync.Outcome, version float64) {
		t.Helper()
		f.log.Reset()
		f.tick(want)
		requireFields(t, f.outcomeLine(), map[string]any{
			"site_id": string(site), "outcome": string(want), "manifest_version": version,
		})
	}
	v1 := yamls("1", idA)
	f.co.publish(1, v1)
	check(losync.Accepted, 1)
	check(losync.NotModified, 1)
	f.co.publish(1, yamls("2", idA, idB))
	check(losync.RejectedRollback, 1)
	m := f.co.publish(2, with(v1, idA, "2"))
	f.co.override(m.Bundle.URL, respond(http.StatusOK, []byte("tampered")))
	check(losync.AbortedDigestMismatch, 2)
	f.co.setDown(true)
	check(losync.Unreachable, 1)
}

// notifyClock is a FakeClock that reports each timer it makes, so a test advances time only once
// Run is waiting.
type notifyClock struct {
	*platformtest.FakeClock
	timers chan time.Duration
}

func (c notifyClock) NewTimer(d time.Duration) platform.Timer {
	t := c.FakeClock.NewTimer(d)
	c.timers <- d
	return t
}

func TestRunTicksAtInterval(t *testing.T) {
	clock := notifyClock{platformtest.NewFakeClock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)), make(chan time.Duration)}
	ticks := make(chan struct{}, 10)
	f := newFixture(t, func(c *losync.Config) { c.Clock = clock })
	f.co.publish(1, yamls("1", idA))
	f.co.override(manifestPath, counting(f.co, ticks))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.sync.Run(ctx) }()

	recv(t, ticks, "the first tick")
	if d := recv(t, clock.timers, "the first wait"); d != pollInterval {
		t.Fatalf("Run waits %v, want the poll interval %v", d, pollInterval)
	}
	clock.Advance(pollInterval - time.Nanosecond)
	select {
	case <-ticks:
		t.Fatal("ticked before the interval elapsed")
	default: // the timer has not fired, so Run cannot tick
	}
	clock.Advance(time.Nanosecond)
	recv(t, ticks, "the second tick")
	recv(t, clock.timers, "the second wait")

	cancel()
	if err := recv(t, done, "Run to return"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

// counting returns a manifest handler that reports each request on ticks, then serves it.
func counting(co *fakeCO, ticks chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ticks <- struct{}{}
		co.override(manifestPath, nil)
		co.ServeHTTP(w, r)
		co.override(manifestPath, counting(co, ticks))
	}
}

// recv receives from ch. The deadline only guards against a hang: the test never waits on time
// otherwise (G-F4).
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}
