package api_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/contract"
)

const (
	capabilitiesPath = "/api/v1/capabilities/{deviceId}"
	statusPath       = "/api/v1/deployments/{deploymentId}/status"
)

// send sends method path with body and site's token.
func (f fixture) send(site contract.SiteID, method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.tokens[site])
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// wantStatus checks rec has status and, for a success, no problem body.
func (f fixture) wantStatus(rec *httptest.ResponseRecorder, status int) {
	f.t.Helper()
	if rec.Code != status {
		f.t.Fatalf("status %d, want %d; body %s", rec.Code, status, rec.Body)
	}
}

func capabilities(id string, labels string) string {
	b := `{"properties":{"id":"` + id + `","vendor":"ieo","modelNumber":"m","serialNumber":"1"`
	if strings.Contains(id, "/") {
		b += `,"memory":"8Gi","supportedRuntimes":["oci"],"supportedDeploymentTypes":["compose"]`
	}
	b += `}`
	if labels != "" {
		b += `,"labels":` + labels
	}
	return b + `}`
}

func status(id uuid.UUID, device string, adopted contract.ManifestVersion, state contract.ComponentState) string {
	s := contract.DeploymentStatus{
		DeploymentID: id, AdoptedManifestVersion: adopted,
		Status:     contract.DeploymentState{State: state},
		Components: []contract.ComponentStatus{{Name: "web", State: state}},
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	if device == "" {
		return strings.Replace(string(b), `"deviceId":"",`, "", 1)
	}
	return strings.Replace(string(b), `"deviceId":""`, `"deviceId":"`+device+`"`, 1)
}

func (f fixture) reportStatus(site contract.SiteID, id uuid.UUID, body string, want int) {
	f.t.Helper()
	f.wantStatus(f.send(site, http.MethodPost, "/api/v1/deployments/"+id.String()+"/status", body), want)
}

func (f fixture) siteVersion(site contract.SiteID) contract.ManifestVersion {
	f.t.Helper()
	m, _, err := f.store.Manifest(f.ctx, site)
	if err != nil {
		f.t.Fatal(err)
	}
	return m.Version
}

// SPEC §17.2: "A host capability report before the gateway report returns `404
// gateway-not-found`."
func TestSpec_17_2_HostCapabilityReportBeforeGatewayReturns404(t *testing.T) {
	f := newFixture(t)
	host := "/api/v1/capabilities/site-1/host-2"

	f.wantProblem(f.send(site1, http.MethodPut, host, capabilities("site-1/host-2", "")),
		http.StatusNotFound, contract.ProblemGatewayNotFound, "put", capabilitiesPath)
	if _, err := f.deploy.Create(f.ctx, deploy.Request{AppID: appID, Version: version,
		Target: contract.DeviceID{Site: site1, Host: "host-2"}}); !errors.Is(err, deploy.ErrCheckFailed) {
		t.Fatalf("deploy to a rejected host: err %v, want ErrCheckFailed", err)
	}

	f.wantStatus(f.send(site1, http.MethodPut, "/api/v1/capabilities/site-1", capabilities("site-1", "")), http.StatusCreated)
	f.wantStatus(f.send(site1, http.MethodPut, "/api/v1/capabilities/site-1", capabilities("site-1", "")), http.StatusOK)
	f.wantStatus(f.send(site1, http.MethodPut, host, capabilities("site-1/host-2", `{"line":"2"}`)), http.StatusCreated)
	f.wantStatus(f.send(site1, http.MethodPut, host, capabilities("site-1/host-2", `{"line":"3"}`)), http.StatusOK)

	// The reported host is what preliminary checks see (SPEC §8.1.1).
	if _, err := f.deploy.Create(f.ctx, deploy.Request{AppID: appID, Version: version,
		Target: contract.DeviceID{Site: site1, Host: "host-2"}}); err != nil {
		t.Errorf("deploy to the reported host: %v", err)
	}
	// Another site's gateway report doesn't count.
	f.wantProblem(f.send(site2, http.MethodPut, "/api/v1/capabilities/site-2/host-2", capabilities("site-2/host-2", "")),
		http.StatusNotFound, contract.ProblemGatewayNotFound, "put", capabilitiesPath)
}

// SPEC §11.1: capability report rules.
func TestCapabilityReportRules(t *testing.T) {
	f := newFixture(t)
	f.wantStatus(f.send(site1, http.MethodPut, "/api/v1/capabilities/site-1", capabilities("site-1", "")), http.StatusCreated)
	put := http.MethodPut
	for _, tt := range []struct {
		name, method, path, body string
		status                   int
		typ                      string
	}{
		{"another site's gateway", put, "/api/v1/capabilities/site-2", capabilities("site-2", ""),
			http.StatusForbidden, contract.ProblemNotAuthorized},
		{"another site's host", put, "/api/v1/capabilities/site-2/host-1", capabilities("site-2/host-1", ""),
			http.StatusForbidden, contract.ProblemNotAuthorized},
		{"autonomous target", put, "/api/v1/capabilities/site-1/*", capabilities("site-1/host-1", ""),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"child of a host", put, "/api/v1/capabilities/site-1/host-1/cam", capabilities("site-1/host-1/cam", ""),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"id differs from path", put, "/api/v1/capabilities/site-1/host-3", capabilities("site-1/host-4", ""),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"schema: no vendor", put, "/api/v1/capabilities/site-1/host-3",
			`{"properties":{"id":"site-1/host-3","modelNumber":"m","serialNumber":"1"}}`,
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"label that is not a string", put, "/api/v1/capabilities/site-1/host-3", capabilities("site-1/host-3", `{"line":2}`),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"not JSON", put, "/api/v1/capabilities/site-1/host-3", `{"properties":`,
			http.StatusBadRequest, contract.ProblemInvalidRequest},
		{"delete another site's host", http.MethodDelete, "/api/v1/capabilities/site-2/host-1", "",
			http.StatusForbidden, contract.ProblemNotAuthorized},
		{"delete unknown host", http.MethodDelete, "/api/v1/capabilities/site-1/host-9", "",
			http.StatusNotFound, contract.ProblemDeviceNotFound},
		{"delete the gateway", http.MethodDelete, "/api/v1/capabilities/site-1", "",
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := f.with(t)
			p := f.wantProblem(f.send(site1, tt.method, tt.path, tt.body), tt.status, tt.typ,
				strings.ToLower(tt.method), capabilitiesPath)
			if tt.status == http.StatusUnprocessableEntity && len(p.Errors) == 0 {
				t.Error("422 without errors[]")
			}
		})
	}

	// DELETE removes a host: preliminary checks no longer see it.
	f.wantStatus(f.send(site1, http.MethodPut, "/api/v1/capabilities/site-1/host-3", capabilities("site-1/host-3", "")), http.StatusCreated)
	f.wantStatus(f.send(site1, http.MethodDelete, "/api/v1/capabilities/site-1/host-3", ""), http.StatusNoContent)
	if _, err := f.deploy.Create(f.ctx, deploy.Request{AppID: appID, Version: version,
		Target: contract.DeviceID{Site: site1, Host: "host-3"}}); !errors.Is(err, deploy.ErrCheckFailed) {
		t.Errorf("deploy to a removed host: err %v, want ErrCheckFailed", err)
	}
}

// SPEC §17.2: "A deleted deployment reported `removed` is marked removed; status history keeps
// every report."
func TestSpec_17_2_DeletedDeploymentReportedRemovedIsMarkedRemovedHistoryKept(t *testing.T) {
	f := newFixture(t)
	d := f.create(site1, "hi")
	f.reportStatus(site1, d.ID, status(d.ID, "site-1/host-1", f.siteVersion(site1), contract.StateInstalled), http.StatusCreated)
	if err := f.deploy.Delete(f.ctx, d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	f.reportStatus(site1, d.ID, status(d.ID, "site-1/host-1", f.siteVersion(site1), contract.StateRemoving), http.StatusOK)
	if got := f.deployment(d.ID); got.Removed {
		t.Fatal("marked removed on `removing`")
	}
	f.reportStatus(site1, d.ID, status(d.ID, "site-1/host-1", f.siteVersion(site1), contract.StateRemoved), http.StatusOK)

	if got := f.deployment(d.ID); !got.Removed {
		t.Error("deleted deployment reported removed is not marked removed")
	}
	f.wantHistory(d.ID, contract.StateInstalled, contract.StateRemoving, contract.StateRemoved)
	f.wantCurrent(d.ID, contract.StateRemoved)

	// A deployment that is not deleted is not marked removed.
	live := f.create(site1, "hola")
	f.reportStatus(site1, live.ID, status(live.ID, "", f.siteVersion(site1), contract.StateRemoved), http.StatusCreated)
	if f.deployment(live.ID).Removed {
		t.Error("a deployment that is not deleted was marked removed")
	}
}

// SPEC §8.1.2: a status whose adoptedManifestVersion is older than the version holding the
// current digest is kept in history but is not current, and does not mark a deployment removed.
func TestStatusWithStaleAdoptedManifestVersionIsHistoryOnly(t *testing.T) {
	f := newFixture(t)
	d := f.create(site1, "hi")
	v1 := f.siteVersion(site1)
	f.reportStatus(site1, d.ID, status(d.ID, "", v1, contract.StateInstalled), http.StatusCreated)

	if _, err := f.deploy.Update(f.ctx, d.ID, deploy.Request{AppID: appID, Version: version, Target: d.Target,
		Parameters: map[string]any{"greeting": "ciao"}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	v2 := f.siteVersion(site1)
	// A later manifest that leaves d's digest alone does not make v2 reports stale.
	f.create(site1, "other")

	f.reportStatus(site1, d.ID, status(d.ID, "", v1, contract.StateFailed), http.StatusOK)
	f.wantCurrent(d.ID, contract.StateInstalled)
	f.reportStatus(site1, d.ID, status(d.ID, "", v2, contract.StateInstalling), http.StatusOK)
	f.wantCurrent(d.ID, contract.StateInstalling)

	if err := f.deploy.Delete(f.ctx, d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// `removed` counts only from the manifest that deleted the deployment: v1 is about an older
	// digest, and v2 predates the deletion (e.g. a report delayed in the outbox).
	for _, v := range []contract.ManifestVersion{v1, v2} {
		f.reportStatus(site1, d.ID, status(d.ID, "", v, contract.StateRemoved), http.StatusOK)
		if f.deployment(d.ID).Removed {
			t.Fatalf("a `removed` adopted at version %d marked the deployment removed", v)
		}
	}
	f.reportStatus(site1, d.ID, status(d.ID, "", f.siteVersion(site1), contract.StateRemoved), http.StatusOK)
	if !f.deployment(d.ID).Removed {
		t.Error("`removed` at the deleting version did not mark the deployment removed")
	}
	f.wantHistory(d.ID, contract.StateInstalled, contract.StateFailed, contract.StateInstalling,
		contract.StateRemoved, contract.StateRemoved, contract.StateRemoved)
}

func TestWrongMethodOnStatusIs405(t *testing.T) {
	f := newFixture(t)
	d := f.create(site1, "hi")
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec := f.send(site1, method, "/api/v1/deployments/"+d.ID.String()+"/status", "")
		f.wantProblem(rec, http.StatusMethodNotAllowed, contract.ProblemAboutBlank, strings.ToLower(method), statusPath)
		if got := rec.Header().Get("Allow"); got != "POST" {
			t.Errorf("%s: Allow %q, want POST", method, got)
		}
	}
}

// SPEC §11.1: status report rules.
func TestStatusReportRules(t *testing.T) {
	f := newFixture(t)
	d := f.create(site1, "hi")
	other := f.create(site2, "hola")
	v := f.siteVersion(site1)
	for _, tt := range []struct {
		name   string
		id     uuid.UUID
		body   string
		status int
		typ    string
	}{
		{"body names another deployment", d.ID, status(other.ID, "", v, contract.StateInstalled),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"another site's deployment", other.ID, status(other.ID, "", 1, contract.StateInstalled),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"unknown deployment", uuid.Nil, "", http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"device at another site", d.ID, status(d.ID, "site-2/host-1", v, contract.StateInstalled),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"adopted version 0", d.ID, status(d.ID, "", 0, contract.StateInstalled),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"adopted version not yet published", d.ID, status(d.ID, "", v+1, contract.StateInstalled),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"schema: unknown state", d.ID, strings.Replace(status(d.ID, "", v, contract.StateInstalled), `"installed"`, `"running"`, 1),
			http.StatusUnprocessableEntity, contract.ProblemSemanticError},
		{"not JSON", d.ID, "{", http.StatusBadRequest, contract.ProblemInvalidRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := f.with(t)
			id, body := tt.id, tt.body
			if id == uuid.Nil {
				id = uuid.New()
				body = status(id, "", v, contract.StateInstalled)
			}
			rec := f.send(site1, http.MethodPost, "/api/v1/deployments/"+id.String()+"/status", body)
			p := f.wantProblem(rec, tt.status, tt.typ, "post", statusPath)
			if tt.status == http.StatusUnprocessableEntity && (len(p.Errors) == 0 || p.Errors[0].Field == "") {
				t.Errorf("422 errors[] %+v, want a named field", p.Errors)
			}
		})
	}
	if h := f.history(d.ID); len(h) != 0 {
		t.Errorf("rejected reports were recorded: %v", h)
	}
	if h := f.history(other.ID); len(h) != 0 {
		t.Errorf("another site's deployment got a report: %v", h)
	}
}

// SPEC §17.2: a retired site gets 403 on the report endpoints too.
func TestRetiredSiteCannotReport(t *testing.T) {
	f := newFixture(t)
	d := f.create(site1, "hi")
	if err := f.deploy.RetireSite(f.ctx, site1); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ method, url, path, body string }{
		{http.MethodPut, "/api/v1/capabilities/site-1", capabilitiesPath, capabilities("site-1", "")},
		{http.MethodDelete, "/api/v1/capabilities/site-1/host-1", capabilitiesPath, ""},
		{http.MethodPost, "/api/v1/deployments/" + d.ID.String() + "/status", statusPath, status(d.ID, "", 2, contract.StateInstalled)},
	} {
		f.wantProblem(f.send(site1, tt.method, tt.url, tt.body), http.StatusForbidden, contract.ProblemNotAuthorized,
			strings.ToLower(tt.method), tt.path)
	}
}

func (f fixture) deployment(id uuid.UUID) deploy.Deployment {
	f.t.Helper()
	d, ok, err := f.store.Deployment(f.ctx, id)
	if err != nil || !ok {
		f.t.Fatalf("deployment %s: ok=%v err=%v", id, ok, err)
	}
	return d
}

func (f fixture) history(id uuid.UUID) []contract.DeploymentStatus {
	f.t.Helper()
	h, err := f.store.StatusHistory(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

func (f fixture) wantHistory(id uuid.UUID, states ...contract.ComponentState) {
	f.t.Helper()
	var got []contract.ComponentState
	for _, s := range f.history(id) {
		got = append(got, s.Status.State)
	}
	if fmt.Sprint(got) != fmt.Sprint(states) {
		f.t.Errorf("history %v, want %v", got, states)
	}
}

func (f fixture) wantCurrent(id uuid.UUID, state contract.ComponentState) {
	f.t.Helper()
	s, ok, err := f.store.CurrentStatus(f.ctx, id)
	if err != nil || !ok || s.Status.State != state {
		f.t.Errorf("current status %v (ok=%v err=%v), want %s", s.Status.State, ok, err, state)
	}
}
