package deploy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/storetest"
	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/ocitest"
)

const (
	repo    = "registry.test/example/hello"
	appID   = "com-example-hello"
	version = "1.2.3"
)

// description has vendor extensions at every level, device constraints, a parameter with a
// default value (greeting) and a REQUIRED one (token).
const description = `apiVersion: margo.org/v1-alpha1
id: com-example-hello
metadata:
  name: Hello
  version: 1.2.3
  catalog:
    organization:
      - name: Example
deploymentProfiles:
  - type: compose
    id: hello-compose
    x-acme-extensions: {tier: gold, zones: [b, a]}
    deviceConstraints:
      capacityRequirements:
        memory: 512Mi
      eligibilityRules:
        - labelSelector:
            matchExpressions:
              - {key: line, operator: In, values: ["2"]}
    components:
      - name: web
        x-acme-extensions: {probe: /healthz, port: 8080}
        properties:
          repository: oci://registry.test/example/hello-web
          revision: 1.2.3_build.5
parameters:
  greeting:
    value: Hello
    targets:
      - pointer: GREETING
        components: [web]
  token:
    targets:
      - pointer: TOKEN
        components: [web]
x-acme-extensions: {owner: team-a, retries: 3}
`

var (
	site1 = contract.SiteID("site-1")
	site2 = contract.SiteID("site-2")
	host1 = contract.DeviceID{Site: site1, Host: "host-1"}
)

type fixture struct {
	t      *testing.T
	ctx    context.Context
	store  storetest.Store
	deploy *deploy.Service
}

// newFixture imports the application and adds site-1 with host-1 and site-2 with no hosts.
func newFixture(t *testing.T, newStore storetest.New) fixture {
	t.Helper()
	ctx := context.Background()
	reg := ocitest.NewRegistry()
	reg.PushApp(t, repo, version, []byte(description))
	s := newStore(t)
	if _, err := catalog.New(reg.Open, s).Import(ctx, repo, version); err != nil {
		t.Fatalf("import: %v", err)
	}
	f := fixture{t: t, ctx: ctx, store: s, deploy: deploy.New(s)}
	for _, site := range []contract.SiteID{site1, site2} {
		if _, err := f.deploy.AddSite(ctx, site); err != nil {
			t.Fatalf("add site %s: %v", site, err)
		}
	}
	f.putHost(host1, "8Gi", "2")
	return f
}

// putHost reports a host with memory and a `line` label. The description asks for 512Mi and
// line 2.
func (f fixture) putHost(id contract.DeviceID, memory, line string) {
	f.t.Helper()
	if err := f.store.PutDevice(f.ctx, id, contract.DeviceCapabilitiesManifest{
		Properties: contract.DeviceCapabilities{ID: id, Memory: memory},
		Labels:     map[string]string{"line": line},
	}); err != nil {
		f.t.Fatalf("put device %s: %v", id, err)
	}
}

// request is a valid directed request to host-1.
func request() deploy.Request {
	return deploy.Request{AppID: appID, Version: version, Target: host1, Parameters: map[string]any{"token": "t-1"}}
}

func (f fixture) create(req deploy.Request) deploy.Deployment {
	f.t.Helper()
	d, err := f.deploy.Create(f.ctx, req)
	if err != nil {
		f.t.Fatalf("create: %v", err)
	}
	return d
}

func (f fixture) manifest(site contract.SiteID) (deploy.Manifest, contract.StateManifest) {
	f.t.Helper()
	m, ok, err := f.store.Manifest(f.ctx, site)
	if err != nil || !ok {
		f.t.Fatalf("manifest of %s: ok=%v err=%v", site, ok, err)
	}
	var body contract.StateManifest
	if err := json.Unmarshal(m.Body, &body); err != nil {
		f.t.Fatalf("decode manifest of %s: %v\n%s", site, err, m.Body)
	}
	if m.Version != body.ManifestVersion {
		f.t.Fatalf("manifest of %s: Version %d, body says %d", site, m.Version, body.ManifestVersion)
	}
	return m, body
}

// deploymentCount returns how many deployments the store holds for site, deleted ones included.
func (f fixture) deploymentCount(site contract.SiteID) int {
	f.t.Helper()
	n := 0
	err := f.store.ChangeSite(f.ctx, site, func(state deploy.SiteState, _ bool) (deploy.SiteChange, error) {
		n = len(state.Deployments)
		return deploy.SiteChange{}, nil
	})
	if err != nil {
		f.t.Fatalf("read site %s: %v", site, err)
	}
	return n
}

func (f fixture) blob(d contract.Digest) []byte {
	f.t.Helper()
	b, ok, err := f.store.Blob(f.ctx, d)
	if err != nil || !ok {
		f.t.Fatalf("blob %s: ok=%v err=%v", d, ok, err)
	}
	return b
}

// SPEC §17.2: "Creating a deployment stores YAML under the SHA-256 of its exact bytes."
func TestSpec_17_2_CreateStoresYAMLUnderDigestOfExactBytes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		d := f.create(request())

		b := f.blob(d.Digest)
		if got := contract.DigestOf(b); got != d.Digest {
			t.Fatalf("stored YAML has digest %s, deployment says %s", got, d.Digest)
		}
		js, err := yaml.YAMLToJSON(b)
		if err != nil {
			t.Fatalf("stored YAML: %v\n%s", err, b)
		}
		var doc contract.ApplicationDeployment
		if err := json.Unmarshal(js, &doc); err != nil {
			t.Fatalf("decode stored YAML: %v\n%s", err, js)
		}
		if doc.ID != d.ID || doc.Metadata.DeviceID != host1 || doc.Spec.ApplicationID != appID {
			t.Errorf("stored YAML: id %s, deviceId %s, applicationId %q; want %s, %s, %q",
				doc.ID, doc.Metadata.DeviceID, doc.Spec.ApplicationID, d.ID, host1, appID)
		}

		m, body := f.manifest(site1)
		want := []contract.DeploymentRef{{
			DeploymentID: d.ID, Digest: d.Digest, SizeBytes: uint64(len(b)), URL: contract.DeploymentURL(d.ID, d.Digest),
		}}
		if !reflect.DeepEqual(body.Deployments, want) {
			t.Errorf("manifest deployments = %+v, want %+v", body.Deployments, want)
		}
		if body.Bundle == nil || body.Bundle.Digest != m.Bundle || contract.DigestOf(f.blob(m.Bundle)) != m.Bundle {
			t.Errorf("manifest bundle = %+v, store bundle %s: want the digest of a stored bundle", body.Bundle, m.Bundle)
		}
	})
}

// SPEC §17.2: "Updating a deployment keeps its ID, changes its digest, and increments
// `manifestVersion` by one."
//
// An update that rebuilds identical bytes changes no manifest (SPEC §8.1), and an update can't move
// a deployment to another site.
func TestSpec_17_2_UpdateKeepsIDChangesDigestIncrementsManifestVersion(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		d := f.create(request())
		before, _ := f.manifest(site1)

		req := request()
		req.Parameters["greeting"] = "Hi"
		u, err := f.deploy.Update(f.ctx, d.ID, req)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if u.ID != d.ID || u.Digest == d.Digest {
			t.Fatalf("update: ID %s digest %s; want ID %s and a digest other than %s", u.ID, u.Digest, d.ID, d.Digest)
		}
		after, body := f.manifest(site1)
		if after.Version != before.Version+1 {
			t.Errorf("manifestVersion %d after update, want %d", after.Version, before.Version+1)
		}
		if len(body.Deployments) != 1 || body.Deployments[0].Digest != u.Digest {
			t.Errorf("manifest deployments = %+v, want only %s at %s", body.Deployments, u.ID, u.Digest)
		}

		same, err := f.deploy.Update(f.ctx, d.ID, req)
		if err != nil {
			t.Fatalf("identical update: %v", err)
		}
		unchanged, _ := f.manifest(site1)
		if same.Digest != u.Digest || unchanged.Version != after.Version || !bytes.Equal(unchanged.Body, after.Body) {
			t.Errorf("identical update: digest %s, manifestVersion %d; want %s and %d with the same body",
				same.Digest, unchanged.Version, u.Digest, after.Version)
		}

		moved := request()
		moved.Target = contract.DeviceID{Site: site2, Autonomous: true}
		if _, err := f.deploy.Update(f.ctx, d.ID, moved); !errors.Is(err, deploy.ErrInvalidRequest) {
			t.Errorf("update to another site: err = %v, want ErrInvalidRequest", err)
		}
	})
}

// SPEC §17.2: "Deleting a deployment removes it from the manifest and increments
// `manifestVersion`."
//
// Deleting it again changes nothing; an unknown deployment is not found.
func TestSpec_17_2_DeleteRemovesFromManifestIncrementsManifestVersion(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		d := f.create(request())
		before, _ := f.manifest(site1)

		if err := f.deploy.Delete(f.ctx, d.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		after, body := f.manifest(site1)
		if after.Version != before.Version+1 || len(body.Deployments) != 0 || body.Bundle != nil {
			t.Errorf("after delete: manifestVersion %d, deployments %+v, bundle %+v; want %d, none, null",
				after.Version, body.Deployments, body.Bundle, before.Version+1)
		}

		if err := f.deploy.Delete(f.ctx, d.ID); err != nil {
			t.Fatalf("delete again: %v", err)
		}
		if again, _ := f.manifest(site1); again.Version != after.Version {
			t.Errorf("delete again: manifestVersion %d, want %d", again.Version, after.Version)
		}
		if err := f.deploy.Delete(f.ctx, uuid.New()); !errors.Is(err, deploy.ErrNotFound) {
			t.Errorf("delete unknown: err = %v, want ErrNotFound", err)
		}
		if _, err := f.deploy.Update(f.ctx, d.ID, request()); !errors.Is(err, deploy.ErrNotFound) {
			t.Errorf("update deleted: err = %v, want ErrNotFound", err)
		}
	})
}

// SPEC §17.2: "Preliminary checks reject: unknown site, retired site, unknown directed host, host
// failing constraints, autonomous target with no eligible host. No deployment is created."
//
// The required-parameter checks of SPEC §8.1.1 are here too. The description's profile asks for
// 512Mi of memory and the label line=2 (SPEC §5.5).
func TestSpec_17_2_PreliminaryChecksRejectAndCreateNothing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		tests := []struct {
			name   string
			modify func(f fixture, req *deploy.Request)
		}{
			{"unknown site", func(_ fixture, req *deploy.Request) {
				req.Target = contract.DeviceID{Site: "site-9", Host: "host-1"}
			}},
			{"retired site", func(f fixture, _ *deploy.Request) {
				if err := f.deploy.RetireSite(f.ctx, site1); err != nil {
					f.t.Fatalf("retire: %v", err)
				}
			}},
			{"unknown directed host", func(_ fixture, req *deploy.Request) {
				req.Target = contract.DeviceID{Site: site1, Host: "host-9"}
			}},
			{"host failing eligibility rules", func(f fixture, req *deploy.Request) {
				req.Target = contract.DeviceID{Site: site1, Host: "host-2"}
				f.putHost(req.Target, "8Gi", "3")
			}},
			{"host failing capacity requirements", func(f fixture, req *deploy.Request) {
				req.Target = contract.DeviceID{Site: site1, Host: "host-2"}
				f.putHost(req.Target, "256Mi", "2")
			}},
			{"autonomous target with no eligible host", func(f fixture, req *deploy.Request) {
				req.Target = contract.DeviceID{Site: site2, Autonomous: true}
				f.putHost(contract.DeviceID{Site: site2, Host: "host-2"}, "8Gi", "3")
				f.putHost(contract.DeviceID{Site: site2, Host: "host-3"}, "256Mi", "2")
			}},
			{"autonomous target with no host", func(_ fixture, req *deploy.Request) {
				req.Target = contract.DeviceID{Site: site2, Autonomous: true}
			}},
			{"required parameter without value", func(_ fixture, req *deploy.Request) {
				delete(req.Parameters, "token")
			}},
			{"unknown parameter", func(_ fixture, req *deploy.Request) {
				req.Parameters["colour"] = "red"
			}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				f := newFixture(t, newStore)
				req := request()
				tt.modify(f, &req)
				site := req.Target.Site
				before, hadManifest, _ := f.store.Manifest(f.ctx, site)

				if _, err := f.deploy.Create(f.ctx, req); !errors.Is(err, deploy.ErrCheckFailed) {
					t.Fatalf("create: err = %v, want ErrCheckFailed", err)
				}
				if n := f.deploymentCount(site); n != 0 {
					t.Errorf("%d deployments stored at %s, want 0", n, site)
				}
				after, hasManifest, _ := f.store.Manifest(f.ctx, site)
				if hasManifest != hadManifest || after.Version != before.Version || !bytes.Equal(after.Body, before.Body) {
					t.Errorf("manifest of %s changed: version %d → %d", site, before.Version, after.Version)
				}
			})
		}

		t.Run("autonomous target with one eligible host is accepted", func(t *testing.T) {
			f := newFixture(t, newStore)
			f.putHost(contract.DeviceID{Site: site1, Host: "host-2"}, "8Gi", "3")
			req := request()
			req.Target = contract.DeviceID{Site: site1, Autonomous: true}
			if _, err := f.deploy.Create(f.ctx, req); err != nil {
				t.Errorf("create: %v", err)
			}
		})
	})
}

// SPEC §17.2: "`manifestVersion` starts at 1 per site and is independent across sites."
func TestSpec_17_2_ManifestVersionStartsAtOnePerSite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		for _, site := range []contract.SiteID{site1, site2} {
			if m, _ := f.manifest(site); m.Version != 1 {
				t.Errorf("%s: manifestVersion %d after adding the site, want 1", site, m.Version)
			}
		}
		f.create(request())
		f.create(request())
		if m, _ := f.manifest(site1); m.Version != 3 {
			t.Errorf("%s: manifestVersion %d after two creates, want 3", site1, m.Version)
		}
		if m, _ := f.manifest(site2); m.Version != 1 {
			t.Errorf("%s: manifestVersion %d, want 1: another site's changes must not count", site2, m.Version)
		}
		if _, err := f.deploy.AddSite(f.ctx, site1); err != nil {
			t.Fatalf("add site again: %v", err)
		}
		if m, _ := f.manifest(site1); m.Version != 3 {
			t.Errorf("%s: manifestVersion %d after adding the site again, want 3", site1, m.Version)
		}
	})
}

// SPEC §17.2: "A site with no deployments gets `bundle: null`."
func TestSpec_17_2_SiteWithNoDeploymentsGetsNullBundle(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		m, _ := f.manifest(site2)
		if want := `{"bundle":null,"deployments":[],"manifestVersion":1}`; string(m.Body) != want {
			t.Errorf("manifest of a new site = %s, want %s", m.Body, want)
		}
		if m.Bundle != "" {
			t.Errorf("manifest of a new site has bundle %s, want none", m.Bundle)
		}
	})
}

// SPEC §17.2: "Vendor extensions are copied byte-for-byte; `deviceConstraints` are copied
// unmodified."
//
// "Copied" means the same keys in the same order with the same values (SPEC §4.1.5).
func TestSpec_17_2_VendorExtensionsAndDeviceConstraintsCopiedUnmodified(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		d := f.create(request())

		src := ordered(t, []byte(description))
		got := ordered(t, f.blob(d.Digest))
		srcProfile := lookup(t, src, "deploymentProfiles").([]any)[0].(yaml.MapSlice)
		spec := lookup(t, got, "spec").(yaml.MapSlice)
		gotProfile := lookup(t, spec, "deploymentProfile").(yaml.MapSlice)

		for _, c := range []struct {
			name      string
			from, to  yaml.MapSlice
			key, into string
		}{
			{"top-level extensions → spec", src, spec, "x-acme-extensions", "x-acme-extensions"},
			{"profile extensions", srcProfile, gotProfile, "x-acme-extensions", "x-acme-extensions"},
			{"deviceConstraints", srcProfile, gotProfile, "deviceConstraints", "deviceConstraints"},
			{"components with their extensions", srcProfile, gotProfile, "components", "components"},
		} {
			want, gotV := lookup(t, c.from, c.key), lookup(t, c.to, c.into)
			if !reflect.DeepEqual(gotV, want) {
				t.Errorf("%s: got %#v, want %#v", c.name, gotV, want)
			}
		}
	})
}

func ordered(t *testing.T, b []byte) yaml.MapSlice {
	t.Helper()
	var m yaml.MapSlice
	if err := yaml.UnmarshalWithOptions(b, &m, yaml.UseOrderedMap()); err != nil {
		t.Fatalf("decode YAML: %v\n%s", err, b)
	}
	return m
}

func lookup(t *testing.T, m yaml.MapSlice, key string) any {
	t.Helper()
	for _, item := range m {
		if item.Key == key {
			return item.Value
		}
	}
	t.Fatalf("no key %q in %#v", key, m)
	return nil
}

// SPEC §11.1: a report from a site that no longer exists wraps ErrUnknownSite, and one from a
// retired site wraps ErrSiteRetired, whatever the API's earlier check saw. Nothing is written.
func TestReportsFromUnknownOrRetiredSite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		d := f.create(request())
		gone := contract.SiteID("site-gone")
		if err := f.deploy.RetireSite(f.ctx, site1); err != nil {
			t.Fatal(err)
		}
		caps := func(id contract.DeviceID) contract.DeviceCapabilitiesManifest {
			return contract.DeviceCapabilitiesManifest{Properties: contract.DeviceCapabilities{ID: id}}
		}
		reports := func(site contract.SiteID, id uuid.UUID) map[string]error {
			gateway := contract.DeviceID{Site: site}
			host := contract.DeviceID{Site: site, Host: "host-1"}
			_, capsErr := f.deploy.ReportCapabilities(f.ctx, site, gateway, caps(gateway))
			_, statusErr := f.deploy.ReportStatus(f.ctx, site, id, contract.DeploymentStatus{
				DeploymentID: id, AdoptedManifestVersion: 1, Status: contract.DeploymentState{State: contract.StateInstalled},
			})
			return map[string]error{
				"ReportCapabilities": capsErr,
				"RemoveDevice":       f.deploy.RemoveDevice(f.ctx, site, host),
				"ReportStatus":       statusErr,
			}
		}
		for name, err := range reports(gone, uuid.New()) {
			if !errors.Is(err, deploy.ErrUnknownSite) {
				t.Errorf("%s from an unknown site: %v, want ErrUnknownSite", name, err)
			}
		}
		for name, err := range reports(site1, d.ID) {
			if !errors.Is(err, deploy.ErrSiteRetired) {
				t.Errorf("%s from a retired site: %v, want ErrSiteRetired", name, err)
			}
		}
		if h, _ := f.store.StatusHistory(f.ctx, d.ID); len(h) != 0 {
			t.Errorf("retired site's report stored: %+v", h)
		}
		if f.deploymentCount(gone) != 0 {
			t.Error("unknown site got deployments")
		}
	})
}

// Adding a site reports whether it was new, so `co site add` can refuse an existing site before it
// issues a token (SPEC §15.6).
func TestAddSiteReportsWhetherItCreatedTheSite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		site3 := contract.SiteID("site-3")
		if created, err := f.deploy.AddSite(f.ctx, site3); err != nil || !created {
			t.Errorf("add new site: created=%v err=%v, want true, nil", created, err)
		}
		if created, err := f.deploy.AddSite(f.ctx, site3); err != nil || created {
			t.Errorf("add existing site: created=%v err=%v, want false, nil", created, err)
		}
		if err := f.deploy.RetireSite(f.ctx, site3); err != nil {
			t.Fatal(err)
		}
		if created, err := f.deploy.AddSite(f.ctx, site3); err != nil || created {
			t.Errorf("add retired site: created=%v err=%v, want false, nil", created, err)
		}
	})
}

// SPEC §12: the restore procedure republishes every site's manifest, retired sites included, at
// its version plus by. Deployments, digests and bundles stay as they are, so the old YAML and
// bundle URLs are still served and status reports about them are still current.
func TestRaiseManifestVersions(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		d := f.create(request()) // site-1 at version 2
		if err := f.deploy.RetireSite(f.ctx, site2); err != nil {
			t.Fatal(err)
		}
		before1, body1 := f.manifest(site1)
		before2, _ := f.manifest(site2)
		stored, ok, err := f.store.Deployment(f.ctx, d.ID)
		if err != nil || !ok {
			t.Fatalf("deployment: %v %v", ok, err)
		}

		raised, err := f.deploy.RaiseManifestVersions(f.ctx, 10)
		if err != nil {
			t.Fatalf("raise: %v", err)
		}
		want := []deploy.Raised{{Site: site1, From: 2, To: 12}, {Site: site2, From: 1, To: 11}}
		if !reflect.DeepEqual(raised, want) {
			t.Errorf("raised = %+v, want %+v", raised, want)
		}

		after1, afterBody1 := f.manifest(site1) // also checks the body states the new version
		if after1.Version != 12 {
			t.Errorf("%s: version %d, want 12", site1, after1.Version)
		}
		if after1.ETag == before1.ETag {
			t.Errorf("%s: ETag unchanged (%s); an LO holding it would get 304 and keep the old version", site1, after1.ETag)
		}
		if after1.Bundle != before1.Bundle || !reflect.DeepEqual(afterBody1.Deployments, body1.Deployments) ||
			!reflect.DeepEqual(afterBody1.Bundle, body1.Bundle) {
			t.Errorf("%s: deployments or bundle changed:\nbefore %s\nafter  %s", site1, before1.Body, after1.Body)
		}
		if after2, _ := f.manifest(site2); after2.Version != 11 || after2.ETag == before2.ETag {
			t.Errorf("retired %s: version %d ETag %s, want 11 and a new ETag", site2, after2.Version, after2.ETag)
		}
		if got, _, _ := f.store.Site(f.ctx, site2); !got.Retired {
			t.Errorf("%s no longer retired", site2)
		}
		if got, ok, err := f.store.Deployment(f.ctx, d.ID); err != nil || !ok || !reflect.DeepEqual(got, stored) {
			t.Errorf("deployment changed by the raise:\nbefore %+v\nafter  %+v (ok=%v err=%v)", stored, got, ok, err)
		}
	})
}

// SPEC §12, §8.1.2: after the raise, the YAML and bundle URLs of the old manifest are still served,
// status reports about the deployment are still current, and versions keep growing.
func TestRaiseManifestVersionsKeepsServingAndReports(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		d := f.create(request()) // site-1 at version 2
		if _, err := f.deploy.RaiseManifestVersions(f.ctx, 10); err != nil {
			t.Fatalf("raise: %v", err)
		}
		after1, _ := f.manifest(site1)
		if b, ok, err := f.store.DeploymentYAML(f.ctx, site1, d.ID, d.Digest); err != nil || !ok || len(b) == 0 {
			t.Errorf("deployment YAML no longer served: ok=%v err=%v", ok, err)
		}
		if b, ok, err := f.store.Bundle(f.ctx, site1, after1.Bundle); err != nil || !ok || len(b) == 0 {
			t.Errorf("bundle no longer served: ok=%v err=%v", ok, err)
		}

		// A report at the raised version, and one at the version that first carried the digest,
		// are both current (SPEC §8.1.2).
		for _, adopted := range []contract.ManifestVersion{12, 2} {
			st := contract.DeploymentStatus{DeploymentID: d.ID, AdoptedManifestVersion: adopted,
				Status: contract.DeploymentState{State: contract.StateInstalled}}
			if _, err := f.deploy.ReportStatus(f.ctx, site1, d.ID, st); err != nil {
				t.Fatalf("report at %d: %v", adopted, err)
			}
			if cur, ok, _ := f.store.CurrentStatus(f.ctx, d.ID); !ok || cur.AdoptedManifestVersion != adopted {
				t.Errorf("report at version %d not current: %+v", adopted, cur)
			}
		}

		// Rerunning is safe: versions only grow.
		if _, err := f.deploy.RaiseManifestVersions(f.ctx, 1); err != nil {
			t.Fatalf("raise again: %v", err)
		}
		if m, _ := f.manifest(site1); m.Version != 13 {
			t.Errorf("%s: version %d after raising again by 1, want 13", site1, m.Version)
		}
		// The next change publishes above the raised version.
		f.create(request())
		if m, _ := f.manifest(site1); m.Version != 14 {
			t.Errorf("%s: version %d after a create, want 14", site1, m.Version)
		}
	})
}

// Raising by 0 would not move any version above a restored one (SPEC §12).
func TestRaiseManifestVersionsByZeroIsInvalid(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		if _, err := f.deploy.RaiseManifestVersions(f.ctx, 0); !errors.Is(err, deploy.ErrInvalidRequest) {
			t.Errorf("raise by 0: %v, want ErrInvalidRequest", err)
		}
		if m, _ := f.manifest(site1); m.Version != 1 {
			t.Errorf("raise by 0 changed version to %d", m.Version)
		}
	})
}
