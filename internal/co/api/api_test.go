package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/api"
	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store"
	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/contract/margotest"
	"github.com/balaji-balu/ieo/internal/ocitest"
)

const (
	repo    = "registry.test/example/hello"
	appID   = "com-example-hello"
	version = "1.2.3"

	// Paths as the pinned OpenAPI file names them.
	manifestPath   = "/api/v1/deployments"
	deploymentPath = "/api/v1/deployments/{deploymentId}/{digest}"
	bundlePath     = "/api/v1/bundles/{digest}"

	immutable = "private, max-age=31536000, immutable"
)

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
    components:
      - name: web
        properties:
          repository: oci://registry.test/example/hello-web
          revision: 1.2.3
parameters:
  greeting:
    value: Hello
    targets:
      - pointer: GREETING
        components: [web]
`

var (
	site1 = contract.SiteID("site-1")
	site2 = contract.SiteID("site-2")
)

type fixture struct {
	t       *testing.T
	ctx     context.Context
	store   *store.Memory
	deploy  *deploy.Service
	handler http.Handler
	api     *margotest.API
	tokens  map[contract.SiteID]string
	log     *bytes.Buffer
}

// newFixture imports the application and adds site-1 and site-2, each with host-1 and a token.
func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	reg := ocitest.NewRegistry()
	reg.PushApp(t, repo, version, []byte(description))
	s := store.NewMemory()
	if _, err := catalog.New(reg.Open, s).Import(ctx, repo, version); err != nil {
		t.Fatalf("import: %v", err)
	}
	tokens := auth.New(s)
	log := &bytes.Buffer{}
	f := fixture{
		t: t, ctx: ctx, store: s, deploy: deploy.New(s),
		handler: api.New(tokens, s, slog.New(slog.NewJSONHandler(log, nil))),
		api:     margotest.Load(t), tokens: map[contract.SiteID]string{}, log: log,
	}
	for _, site := range []contract.SiteID{site1, site2} {
		if err := f.deploy.AddSite(ctx, site); err != nil {
			t.Fatalf("add site %s: %v", site, err)
		}
		host := contract.DeviceID{Site: site, Host: "host-1"}
		if err := s.PutDevice(ctx, host, contract.DeviceCapabilitiesManifest{
			Properties: contract.DeviceCapabilities{ID: host, Memory: "8Gi"},
		}); err != nil {
			t.Fatalf("put device %s: %v", host, err)
		}
		token, err := tokens.Issue(ctx, site)
		if err != nil {
			t.Fatalf("issue token for %s: %v", site, err)
		}
		f.tokens[site] = token
	}
	return f
}

// with returns f reporting to t, for subtests.
func (f fixture) with(t *testing.T) fixture {
	f.t = t
	return f
}

func (f fixture) create(site contract.SiteID, greeting string) deploy.Deployment {
	f.t.Helper()
	d, err := f.deploy.Create(f.ctx, deploy.Request{
		AppID: appID, Version: version, Target: contract.DeviceID{Site: site, Host: "host-1"},
		Parameters: map[string]any{"greeting": greeting},
	})
	if err != nil {
		f.t.Fatalf("create: %v", err)
	}
	return d
}

// get sends GET path with site's token and the headers given as name, value pairs.
func (f fixture) get(site contract.SiteID, path string, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	h := []string{"Authorization", "Bearer " + f.tokens[site]}
	return f.do(http.MethodGet, path, append(h, headers...)...)
}

func (f fixture) do(method, path string, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// manifest returns the manifest site's LO gets.
func (f fixture) manifest(site contract.SiteID) (contract.StateManifest, *httptest.ResponseRecorder) {
	f.t.Helper()
	rec := f.get(site, manifestPath)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("GET %s as %s: %d %s", manifestPath, site, rec.Code, rec.Body)
	}
	var m contract.StateManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		f.t.Fatalf("decode manifest: %v\n%s", err, rec.Body)
	}
	return m, rec
}

// wantProblem checks rec is an RFC 9457 problem with status and type that validates against the
// schema the pinned file gives that response of the operation (method, path), or against
// ProblemDetail if the file lists no such response.
func (f fixture) wantProblem(rec *httptest.ResponseRecorder, status int, typ, method, path string) contract.Problem {
	f.t.Helper()
	if rec.Code != status {
		f.t.Fatalf("status %d, want %d; body %s", rec.Code, status, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != contract.ProblemMediaType {
		f.t.Errorf("Content-Type %q, want %q", ct, contract.ProblemMediaType)
	}
	schema := f.api.Schema(f.t, "ProblemDetail")
	if s := strconv.Itoa(status); f.api.HasResponse(method, path, s) {
		schema = f.api.Response(f.t, method, path, s, contract.ProblemMediaType)
	}
	if err := margotest.Validate(schema, rec.Body.Bytes()); err != nil {
		f.t.Errorf("problem %s does not validate: %v", rec.Body, err)
	}
	var p contract.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		f.t.Fatalf("decode problem: %v", err)
	}
	if p.Type != typ || p.Status != status {
		f.t.Errorf("problem type %q status %d, want %q %d", p.Type, p.Status, typ, status)
	}
	return p
}

// SPEC §17.2: "`If-None-Match` with the current ETag returns `304`."
func TestSpec_17_2_IfNoneMatchWithCurrentETagReturns304(t *testing.T) {
	f := newFixture(t)
	f.create(site1, "hi")
	m, rec := f.manifest(site1)
	etag := rec.Header().Get("ETag")
	if etag != contract.ETag(rec.Body.Bytes()) {
		t.Fatalf("ETag %q, want %q", etag, contract.ETag(rec.Body.Bytes()))
	}
	bundleETag := `"` + string(m.Bundle.Digest) + `"`

	for _, inm := range []string{etag, "W/" + etag, `"sha256:00", ` + etag, "*"} {
		rec := f.get(site1, manifestPath, "If-None-Match", inm)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("manifest, If-None-Match %s: %d %q, want 304 with no body", inm, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("ETag"); got != etag {
			t.Errorf("manifest 304 ETag %q, want %q", got, etag)
		}
		if got := rec.Header().Get("Cache-Control"); got != "private" {
			t.Errorf("manifest 304 Cache-Control %q, want private", got)
		}

		inm = strings.Replace(inm, etag, bundleETag, 1)
		rec = f.get(site1, m.Bundle.URL, "If-None-Match", inm)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("bundle, If-None-Match %s: %d, want 304 with no body", inm, rec.Code)
		}
	}

	// An ETag that is no longer current gets the new manifest.
	f.create(site1, "hello again")
	rec = f.get(site1, manifestPath, "If-None-Match", etag)
	if rec.Code != http.StatusOK {
		t.Fatalf("stale If-None-Match: %d, want 200", rec.Code)
	}
	if rec.Header().Get("ETag") == etag {
		t.Error("new manifest has the old ETag")
	}
}

// SPEC §17.2: "Deployment YAML and bundles are served byte-identical to their digests with
// immutable cache headers; the manifest has `Cache-Control: private`."
func TestSpec_17_2_DeploymentYAMLAndBundlesServedByteIdenticalWithImmutableCacheHeaders(t *testing.T) {
	f := newFixture(t)
	first := f.create(site1, "hi")
	f.create(site1, "bonjour")
	m, rec := f.manifest(site1)

	if got := rec.Header().Get("Cache-Control"); got != "private" {
		t.Errorf("manifest Cache-Control %q, want private", got)
	}
	if got := rec.Header().Get("Content-Type"); got != contract.ManifestMediaType {
		t.Errorf("manifest Content-Type %q, want %q", got, contract.ManifestMediaType)
	}
	schema := f.api.Response(t, "get", manifestPath, "200", contract.ManifestMediaType)
	if err := margotest.Validate(schema, rec.Body.Bytes()); err != nil {
		t.Errorf("manifest does not validate: %v", err)
	}
	stored, _, err := f.store.Manifest(f.ctx, site1)
	if err != nil || !bytes.Equal(rec.Body.Bytes(), stored.Body) {
		t.Errorf("manifest body differs from the published bytes (err %v)", err)
	}

	type content struct {
		url, mediaType string
		digest         contract.Digest
	}
	var all []content
	for _, d := range m.Deployments {
		all = append(all, content{d.URL, contract.DeploymentMediaType, d.Digest})
	}
	all = append(all, content{m.Bundle.URL, contract.BundleMediaType, m.Bundle.Digest})

	// The previous digest of an updated deployment is still served (SPEC §11.1).
	updated, err := f.deploy.Update(f.ctx, first.ID, deploy.Request{
		AppID: appID, Version: version, Target: first.Target, Parameters: map[string]any{"greeting": "ciao"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Digest == first.Digest {
		t.Fatal("update kept the digest")
	}

	for _, c := range all {
		rec := f.get(site1, c.url)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d %s", c.url, rec.Code, rec.Body)
			continue
		}
		if got := contract.DigestOf(rec.Body.Bytes()); got != c.digest {
			t.Errorf("GET %s: body digest %s", c.url, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != immutable {
			t.Errorf("GET %s: Cache-Control %q, want %q", c.url, got, immutable)
		}
		if got := rec.Header().Get("Content-Type"); got != c.mediaType {
			t.Errorf("GET %s: Content-Type %q, want %q", c.url, got, c.mediaType)
		}
		if got := rec.Header().Get("ETag"); got != `"`+string(c.digest)+`"` {
			t.Errorf("GET %s: ETag %q", c.url, got)
		}
	}
}

// SPEC §17.2: "A caller sees only its own site's resources; the path cannot be used to read
// another site."
func TestSpec_17_2_CallerSeesOnlyOwnSiteResources(t *testing.T) {
	f := newFixture(t)
	own1 := f.create(site1, "hi")
	own2 := f.create(site1, "bonjour")
	other := f.create(site2, "hola")
	_, rec := f.manifest(site1)
	stored, _, _ := f.store.Manifest(f.ctx, site1)
	if !bytes.Equal(rec.Body.Bytes(), stored.Body) {
		t.Errorf("site-1 got\n%s\nwant its own manifest\n%s", rec.Body, stored.Body)
	}
	otherManifest, _ := f.manifest(site2)

	notFound := []struct {
		name, url, typ, path string
	}{
		{"another site's deployment", contract.DeploymentURL(other.ID, other.Digest),
			contract.ProblemDeploymentNotFound, deploymentPath},
		{"another site's bundle", otherManifest.Bundle.URL, contract.ProblemInvalidBundle, bundlePath},
		{"own digest under another own deployment", contract.DeploymentURL(own2.ID, own1.Digest),
			contract.ProblemDeploymentNotFound, deploymentPath},
		{"own deployment digest as a bundle", contract.BundleURL(own1.Digest),
			contract.ProblemInvalidBundle, bundlePath},
		{"malformed digest", "/api/v1/deployments/" + own1.ID.String() + "/sha256:XYZ",
			contract.ProblemDeploymentNotFound, deploymentPath},
		{"malformed deployment ID", "/api/v1/deployments/not-a-uuid/" + string(own1.Digest),
			contract.ProblemDeploymentNotFound, deploymentPath},
		{"non-canonical deployment ID", "/api/v1/deployments/" + strings.ToUpper(own1.ID.String()) + "/" + string(own1.Digest),
			contract.ProblemDeploymentNotFound, deploymentPath},
		{"unknown deployment", contract.DeploymentURL(uuid.New(), own1.Digest),
			contract.ProblemDeploymentNotFound, deploymentPath},
	}
	for _, tt := range notFound {
		t.Run(tt.name, func(t *testing.T) {
			f := f.with(t)
			f.wantProblem(f.get(site1, tt.url), http.StatusNotFound, tt.typ, "get", tt.path)
		})
	}

	for name, header := range map[string]string{
		"no token":      "",
		"unknown token": "Bearer not-a-token",
		"other scheme":  "Basic " + f.tokens[site1],
		"bare token":    f.tokens[site1],
	} {
		t.Run(name, func(t *testing.T) {
			f := f.with(t)
			rec := f.do(http.MethodGet, manifestPath, "Authorization", header)
			f.wantProblem(rec, http.StatusUnauthorized, contract.ProblemAboutBlank, "get", manifestPath)
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate %q, want Bearer", got)
			}
		})
	}
}

// SPEC §17.2: "A retired site gets `403` with problem type `#not-authorized`"
func TestSpec_17_2_RetiredSiteGets403NotAuthorized(t *testing.T) {
	f := newFixture(t)
	d := f.create(site1, "hi")
	m, _ := f.manifest(site1)
	if err := f.deploy.RetireSite(f.ctx, site1); err != nil {
		t.Fatalf("retire: %v", err)
	}
	for _, tt := range []struct{ url, path string }{
		{manifestPath, manifestPath},
		{contract.DeploymentURL(d.ID, d.Digest), deploymentPath},
		{m.Bundle.URL, bundlePath},
	} {
		p := f.wantProblem(f.get(site1, tt.url), http.StatusForbidden, contract.ProblemNotAuthorized, "get", tt.path)
		if p.Title != "Client Relationship Retired" {
			t.Errorf("GET %s: title %q, want Client Relationship Retired", tt.url, p.Title)
		}
	}
	f.manifest(site2) // other sites are unaffected
}

// SPEC §11.1: `Accept` must admit the manifest media type, else 406.
func TestManifestAcceptNegotiation(t *testing.T) {
	f := newFixture(t)
	for accept, want := range map[string]int{
		"":                         http.StatusOK,
		"*/*":                      http.StatusOK,
		"application/*":            http.StatusOK,
		contract.ManifestMediaType: http.StatusOK,
		"application/json, " + contract.ManifestMediaType + ";q=0.5": http.StatusOK,
		"application/json":                  http.StatusNotAcceptable,
		contract.ManifestMediaType + ";q=0": http.StatusNotAcceptable,
		"text/*":                            http.StatusNotAcceptable,
		"application/*, " + contract.ManifestMediaType + ";q=0": http.StatusNotAcceptable,
		contract.ManifestMediaType + ";q=0, */*":                http.StatusNotAcceptable,
	} {
		rec := f.get(site1, manifestPath, "Accept", accept)
		if want == http.StatusOK {
			if rec.Code != want {
				t.Errorf("Accept %q: %d, want 200", accept, rec.Code)
			}
			continue
		}
		f.wantProblem(rec, want, contract.ProblemCannotGenerate, "get", manifestPath)
	}
}

// SPEC §15.4: a store failure is logged and answered with 500, and the caller's token never
// appears in the log or the response.
func TestStoreFailureLogsNoToken(t *testing.T) {
	f := newFixture(t)
	f.handler = api.New(auth.New(f.store), failingStore{f.store}, slog.New(slog.NewJSONHandler(f.log, nil)))
	rec := f.get(site1, manifestPath)
	f.wantProblem(rec, http.StatusInternalServerError, contract.ProblemAboutBlank, "get", manifestPath)
	if f.log.Len() == 0 {
		t.Error("store failure not logged")
	}
	for _, out := range []string{f.log.String(), rec.Body.String()} {
		if strings.Contains(out, f.tokens[site1]) {
			t.Errorf("token appears in %q", out)
		}
	}
	if strings.Contains(rec.Body.String(), "disk on fire") {
		t.Errorf("response leaks the internal error: %s", rec.Body)
	}
}

type failingStore struct{ *store.Memory }

func (failingStore) Manifest(context.Context, contract.SiteID) (deploy.Manifest, bool, error) {
	return deploy.Manifest{}, false, errors.New("disk on fire")
}

func TestUnknownPathIsProblem(t *testing.T) {
	f := newFixture(t)
	f.wantProblem(f.get(site1, "/api/v1/nothing"), http.StatusNotFound, contract.ProblemAboutBlank, "get", "/api/v1/nothing")
}

func TestWrongMethodOnKnownPathIs405(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodDelete, manifestPath, "Authorization", "Bearer "+f.tokens[site1])
	f.wantProblem(rec, http.StatusMethodNotAllowed, contract.ProblemAboutBlank, "delete", manifestPath)
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow %q, want GET, HEAD", got)
	}
}
