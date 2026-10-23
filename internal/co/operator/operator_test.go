package operator_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/operator"
	"github.com/balaji-balu/ieo/internal/co/store/storetest"
	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/ocitest"
	"github.com/balaji-balu/ieo/internal/operatorapi"
)

const (
	repo      = "registry.test/example/hello"
	otherRepo = "registry.test/mirror/hello"
	badRepo   = "registry.test/example/broken"
	slowRepo  = "registry.test/example/slow"
	twoRepo   = "registry.test/example/two"
	appID     = "com-example-hello"
	version   = "1.2.3"
)

// hello is the Application Description of appID at version.
var hello = description(appID, version, "hello-compose")

var (
	site1 = contract.SiteID("site-1")
	site2 = contract.SiteID("site-2")
	host1 = contract.DeviceID{Site: site1, Host: "host-1"}
	host2 = contract.DeviceID{Site: site2, Host: "host-1"}
)

// description is a Margo Application Description of application id at version, with one compose
// profile per name in profiles and a greeting parameter.
func description(id, version string, profiles ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: margo.org/v1-alpha1
id: %s
metadata:
  name: Hello
  version: %s
  catalog:
    organization:
      - name: Example
deploymentProfiles:
`, id, version)
	for _, p := range profiles {
		fmt.Fprintf(&b, `  - type: compose
    id: %s
    components:
      - name: web
        properties:
          repository: oci://registry.test/example/hello-web
          revision: 1.2.3
`, p)
	}
	b.WriteString(`parameters:
  greeting:
    value: Hello
    targets:
      - pointer: GREETING
        components: [web]
`)
	return []byte(b.String())
}

type fixture struct {
	t       *testing.T
	reg     *ocitest.Registry
	store   storetest.Store
	deploy  *deploy.Service
	handler http.Handler
	token   string
	log     *bytes.Buffer
}

// newFixture returns the operator API over a new store with site-1 and site-2, each with its
// gateway and host-1 reported, and a registry holding the packages the tests import. Nothing is
// imported yet.
func newFixture(t *testing.T, newStore storetest.New) fixture {
	t.Helper()
	ctx := context.Background()
	reg := ocitest.NewRegistry()
	s := newStore(t)
	svc := deploy.New(s)
	for _, site := range []contract.SiteID{site1, site2} {
		if _, err := svc.AddSite(ctx, site); err != nil {
			t.Fatalf("add site %s: %v", site, err)
		}
		for _, id := range []contract.DeviceID{{Site: site}, {Site: site, Host: "host-1"}} {
			if err := s.PutDevice(ctx, id, capabilities(id)); err != nil {
				t.Fatalf("put device %s: %v", id, err)
			}
		}
	}
	token := rand.Text() // made up per run: no token in a fixture (SPEC §15.6)
	log := &bytes.Buffer{}
	return fixture{
		t: t, reg: reg, store: s, deploy: svc, token: token, log: log,
		handler: operator.New(operator.Config{
			Token: sha256.Sum256([]byte(token)), Catalog: catalog.New(reg.Open, s), Deploy: svc, Store: s,
			ImportTimeout: 200 * time.Millisecond, Log: slog.New(slog.NewJSONHandler(log, nil)),
		}),
	}
}

func capabilities(id contract.DeviceID) contract.DeviceCapabilitiesManifest {
	caps := contract.DeviceCapabilitiesManifest{Properties: contract.DeviceCapabilities{ID: id}}
	if id.Host != "" {
		caps.Properties.Memory = "8Gi"
		caps.Labels = map[string]string{"zone": string(id.Site)}
	}
	return caps
}

// with returns f reporting to t, for subtests.
func (f fixture) with(t *testing.T) fixture {
	f.t = t
	return f
}

type response struct {
	code   int
	header http.Header
	body   []byte
}

// do sends a request with the operator token.
func (f fixture) do(method, path, body string) response {
	f.t.Helper()
	return f.send(method, path, body, "Bearer "+f.token)
}

// send sends a request with authorization as its Authorization header, none when empty.
func (f fixture) send(method, path, body, authorization string) response {
	f.t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return response{rec.Code, rec.Header(), rec.Body.Bytes()}
}

// want fails the test unless r has status code.
func (f fixture) want(what string, r response, code int) {
	f.t.Helper()
	if r.code != code {
		f.t.Errorf("%s: %d %s; want %d", what, r.code, r.body, code)
	}
}

// wantJSON fails the test unless r is 200 or 201 as code, with a JSON body that decodes into v.
func (f fixture) wantJSON(what string, r response, code int, v any) {
	f.t.Helper()
	f.want(what, r, code)
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		f.t.Errorf("%s: Content-Type %q, want application/json", what, ct)
	}
	if err := json.Unmarshal(r.body, v); err != nil {
		f.t.Errorf("%s: body %s: %v", what, r.body, err)
	}
}

// wantProblem fails the test unless r is an RFC 9457 about:blank problem with status code, the
// status phrase as title and, for 4xx, a detail that holds each of reasons (SPEC §11.3).
func (f fixture) wantProblem(what string, r response, code int, reasons ...string) {
	f.t.Helper()
	f.want(what, r, code)
	if ct := r.header.Get("Content-Type"); ct != contract.ProblemMediaType {
		f.t.Errorf("%s: Content-Type %q, want %s", what, ct, contract.ProblemMediaType)
	}
	var p contract.Problem
	if err := json.Unmarshal(r.body, &p); err != nil {
		f.t.Errorf("%s: problem %s: %v", what, r.body, err)
		return
	}
	if p.Type != contract.ProblemAboutBlank || p.Title != http.StatusText(code) || p.Status != code {
		f.t.Errorf("%s: problem %+v; want type about:blank, title %q, status %d", what, p, http.StatusText(code), code)
	}
	if code < 500 && p.Detail == "" {
		f.t.Errorf("%s: problem without detail", what)
	}
	for _, reason := range reasons {
		if !strings.Contains(p.Detail, reason) {
			f.t.Errorf("%s: detail %q does not name %q", what, p.Detail, reason)
		}
	}
}

func importBody(repository, version string) string {
	return fmt.Sprintf(`{"repository":%q,"version":%q}`, repository, version)
}

func app(repository, id, version string, desc []byte) operatorapi.App {
	return operatorapi.App{ID: id, Version: version, Repository: repository, Digest: contract.DigestOf(desc)}
}

// SPEC §17.2: "The operator API answers each §11.3 operation with the status codes and resources
// given there"
//
// The operations run in order on one store, as an operator would call them: import, then list,
// then deploy and read back.
func TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes(t *testing.T) {
	steps := []struct {
		name string
		run  func(t *testing.T, f fixture, created *operatorapi.Deployment)
	}{
		{"lists are [] when empty", listsAreEmpty},
		{"import", importApps},
		{"list and get apps", listAndGetApps},
		{"devices", listDevices},
		{"create", createDeployments},
		{"list and get deployments", listAndGetDeployments},
		{"status", getStatus},
		{"update", updateDeployments},
		{"other methods and paths", otherMethodsAndPaths},
	}
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		var created operatorapi.Deployment // by "create", for the steps after it
		for _, step := range steps {
			t.Run(step.name, func(t *testing.T) { step.run(t, f.with(t), &created) })
		}
	})
}

// listsAreEmpty is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
func listsAreEmpty(t *testing.T, f fixture, _ *operatorapi.Deployment) {
	for _, path := range []string{"/api/v1/apps", "/api/v1/deployments", "/api/v1/deployments?site=site-1"} {
		r := f.do(http.MethodGet, path, "")
		f.want(path, r, http.StatusOK)
		if string(bytes.TrimSpace(r.body)) != "[]" {
			f.t.Errorf("GET %s: %s; want []", path, r.body)
		}
	}
}

// importApps is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
func importApps(t *testing.T, f fixture, _ *operatorapi.Deployment) {
	f.reg.PushApp(t, repo, version, hello)
	f.reg.PushApp(t, otherRepo, version, description(appID, version, "other-compose"))
	f.reg.PushApp(t, badRepo, version, description(appID, "9.9.9", "hello-compose"))
	f.reg.PushApp(t, slowRepo, version, hello)
	f.reg.Stall(slowRepo)

	var got operatorapi.App
	f.wantJSON("first import", f.do(http.MethodPost, "/api/v1/apps", importBody(repo, version)), http.StatusCreated, &got)
	if want := app(repo, appID, version, hello); got != want {
		t.Errorf("first import: %+v, want %+v", got, want)
	}
	got = operatorapi.App{}
	f.wantJSON("identical re-import", f.do(http.MethodPost, "/api/v1/apps", importBody(repo, version)), http.StatusOK, &got)
	if want := app(repo, appID, version, hello); got != want {
		t.Errorf("identical re-import: %+v, want %+v", got, want)
	}
	f.wantProblem("same version, other content", f.do(http.MethodPost, "/api/v1/apps", importBody(otherRepo, version)),
		http.StatusConflict, appID, version)
	f.wantProblem("§5.3 rejection", f.do(http.MethodPost, "/api/v1/apps", importBody(badRepo, version)),
		http.StatusUnprocessableEntity, "metadata.version")
	f.wantProblem("tag the registry does not have", f.do(http.MethodPost, "/api/v1/apps", importBody(repo, "4.5.6")),
		http.StatusUnprocessableEntity, "4.5.6")
	f.wantProblem("registry does not answer", f.do(http.MethodPost, "/api/v1/apps", importBody(slowRepo, version)),
		http.StatusBadGateway, "registry.test")
	for name, body := range map[string]string{
		"not JSON":         `{"repository":`,
		"unknown field":    `{"repository":"` + repo + `","version":"` + version + `","tag":"x"}`,
		"no version":       `{"repository":"` + repo + `"}`,
		"no repository":    `{"version":"` + version + `"}`,
		"two JSON values":  importBody(repo, version) + importBody(repo, version),
		"version a number": `{"repository":"` + repo + `","version":1}`,
	} {
		f.wantProblem("import, "+name, f.do(http.MethodPost, "/api/v1/apps", body), http.StatusBadRequest)
	}
}

// listAndGetApps is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
func listAndGetApps(t *testing.T, f fixture, _ *operatorapi.Deployment) {
	// Bytewise order: "1.10.0" before "1.9.0", "com-example-alpha" before "com-example-hello".
	for _, v := range []string{"1.9.0", "1.10.0"} {
		f.reg.PushApp(t, repo, v, description(appID, v, "hello-compose"))
		f.want("import "+v, f.do(http.MethodPost, "/api/v1/apps", importBody(repo, v)), http.StatusCreated)
	}
	alpha := description("com-example-alpha", "1.0.0", "a")
	f.reg.PushApp(t, "registry.test/example/alpha", "1.0.0", alpha)
	f.want("import alpha", f.do(http.MethodPost, "/api/v1/apps", importBody("registry.test/example/alpha", "1.0.0")), http.StatusCreated)

	helloVersions := []operatorapi.App{
		app(repo, appID, "1.10.0", description(appID, "1.10.0", "hello-compose")),
		app(repo, appID, version, hello),
		app(repo, appID, "1.9.0", description(appID, "1.9.0", "hello-compose")),
	}
	var got []operatorapi.App
	f.wantJSON("GET /apps", f.do(http.MethodGet, "/api/v1/apps", ""), http.StatusOK, &got)
	if want := append([]operatorapi.App{app("registry.test/example/alpha", "com-example-alpha", "1.0.0", alpha)},
		helloVersions...); !reflect.DeepEqual(got, want) {
		t.Errorf("GET /apps:\n got %+v\nwant %+v", got, want)
	}
	got = nil
	f.wantJSON("GET /apps/{appId}", f.do(http.MethodGet, "/api/v1/apps/"+appID, ""), http.StatusOK, &got)
	if !reflect.DeepEqual(got, helloVersions) {
		t.Errorf("GET /apps/%s:\n got %+v\nwant %+v", appID, got, helloVersions)
	}
	f.wantProblem("GET /apps/{appId}, none imported", f.do(http.MethodGet, "/api/v1/apps/com-example-none", ""),
		http.StatusNotFound)
}

// listDevices is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
func listDevices(t *testing.T, f fixture, _ *operatorapi.Deployment) {
	all := []operatorapi.Device{}
	for _, id := range []contract.DeviceID{{Site: site1}, host1, {Site: site2}, host2} {
		all = append(all, operatorapi.Device{DeviceID: id, Capabilities: capabilities(id)})
	}
	var got []operatorapi.Device
	f.wantJSON("GET /devices", f.do(http.MethodGet, "/api/v1/devices", ""), http.StatusOK, &got)
	if !reflect.DeepEqual(got, all) {
		t.Errorf("GET /devices:\n got %+v\nwant %+v", got, all)
	}
	got = nil
	f.wantJSON("GET /devices?site=site-2", f.do(http.MethodGet, "/api/v1/devices?site=site-2", ""), http.StatusOK, &got)
	if !reflect.DeepEqual(got, all[2:]) {
		t.Errorf("GET /devices?site=site-2:\n got %+v\nwant %+v", got, all[2:])
	}
	f.wantProblem("unknown site", f.do(http.MethodGet, "/api/v1/devices?site=site-9", ""), http.StatusNotFound, "site-9")
	f.wantProblem("site ID that does not parse", f.do(http.MethodGet, "/api/v1/devices?site=a%2Fb", ""), http.StatusBadRequest)
}

// createDeployments is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
func createDeployments(t *testing.T, f fixture, created *operatorapi.Deployment) {
	f.reg.PushApp(t, twoRepo, version, description("com-example-two", version, "one", "two"))
	f.want("import two", f.do(http.MethodPost, "/api/v1/apps", importBody(twoRepo, version)), http.StatusCreated)

	body := `{"appId":"` + appID + `","version":"` + version + `","target":"site-1/host-1","parameters":{"greeting":"Hi"}}`
	f.wantJSON("create", f.do(http.MethodPost, "/api/v1/deployments", body), http.StatusCreated, created)
	stored, ok, err := f.store.Deployment(context.Background(), created.ID)
	if err != nil || !ok {
		t.Fatalf("created deployment %s not stored: %v", created.ID, err)
	}
	want := operatorapi.Deployment{
		ID: created.ID, AppID: appID, Version: version, Profile: "hello-compose", Target: host1,
		Name: appID, Namespace: appID, Parameters: map[string]any{"greeting": "Hi"}, Digest: stored.Digest,
	}
	if !reflect.DeepEqual(*created, want) || created.Digest == "" {
		t.Errorf("create:\n got %+v\nwant %+v", *created, want)
	}

	create := func(fields string) response {
		return f.do(http.MethodPost, "/api/v1/deployments", "{"+fields+"}")
	}
	f.wantProblem("version not imported", create(`"appId":"`+appID+`","version":"7.7.7","target":"site-1/host-1"`),
		http.StatusNotFound, "7.7.7")
	f.wantProblem("no such profile", create(`"appId":"`+appID+`","version":"`+version+`","profile":"nope","target":"site-1/host-1"`),
		http.StatusUnprocessableEntity, "nope")
	f.wantProblem("several profiles, none named", create(`"appId":"com-example-two","version":"`+version+`","target":"site-1/host-1"`),
		http.StatusUnprocessableEntity, "profile")
	f.wantProblem("unknown host", create(`"appId":"`+appID+`","version":"`+version+`","target":"site-1/host-9"`),
		http.StatusUnprocessableEntity, "host-9")
	f.wantProblem("unknown site", create(`"appId":"`+appID+`","version":"`+version+`","target":"site-9/host-1"`),
		http.StatusUnprocessableEntity, "site-9")
	f.wantProblem("parameter not in the description", create(`"appId":"`+appID+`","version":"`+version+
		`","target":"site-1/host-1","parameters":{"nope":"x"}`), http.StatusUnprocessableEntity, "nope")
	for name, fields := range map[string]string{
		"target names a site":     `"appId":"` + appID + `","version":"` + version + `","target":"site-1"`,
		"target does not parse":   `"appId":"` + appID + `","version":"` + version + `","target":"site 1/host-1"`,
		"no target":               `"appId":"` + appID + `","version":"` + version + `"`,
		"no appId":                `"version":"` + version + `","target":"site-1/host-1"`,
		"no version":              `"appId":"` + appID + `","target":"site-1/host-1"`,
		"unknown field":           `"appId":"` + appID + `","version":"` + version + `","target":"site-1/host-1","replicas":2`,
		"parameters not a object": `"appId":"` + appID + `","version":"` + version + `","target":"site-1/host-1","parameters":[]`,
	} {
		f.wantProblem("create, "+name, create(fields), http.StatusBadRequest)
	}
}

// listAndGetDeployments is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
func listAndGetDeployments(t *testing.T, f fixture, created *operatorapi.Deployment) {
	var other operatorapi.Deployment
	f.wantJSON("create at site-2", f.do(http.MethodPost, "/api/v1/deployments",
		`{"appId":"`+appID+`","version":"`+version+`","target":"site-2/host-1","name":"hello-2","namespace":"ns"}`),
		http.StatusCreated, &other)
	if other.Name != "hello-2" || other.Namespace != "ns" || !reflect.DeepEqual(other.Parameters, map[string]any{}) {
		t.Errorf("create with name and namespace: %+v; want them kept and parameters {}", other)
	}
	if err := f.deploy.Delete(context.Background(), other.ID); err != nil {
		t.Fatal(err)
	}
	other.Deleted = true

	byID := []operatorapi.Deployment{*created, other}
	if byID[1].ID.String() < byID[0].ID.String() {
		byID[0], byID[1] = byID[1], byID[0]
	}
	var got []operatorapi.Deployment
	f.wantJSON("GET /deployments", f.do(http.MethodGet, "/api/v1/deployments", ""), http.StatusOK, &got)
	if !reflect.DeepEqual(got, byID) {
		t.Errorf("GET /deployments:\n got %+v\nwant %+v", got, byID)
	}
	got = nil
	f.wantJSON("GET /deployments?site=site-2", f.do(http.MethodGet, "/api/v1/deployments?site=site-2", ""), http.StatusOK, &got)
	if want := []operatorapi.Deployment{other}; !reflect.DeepEqual(got, want) {
		t.Errorf("GET /deployments?site=site-2:\n got %+v\nwant %+v", got, want)
	}
	f.wantProblem("unknown site", f.do(http.MethodGet, "/api/v1/deployments?site=site-9", ""), http.StatusNotFound, "site-9")

	var one operatorapi.Deployment
	f.wantJSON("GET /deployments/{id}", f.do(http.MethodGet, "/api/v1/deployments/"+created.ID.String(), ""), http.StatusOK, &one)
	if !reflect.DeepEqual(one, *created) {
		t.Errorf("GET /deployments/{id}:\n got %+v\nwant %+v", one, *created)
	}
	f.wantProblem("no such deployment", f.do(http.MethodGet, "/api/v1/deployments/"+uuid.NewString(), ""), http.StatusNotFound)
	f.wantProblem("ID that does not parse", f.do(http.MethodGet, "/api/v1/deployments/nope", ""), http.StatusBadRequest)
}

// getStatus is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
func getStatus(t *testing.T, f fixture, created *operatorapi.Deployment) {
	path := "/api/v1/deployments/" + created.ID.String() + "/status"
	r := f.do(http.MethodGet, path, "")
	f.want("status before a report", r, http.StatusOK)
	if got := string(bytes.TrimSpace(r.body)); got != `{"current":null,"history":[]}` {
		t.Errorf("status before a report: %s; want {\"current\":null,\"history\":[]}", got)
	}

	var reports []contract.DeploymentStatus
	for _, state := range []contract.ComponentState{contract.StateInstalling, contract.StateInstalled} {
		st := contract.DeploymentStatus{
			DeploymentID: created.ID, DeviceID: host1, AdoptedManifestVersion: 2, Status: contract.DeploymentState{State: state},
			Components: []contract.ComponentStatus{{Name: "web", State: state}},
		}
		if _, err := f.deploy.ReportStatus(context.Background(), site1, created.ID, st); err != nil {
			t.Fatal(err)
		}
		reports = append(reports, st)
	}
	var got operatorapi.Status
	f.wantJSON("status after two reports", f.do(http.MethodGet, path, ""), http.StatusOK, &got)
	want := operatorapi.Status{Current: &reports[1], History: reports}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("status after two reports:\n got %+v\nwant %+v", got, want)
	}
	f.wantProblem("no such deployment", f.do(http.MethodGet, "/api/v1/deployments/"+uuid.NewString()+"/status", ""),
		http.StatusNotFound)
}

// updateDeployments is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
// It runs after "status", whose reports adopt the manifest that created the deployment.
func updateDeployments(t *testing.T, f fixture, created *operatorapi.Deployment) {
	f.reg.PushApp(t, repo, "3.0.0", description(appID, "3.0.0", "other-compose"))
	f.want("import 3.0.0", f.do(http.MethodPost, "/api/v1/apps", importBody(repo, "3.0.0")), http.StatusCreated)
	path := "/api/v1/deployments/" + created.ID.String()
	patch := func(body string) response { return f.do(http.MethodPatch, path, body) }

	var updated operatorapi.Deployment
	f.wantJSON("update", patch(`{"parameters":{"greeting":"Hey"}}`), http.StatusOK, &updated)
	want := *created
	want.Parameters, want.Digest = map[string]any{"greeting": "Hey"}, updated.Digest
	if !reflect.DeepEqual(updated, want) || updated.Digest == created.Digest {
		t.Errorf("update:\n got %+v\nwant %+v with a new digest", updated, want)
	}
	var same operatorapi.Deployment
	f.wantJSON("update that changes nothing", patch(`{}`), http.StatusOK, &same)
	if !reflect.DeepEqual(same, updated) {
		t.Errorf("update that changes nothing:\n got %+v\nwant %+v", same, updated)
	}
	*created = updated

	var gone operatorapi.Deployment
	f.wantJSON("create another", f.do(http.MethodPost, "/api/v1/deployments",
		`{"appId":"`+appID+`","version":"`+version+`","target":"site-1/host-1"}`), http.StatusCreated, &gone)
	if err := f.deploy.Delete(context.Background(), gone.ID); err != nil {
		t.Fatal(err)
	}

	f.wantProblem("no such deployment", f.do(http.MethodPatch, "/api/v1/deployments/"+uuid.NewString(), `{}`), http.StatusNotFound)
	f.wantProblem("version not imported", patch(`{"version":"7.7.7"}`), http.StatusNotFound, "7.7.7")
	f.wantProblem("deleted deployment, whatever else the request holds",
		f.do(http.MethodPatch, "/api/v1/deployments/"+gone.ID.String(), `{"version":"7.7.7"}`), http.StatusUnprocessableEntity, "deleted")
	f.wantProblem("profile the new version lacks", patch(`{"version":"3.0.0"}`), http.StatusUnprocessableEntity, "hello-compose")
	f.wantProblem("target at another site", patch(`{"target":"site-2/host-1"}`), http.StatusUnprocessableEntity, "site-2")
	f.wantProblem("unknown host", patch(`{"target":"site-1/host-9"}`), http.StatusUnprocessableEntity, "host-9")
	f.wantProblem("parameter not in the description", patch(`{"parameters":{"nope":"x"}}`), http.StatusUnprocessableEntity, "nope")
	for name, body := range map[string]string{
		"appId":                   `{"appId":"com-example-other"}`,
		"not JSON":                `{"version":`,
		"target names a site":     `{"target":"site-1"}`,
		"target does not parse":   `{"target":"site 1/host-1"}`,
		"unknown field":           `{"replicas":2}`,
		"parameters not a object": `{"parameters":[]}`,
		"version a number":        `{"version":2}`,
	} {
		f.wantProblem("update, "+name, patch(body), http.StatusBadRequest)
	}
	f.wantProblem("update, ID that does not parse", f.do(http.MethodPatch, "/api/v1/deployments/nope", `{}`), http.StatusBadRequest)

	var stored operatorapi.Deployment
	f.wantJSON("GET after rejected updates", f.do(http.MethodGet, path, ""), http.StatusOK, &stored)
	if !reflect.DeepEqual(stored, updated) {
		t.Errorf("after rejected updates:\n got %+v\nwant %+v", stored, updated)
	}
}

// SPEC §17.2: "An update through the operator API keeps every field it does not name"
//
// Version 2.0.0 has no greeting parameter and a new color one; its compose profile keeps its `id`.
func TestSpec_17_2_OperatorAPIUpdateKeepsFieldsItDoesNotName(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		v2 := bytes.Replace(description(appID, "2.0.0", "hello-compose"),
			[]byte("  greeting:\n    value: Hello\n"), []byte("  color:\n    value: blue\n"), 1)
		f.reg.PushApp(t, repo, version, hello)
		f.reg.PushApp(t, repo, "2.0.0", v2)
		for _, v := range []string{version, "2.0.0"} {
			f.want("import "+v, f.do(http.MethodPost, "/api/v1/apps", importBody(repo, v)), http.StatusCreated)
		}
		var d operatorapi.Deployment
		f.wantJSON("create", f.do(http.MethodPost, "/api/v1/deployments", `{"appId":"`+appID+`","version":"`+version+
			`","target":"site-1/host-1","name":"hello-1","namespace":"ns","parameters":{"greeting":"Hi"}}`), http.StatusCreated, &d)
		path := "/api/v1/deployments/" + d.ID.String()

		last := d
		for _, step := range []struct {
			body    string
			version string
			params  map[string]any
		}{
			{`{"parameters":{"greeting":null}}`, version, map[string]any{}},
			{`{"parameters":{"greeting":"Hey"}}`, version, map[string]any{"greeting": "Hey"}},
			{`{"version":"2.0.0","parameters":{"color":"red"}}`, "2.0.0", map[string]any{"color": "red"}},
		} {
			var u operatorapi.Deployment
			f.wantJSON("PATCH "+step.body, f.do(http.MethodPatch, path, step.body), http.StatusOK, &u)
			want := d
			want.Version, want.Parameters, want.Digest = step.version, step.params, u.Digest
			if !reflect.DeepEqual(u, want) || u.Digest == last.Digest {
				t.Errorf("PATCH %s:\n got %+v\nwant %+v with a digest other than %s", step.body, u, want, last.Digest)
			}
			last = u
		}
		f.wantProblem("a name the new version lacks", f.do(http.MethodPatch, path, `{"parameters":{"greeting":"x"}}`),
			http.StatusUnprocessableEntity, "greeting")
		var stored operatorapi.Deployment
		f.wantJSON("GET", f.do(http.MethodGet, path, ""), http.StatusOK, &stored)
		if !reflect.DeepEqual(stored, last) {
			t.Errorf("GET:\n got %+v\nwant %+v", stored, last)
		}
	})
}

// otherMethodsAndPaths is a step of TestSpec_17_2_OperatorAPIAnswersEachOperationWithItsStatusCodes.
func otherMethodsAndPaths(t *testing.T, f fixture, created *operatorapi.Deployment) {
	id := created.ID.String()
	for _, c := range []struct{ method, path, allow string }{
		{http.MethodDelete, "/api/v1/apps", "GET, HEAD, POST"},
		{http.MethodPost, "/api/v1/apps/" + appID, "GET, HEAD"},
		{http.MethodPost, "/api/v1/devices", "GET, HEAD"},
		{http.MethodPut, "/api/v1/deployments", "GET, HEAD, POST"},
		{http.MethodPut, "/api/v1/deployments/" + id, "GET, HEAD, PATCH"},
		{http.MethodDelete, "/api/v1/deployments/" + id, "GET, HEAD, PATCH"},
		{http.MethodPost, "/api/v1/deployments/" + id + "/status", "GET, HEAD"},
	} {
		r := f.do(c.method, c.path, "")
		f.wantProblem(c.method+" "+c.path, r, http.StatusMethodNotAllowed)
		if got := r.header.Get("Allow"); got != c.allow {
			t.Errorf("%s %s: Allow %q, want %q", c.method, c.path, got, c.allow)
		}
	}
	for _, path := range []string{"/api/v1/sites", "/api/v1/devices/site-1", "/api/v1/deployments/" + id + "/stream", "/nope"} {
		f.wantProblem("GET "+path, f.do(http.MethodGet, path, ""), http.StatusNotFound)
	}
}

// SPEC §17.7: "The operator API answers a request without the operator token, or with another"
//
// Every kind of missing or wrong credential gets 401 with WWW-Authenticate: Bearer, on every
// path, before the request is read: an import with no token reaches no registry and stores
// nothing. The token never reaches the log.
func TestSpec_17_7_OperatorAPIAnswers401WithoutTheOperatorToken(t *testing.T) {
	storetest.Each(t, func(t *testing.T, newStore storetest.New) {
		f := newFixture(t, newStore)
		f.reg.PushApp(t, repo, version, description(appID, version, "hello-compose"))
		for name, authorization := range map[string]string{
			"no Authorization":  "",
			"another token":     "Bearer " + rand.Text(),
			"the token, Basic":  "Basic " + f.token,
			"the token, bare":   f.token,
			"Bearer, no token":  "Bearer ",
			"the token's hash":  fmt.Sprintf("Bearer %x", sha256.Sum256([]byte(f.token))),
			"the token, longer": "Bearer " + f.token + "x",
			"the token, prefix": "Bearer " + f.token[:len(f.token)-1],
		} {
			for _, c := range []struct{ method, path, body string }{
				{http.MethodPost, "/api/v1/apps", importBody(repo, version)},
				{http.MethodGet, "/api/v1/apps", ""},
				{http.MethodGet, "/api/v1/deployments", ""},
				{http.MethodPost, "/api/v1/deployments", `{"appId":`}, // malformed: never read
				{http.MethodGet, "/api/v1/nope", ""},
				{http.MethodDelete, "/api/v1/apps", ""},
			} {
				what := name + ": " + c.method + " " + c.path
				r := f.send(c.method, c.path, c.body, authorization)
				f.wantProblem(what, r, http.StatusUnauthorized)
				if got := r.header.Get("WWW-Authenticate"); got != "Bearer" {
					t.Errorf("%s: WWW-Authenticate %q, want Bearer", what, got)
				}
			}
		}
		if n := f.reg.BytesServed(repo); n != 0 {
			t.Errorf("unauthenticated imports read %d bytes from the registry, want 0", n)
		}
		if _, ok, err := f.store.App(context.Background(), appID, version); ok || err != nil {
			t.Errorf("an unauthenticated import stored the app (%v)", err)
		}
		f.want("with the token", f.send(http.MethodGet, "/api/v1/apps", "", "bearer "+f.token), http.StatusOK)
		if strings.Contains(f.log.String(), f.token) {
			t.Error("the log holds the operator token")
		}
	})
}
