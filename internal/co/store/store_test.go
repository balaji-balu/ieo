package store_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/storetest"
	"github.com/balaji-balu/ieo/internal/contract"
)

const (
	site1 = contract.SiteID("site-1")
	site2 = contract.SiteID("site-2")
)

var host1 = contract.DeviceID{Site: site1, Host: "host-1"}

// newSites returns a store with site-1 and site-2, each at manifest version 1.
func newSites(t *testing.T, newStore storetest.New) storetest.Store {
	t.Helper()
	s := newStore(t)
	for _, site := range []contract.SiteID{site1, site2} {
		if err := deploy.New(s).AddSite(context.Background(), site); err != nil {
			t.Fatalf("add site %s: %v", site, err)
		}
	}
	return s
}

// bump returns the change that publishes the site's next manifest version, with no deployments.
func bump(state deploy.SiteState) deploy.SiteChange {
	v := state.Manifest.Version + 1
	return deploy.SiteChange{Manifest: &deploy.Manifest{Version: v, Body: []byte{byte(v)}, ETag: `"e"`}}
}

func version(t *testing.T, s storetest.Store, site contract.SiteID) contract.ManifestVersion {
	t.Helper()
	m, ok, err := s.Manifest(context.Background(), site)
	if err != nil || !ok {
		t.Fatalf("manifest of %s: %v %v", site, ok, err)
	}
	return m.Version
}

// SPEC §12: changes to one site are atomic and serialized, so concurrent changes that each publish
// the next manifest version lose none.
func TestChangesToOneSiteAreSerialized(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		ctx := context.Background()
		d := deployment(site1)
		if err := s.ChangeSite(ctx, site1, func(deploy.SiteState, bool) (deploy.SiteChange, error) {
			return deploy.SiteChange{Deployment: &d}, nil
		}); err != nil {
			t.Fatalf("put deployment: %v", err)
		}
		const n = 10
		var wg sync.WaitGroup
		errs := make(chan error, 3*n)
		for i := range n {
			wg.Add(3)
			go func() {
				defer wg.Done()
				errs <- s.ChangeSite(ctx, site1, func(state deploy.SiteState, _ bool) (deploy.SiteChange, error) {
					return bump(state), nil
				})
			}()
			go func() {
				defer wg.Done()
				errs <- s.ChangeDeployment(ctx, site1, d.ID, func(state deploy.DeploymentState, _ bool) (deploy.SiteChange, error) {
					st := status(d.ID, state.ManifestVersion)
					return deploy.SiteChange{Status: &deploy.StatusReport{Status: st, Current: true}}, nil
				})
			}()
			go func() {
				defer wg.Done()
				host := contract.DeviceID{Site: site1, Host: contract.HostID("host-" + string(rune('a'+i)))}
				errs <- s.ChangeDevice(ctx, site1, host, func(deploy.DeviceState, bool) (deploy.SiteChange, error) {
					caps := capabilities(host)
					return deploy.SiteChange{Device: &deploy.DeviceChange{ID: host, Capabilities: &caps}}, nil
				})
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("change: %v", err)
			}
		}
		if got := version(t, s, site1); got != 1+n {
			t.Errorf("site-1 manifest version %d, want %d", got, 1+n)
		}
		if got := version(t, s, site2); got != 1 {
			t.Errorf("site-2 manifest version %d, want 1", got)
		}
		h, err := s.StatusHistory(ctx, d.ID)
		if err != nil || len(h) != n {
			t.Errorf("status history has %d reports (%v), want %d", len(h), err, n)
		}
		err = s.ChangeSite(ctx, site1, func(state deploy.SiteState, _ bool) (deploy.SiteChange, error) {
			if len(state.Hosts) != n {
				t.Errorf("site state has %d hosts, want %d", len(state.Hosts), n)
			}
			return deploy.SiteChange{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// SPEC §12: a change that fails writes nothing.
func TestFailedChangeWritesNothing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		ctx := context.Background()
		boom := errors.New("boom")
		d := deployment(site1)
		caps := capabilities(host1)
		full := func(state deploy.SiteState) deploy.SiteChange {
			c := bump(state)
			c.Deployment = &d
			c.Blobs = map[contract.Digest][]byte{d.Digest: []byte("yaml")}
			c.Device = &deploy.DeviceChange{ID: host1, Capabilities: &caps}
			c.Site = &deploy.Site{ID: site1, Retired: true}
			return c
		}
		changes := map[string]func() error{
			"ChangeSite": func() error {
				return s.ChangeSite(ctx, site1, func(state deploy.SiteState, _ bool) (deploy.SiteChange, error) {
					return full(state), boom
				})
			},
			"ChangeDeployment": func() error {
				return s.ChangeDeployment(ctx, site1, d.ID, func(deploy.DeploymentState, bool) (deploy.SiteChange, error) {
					return deploy.SiteChange{Deployment: &d}, boom
				})
			},
			"ChangeDevice": func() error {
				return s.ChangeDevice(ctx, site1, host1, func(deploy.DeviceState, bool) (deploy.SiteChange, error) {
					return deploy.SiteChange{Device: &deploy.DeviceChange{ID: host1, Capabilities: &caps}}, boom
				})
			},
		}
		for name, change := range changes {
			if err := change(); !errors.Is(err, boom) {
				t.Errorf("%s returned %v, want the change's error", name, err)
			}
		}
		if got := version(t, s, site1); got != 1 {
			t.Errorf("manifest version %d, want 1", got)
		}
		if _, ok, _ := s.Deployment(ctx, d.ID); ok {
			t.Error("deployment written")
		}
		if _, ok, _ := s.Blob(ctx, d.Digest); ok {
			t.Error("blob written")
		}
		if site, _, _ := s.Site(ctx, site1); site.Retired {
			t.Error("site written")
		}
		err := s.ChangeDevice(ctx, site1, host1, func(state deploy.DeviceState, _ bool) (deploy.SiteChange, error) {
			if state.Device {
				t.Error("device written")
			}
			return deploy.SiteChange{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// SPEC §12: a site's manifestVersion never decreases.
func TestChangeRefusesLowerManifestVersion(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		ctx := context.Background()
		for range 2 {
			if err := s.ChangeSite(ctx, site1, func(state deploy.SiteState, _ bool) (deploy.SiteChange, error) {
				return bump(state), nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		err := s.ChangeSite(ctx, site1, func(deploy.SiteState, bool) (deploy.SiteChange, error) {
			return deploy.SiteChange{Manifest: &deploy.Manifest{Version: 2, Body: []byte("old"), ETag: `"old"`}}, nil
		})
		if err == nil {
			t.Error("lower manifest version accepted")
		}
		if got := version(t, s, site1); got != 3 {
			t.Errorf("manifest version %d, want 3", got)
		}
	})
}

// SPEC §11.1: a digest is served to a site only once a manifest of that site published it, for
// the deployment it was published with.
func TestDigestServedOnlyOncePublished(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		ctx := context.Background()
		d := deployment(site1)
		yaml := []byte("yaml")
		bundle := []byte("bundle")
		bundleDigest := contract.DigestOf(bundle)
		other := uuid.New()

		// Stored, but in no manifest.
		if err := s.ChangeSite(ctx, site1, func(deploy.SiteState, bool) (deploy.SiteChange, error) {
			return deploy.SiteChange{Deployment: &d, Blobs: map[contract.Digest][]byte{d.Digest: yaml, bundleDigest: bundle}}, nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.DeploymentYAML(ctx, site1, d.ID, d.Digest); ok {
			t.Error("YAML served before a manifest published it")
		}
		if _, ok, _ := s.Bundle(ctx, site1, bundleDigest); ok {
			t.Error("bundle served before a manifest named it")
		}

		if err := s.ChangeSite(ctx, site1, func(state deploy.SiteState, _ bool) (deploy.SiteChange, error) {
			c := bump(state)
			c.Deployment = &d
			c.Manifest.Bundle = bundleDigest
			return c, nil
		}); err != nil {
			t.Fatal(err)
		}
		if b, ok, err := s.DeploymentYAML(ctx, site1, d.ID, d.Digest); !ok || err != nil || string(b) != "yaml" {
			t.Errorf("published YAML: %q %v %v", b, ok, err)
		}
		if b, ok, err := s.Bundle(ctx, site1, bundleDigest); !ok || err != nil || string(b) != "bundle" {
			t.Errorf("published bundle: %q %v %v", b, ok, err)
		}
		for name, ok := range map[string]bool{
			"YAML to another site":       must(s.DeploymentYAML(ctx, site2, d.ID, d.Digest)),
			"YAML under another ID":      must(s.DeploymentYAML(ctx, site1, other, d.Digest)),
			"bundle to another site":     must(s.Bundle(ctx, site2, bundleDigest)),
			"bundle digest as YAML":      must(s.DeploymentYAML(ctx, site1, d.ID, bundleDigest)),
			"YAML digest as bundle":      must(s.Bundle(ctx, site1, d.Digest)),
			"empty digest":               must(s.Bundle(ctx, site1, "")),
			"deployment digest unstored": must(s.DeploymentYAML(ctx, site1, d.ID, contract.DigestOf([]byte("x")))),
		} {
			if ok {
				t.Errorf("served %s", name)
			}
		}
	})
}

func must(_ []byte, ok bool, _ error) bool { return ok }

// SPEC §15.6: a site has one token hash; a new one replaces the old.
func TestSiteTokenHashIsReplaced(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		ctx := context.Background()
		h1, h2 := auth.TokenHash{1}, auth.TokenHash{2}
		if ok, err := s.PutSiteToken(ctx, "site-x", h1); ok || err != nil {
			t.Errorf("token for unknown site: %v %v", ok, err)
		}
		if ok, err := s.PutSiteToken(ctx, site1, h1); !ok || err != nil {
			t.Fatalf("put token: %v %v", ok, err)
		}
		if site, ok, err := s.SiteByToken(ctx, h1); site != site1 || !ok || err != nil {
			t.Errorf("site by token: %q %v %v", site, ok, err)
		}
		if ok, err := s.PutSiteToken(ctx, site1, h2); !ok || err != nil {
			t.Fatalf("replace token: %v %v", ok, err)
		}
		if _, ok, _ := s.SiteByToken(ctx, h1); ok {
			t.Error("old token still maps to the site")
		}
		if site, ok, _ := s.SiteByToken(ctx, h2); site != site1 || !ok {
			t.Errorf("new token maps to %q %v", site, ok)
		}
		if _, ok, _ := s.SiteByToken(ctx, auth.TokenHash{}); ok {
			t.Error("zero hash maps to a site")
		}
	})
}

// The store keeps what it was given: values round-trip exactly, a write never keeps the caller's
// maps or slices, and a read never hands out the store's.
func TestValuesRoundTripAndAreCopied(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		d := deployment(site1)
		d.Parameters = parameters()
		yaml := []byte("yaml")
		caps := capabilities(host1)
		caps.Labels["nul"] = "a\x00b"
		st := status(d.ID, 2)
		st.DeviceID = contract.DeviceID{} // optional in Margo
		st.Components[0].Error = &contract.StatusError{Message: "a\x00b"}
		manifest := deploy.Manifest{Version: 2, Body: []byte(`{"manifestVersion":2}`), ETag: `"e2"`}
		put(t, s, site1, deploy.SiteChange{
			Deployment: &d, Blobs: map[contract.Digest][]byte{d.Digest: yaml}, Manifest: &manifest,
			Device: &deploy.DeviceChange{ID: host1, Capabilities: &caps},
			Status: &deploy.StatusReport{Status: st, Current: true},
		})
		want := stored{deployment(site1), capabilities(host1), status(d.ID, 2)}
		want.deployment.ID = d.ID
		want.deployment.Parameters = parameters()
		want.capabilities.Labels["nul"] = "a\x00b"
		want.status.DeviceID = contract.DeviceID{}
		want.status.Components[0].Error = &contract.StatusError{Message: "a\x00b"}

		// Change everything the caller still holds.
		d.Parameters["s"] = "changed"
		d.Parameters["map"].(map[string]any)["k"] = "changed"
		yaml[0] = 'X'
		manifest.Body[0] = 'X'
		caps.Labels["line"] = "changed"
		caps.Properties.CPUs[0].Cores = 99
		st.Components[0].Name = "changed"
		want.check(t, s, "after the caller changed its values")
		want.check(t, s, "after a reader changed what it read")
	})
}

func parameters() map[string]any {
	return map[string]any{"s": "x", "nul": "a\x00b", "n": 3.5, "b": true, "z": nil, "list": []any{"a", 1.0}, "map": map[string]any{"k": "v"}}
}

// stored is what TestValuesRoundTripAndAreCopied wrote.
type stored struct {
	deployment   deploy.Deployment
	capabilities contract.DeviceCapabilitiesManifest
	status       contract.DeploymentStatus
}

// check checks s still holds want, then changes everything a reader got.
func (want stored) check(t *testing.T, s storetest.Store, when string) {
	t.Helper()
	ctx := context.Background()
	id := want.deployment.ID
	d, _, err := s.Deployment(ctx, id)
	if err != nil || !reflect.DeepEqual(d, want.deployment) {
		t.Fatalf("%s: deployment %+v (%v), want %+v", when, d, err, want.deployment)
	}
	b, _, _ := s.Blob(ctx, want.deployment.Digest)
	if string(b) != "yaml" {
		t.Fatalf("%s: blob %q", when, b)
	}
	m, _, _ := s.Manifest(ctx, site1)
	if string(m.Body) != `{"manifestVersion":2}` || m.ETag != `"e2"` || m.Version != 2 {
		t.Fatalf("%s: manifest %+v", when, m)
	}
	if cur, ok, _ := s.CurrentStatus(ctx, id); !ok || !reflect.DeepEqual(cur, want.status) {
		t.Errorf("%s: current status %+v, want %+v", when, cur, want.status)
	}
	h, _ := s.StatusHistory(ctx, id)
	if len(h) != 1 || !reflect.DeepEqual(h[0], want.status) {
		t.Fatalf("%s: status history %+v", when, h)
	}
	read(t, s, site1, func(state deploy.SiteState) {
		if !reflect.DeepEqual(state.Hosts[host1.Host], want.capabilities) {
			t.Errorf("%s: capabilities %+v, want %+v", when, state.Hosts[host1.Host], want.capabilities)
		}
		if string(state.YAML[id]) != "yaml" {
			t.Errorf("%s: state YAML %q", when, state.YAML[id])
		}
		state.Hosts[host1.Host].Labels["line"] = "changed"
		state.Deployments[id].Parameters["s"] = "changed"
		state.YAML[id][0] = 'X'
		state.Manifest.Body[0] = 'X'
	})
	d.Parameters["s"] = "changed"
	b[0] = 'X'
	m.Body[0] = 'X'
	h[0].Components[0].Name = "changed"
}

// SPEC §8.3: ChangeDevice sees whether the site's gateway and the device have reported.
func TestChangeDeviceSeesItsDevice(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		ctx := context.Background()
		see := func(site contract.SiteID, id contract.DeviceID, want deploy.DeviceState, wantExists bool) {
			t.Helper()
			if err := s.ChangeDevice(ctx, site, id, func(state deploy.DeviceState, exists bool) (deploy.SiteChange, error) {
				if exists != wantExists || state != want {
					t.Errorf("device %s: state %+v exists %v, want %+v %v", id, state, exists, want, wantExists)
				}
				return deploy.SiteChange{}, nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		gateway := contract.DeviceID{Site: site1}
		at := deploy.Site{ID: site1}
		see("site-x", contract.DeviceID{Site: "site-x"}, deploy.DeviceState{}, false)
		see(site1, gateway, deploy.DeviceState{Site: at}, true)
		see(site1, host1, deploy.DeviceState{Site: at}, true)
		gwCaps, hostCaps := capabilities(gateway), capabilities(host1)
		put(t, s, site1, deploy.SiteChange{Device: &deploy.DeviceChange{ID: gateway, Capabilities: &gwCaps}})
		put(t, s, site1, deploy.SiteChange{Device: &deploy.DeviceChange{ID: host1, Capabilities: &hostCaps}})
		see(site1, gateway, deploy.DeviceState{Site: at, Gateway: true, Device: true}, true)
		see(site1, host1, deploy.DeviceState{Site: at, Gateway: true, Device: true}, true)
		see(site1, contract.DeviceID{Site: site1, Host: "host-2"}, deploy.DeviceState{Site: at, Gateway: true}, true)
		// A device of another site is never the device of this one.
		see(site2, host1, deploy.DeviceState{Site: deploy.Site{ID: site2}}, true)
		put(t, s, site1, deploy.SiteChange{Device: &deploy.DeviceChange{ID: host1}})
		see(site1, host1, deploy.DeviceState{Site: at, Gateway: true}, true)
	})
}

// SPEC §8.1.2: ChangeDeployment sees the deployment only at its own site, the site's manifest
// version, and whether the deployment has a report.
func TestChangeDeploymentSeesItsDeployment(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		ctx := context.Background()
		d := deployment(site1)
		see := func(site contract.SiteID, want deploy.DeploymentState, wantExists bool) {
			t.Helper()
			if err := s.ChangeDeployment(ctx, site, d.ID, func(state deploy.DeploymentState, exists bool) (deploy.SiteChange, error) {
				if exists != wantExists || !reflect.DeepEqual(state, want) {
					t.Errorf("deployment at %s: state %+v exists %v, want %+v %v", site, state, exists, want, wantExists)
				}
				return deploy.SiteChange{}, nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		see("site-x", deploy.DeploymentState{}, false)
		see(site1, deploy.DeploymentState{Site: deploy.Site{ID: site1}, ManifestVersion: 1}, true)
		put(t, s, site1, deploy.SiteChange{
			Deployment: &d, Manifest: &deploy.Manifest{Version: 2, Body: []byte("2"), ETag: `"2"`},
		})
		see(site1, deploy.DeploymentState{Site: deploy.Site{ID: site1}, ManifestVersion: 2, Deployment: d, Found: true}, true)
		see(site2, deploy.DeploymentState{Site: deploy.Site{ID: site2}, ManifestVersion: 1}, true)
		put(t, s, site1, deploy.SiteChange{Status: &deploy.StatusReport{Status: status(d.ID, 2)}})
		see(site1, deploy.DeploymentState{Site: deploy.Site{ID: site1}, ManifestVersion: 2, Deployment: d, Found: true, Reported: true}, true)
		if _, ok, _ := s.CurrentStatus(ctx, d.ID); ok {
			t.Error("a report that is not current became the current status")
		}
	})
}

// A deployment stays at its site: a change of another site that writes it fails and changes
// nothing.
func TestDeploymentStaysAtItsSite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		ctx := context.Background()
		d := deployment(site1)
		put(t, s, site1, deploy.SiteChange{Deployment: &d})
		moved := d
		moved.Name = "moved"
		if err := s.ChangeSite(ctx, site2, func(deploy.SiteState, bool) (deploy.SiteChange, error) {
			return deploy.SiteChange{Deployment: &moved}, nil
		}); err == nil {
			t.Error("site-2 wrote a deployment of site-1")
		}
		if got, ok, _ := s.DeploymentSite(ctx, d.ID); got != site1 || !ok {
			t.Errorf("deployment at %q %v, want site-1", got, ok)
		}
		if got, _, _ := s.Deployment(ctx, d.ID); got.Name != d.Name {
			t.Errorf("deployment name %q, want %q", got.Name, d.Name)
		}
	})
}

// PutDevice needs the device's site.
func TestPutDeviceOfUnknownSiteFails(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		s := newSites(t, newStore)
		id := contract.DeviceID{Site: "site-x", Host: "host-1"}
		if err := s.PutDevice(context.Background(), id, capabilities(id)); err == nil {
			t.Error("device of an unknown site stored")
		}
	})
}

// put writes c to site.
func put(t *testing.T, s storetest.Store, site contract.SiteID, c deploy.SiteChange) {
	t.Helper()
	if err := s.ChangeSite(context.Background(), site, func(deploy.SiteState, bool) (deploy.SiteChange, error) {
		return c, nil
	}); err != nil {
		t.Fatalf("change site %s: %v", site, err)
	}
}

// read calls see with the state of site, and writes nothing.
func read(t *testing.T, s storetest.Store, site contract.SiteID, see func(deploy.SiteState)) {
	t.Helper()
	if err := s.ChangeSite(context.Background(), site, func(state deploy.SiteState, _ bool) (deploy.SiteChange, error) {
		see(state)
		return deploy.SiteChange{}, nil
	}); err != nil {
		t.Fatalf("read site %s: %v", site, err)
	}
}

func deployment(site contract.SiteID) deploy.Deployment {
	return deploy.Deployment{
		ID: uuid.New(), Target: contract.DeviceID{Site: site, Host: "host-1"}, AppID: "app", AppVersion: "1.0.0",
		Profile: "compose", Name: "app", Namespace: "ns", Digest: contract.DigestOf([]byte("yaml")), DigestVersion: 2,
	}
}

func capabilities(id contract.DeviceID) contract.DeviceCapabilitiesManifest {
	return contract.DeviceCapabilitiesManifest{
		Properties: contract.DeviceCapabilities{
			ID: id, Vendor: "acme", Memory: "8Gi",
			CPUs: []contract.CPU{{Cores: 2, Architecture: "arm64"}}, SupportedRuntimes: []string{"docker"},
		},
		Labels: map[string]string{"line": "1"},
	}
}

func status(id uuid.UUID, adopted contract.ManifestVersion) contract.DeploymentStatus {
	return contract.DeploymentStatus{
		DeploymentID: id, DeviceID: host1, AdoptedManifestVersion: adopted,
		Status:     contract.DeploymentState{State: contract.StateInstalled},
		Components: []contract.ComponentStatus{{Name: "web", State: contract.StateInstalled}},
	}
}
