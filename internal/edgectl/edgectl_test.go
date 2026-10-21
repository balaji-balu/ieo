package edgectl

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/operator"
	"github.com/balaji-balu/ieo/internal/co/store"
	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/ocitest"
	"github.com/balaji-balu/ieo/internal/operatorapi"
)

const (
	repo     = "registry.test/example/hello"
	slowRepo = "registry.test/example/slow"
	appID    = "com-example-hello"
	version  = "1.2.3"
)

var (
	site1 = contract.SiteID("site-1")
	host1 = contract.DeviceID{Site: site1, Host: "host-1"}
)

// hello is the Application Description of appID at version: one compose profile with one
// component, and a greeting parameter.
var hello = []byte(`apiVersion: margo.org/v1-alpha1
id: ` + appID + `
metadata:
  name: Hello
  version: ` + version + `
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
`)

// fixture is the CO's operator API, the real handler over a memory store, served over HTTPS on
// 127.0.0.1 with a certificate edgectl trusts through EDGECTL_TLS_CA_FILE (G-F1, G-F4).
type fixture struct {
	t       *testing.T
	reg     *ocitest.Registry
	store   *store.Memory
	deploy  *deploy.Service
	handler http.Handler
	srv     *httptest.Server
	ca      string // the PEM file of srv's certificate
	token   string
}

// newFixture returns the operator API over a store with site-1, its gateway and host-1, and a
// registry holding hello and a repository that never answers. Nothing is imported yet.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	reg := ocitest.NewRegistry()
	reg.PushApp(t, repo, version, hello)
	reg.PushApp(t, slowRepo, version, hello)
	reg.Stall(slowRepo)
	s := store.NewMemory()
	svc := deploy.New(s)
	if _, err := svc.AddSite(ctx, site1); err != nil {
		t.Fatal(err)
	}
	for _, id := range []contract.DeviceID{{Site: site1}, host1} {
		caps := contract.DeviceCapabilitiesManifest{Properties: contract.DeviceCapabilities{ID: id}}
		if err := s.PutDevice(ctx, id, caps); err != nil {
			t.Fatal(err)
		}
	}
	token := rand.Text() + rand.Text() // made up per run: no token in a fixture (SPEC §15.6)
	h := operator.New(operator.Config{
		Token: sha256.Sum256([]byte(token)), Catalog: catalog.New(reg.Open, s), Deploy: svc, Store: s,
		ImportTimeout: 200 * time.Millisecond, Log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	srv := newTLSServer(h)
	t.Cleanup(srv.Close)
	return &fixture{t: t, reg: reg, store: s, deploy: svc, handler: h, srv: srv, ca: caFile(t, srv), token: token}
}

// newTLSServer returns a started HTTPS server on 127.0.0.1 that does not log the TLS handshakes
// edgectl refuses.
func newTLSServer(h http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	return srv
}

// caFile writes srv's certificate to a PEM file and returns its path.
func caFile(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return ca
}

// env is edgectl's environment for f's CO: its URL, token and CA file. Each pair of more sets a
// variable; an empty value unsets it.
func (f *fixture) env(more ...string) map[string]string {
	env := map[string]string{"EDGECTL_CO_URL": f.srv.URL, "EDGECTL_TOKEN": f.token, "EDGECTL_TLS_CA_FILE": f.ca}
	for i := 0; i+1 < len(more); i += 2 {
		env[more[i]] = more[i+1]
	}
	return env
}

type result struct {
	code           int
	stdout, stderr string
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
}

// run runs edgectl in-process with args and env.
func run(env map[string]string, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, func(k string) string { return env[k] }, &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

// raw sends a request with the operator token to the handler, as edgectl would, and returns the
// response.
func (f *fixture) raw(method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// problem returns the problem the handler answers to the request.
func (f *fixture) problem(method, path, body string) contract.Problem {
	f.t.Helper()
	rec := f.raw(method, path, body)
	var p contract.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Title == "" {
		f.t.Fatalf("%s %s: %d %s is not a problem", method, path, rec.Code, rec.Body)
	}
	return p
}

// importHello imports hello through the handler.
func (f *fixture) importHello() {
	f.t.Helper()
	body := fmt.Sprintf(`{"repository":%q,"version":%q}`, repo, version)
	if rec := f.raw(http.MethodPost, operatorapi.AppsPath, body); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		f.t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
}

// createHello creates a deployment of hello on host1 through the handler.
func (f *fixture) createHello() operatorapi.Deployment {
	f.t.Helper()
	f.importHello()
	rec := f.raw(http.MethodPost, operatorapi.DeploymentsPath, `{"appId":"`+appID+`","version":"`+version+`","target":"site-1/host-1"}`)
	var d operatorapi.Deployment
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &d) != nil {
		f.t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	return d
}

// deployments returns the number of deployments the CO has.
func (f *fixture) deployments() int {
	f.t.Helper()
	var all []operatorapi.Deployment
	if err := json.Unmarshal(f.raw(http.MethodGet, operatorapi.DeploymentsPath, "").Body.Bytes(), &all); err != nil {
		f.t.Fatal(err)
	}
	return len(all)
}

// report records a status of d on host1 in state. d must be site-1's first deployment: adding
// the site publishes manifest version 1 and creating d version 2, which the status adopts.
func (f *fixture) report(d uuid.UUID, state contract.ComponentState) {
	f.t.Helper()
	st := contract.DeploymentStatus{
		DeploymentID: d, DeviceID: host1, AdoptedManifestVersion: 2, Status: contract.DeploymentState{State: state},
		Components: []contract.ComponentStatus{{Name: "web", State: state}},
	}
	if _, err := f.deploy.ReportStatus(context.Background(), site1, d, st); err != nil {
		f.t.Fatal(err)
	}
}

// recorder is an HTTPS server that answers every request with answer and counts the requests and
// the Authorization headers it got.
type recorder struct {
	srv *httptest.Server
	mu  sync.Mutex
	n   int
	got []string // Authorization headers
}

func newRecorder(t *testing.T, tls bool, answer http.HandlerFunc) *recorder {
	t.Helper()
	r := &recorder{}
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.n++
		r.got = append(r.got, req.Header.Get("Authorization"))
		r.mu.Unlock()
		answer(w, req)
	})
	if tls {
		r.srv = newTLSServer(h)
	} else {
		r.srv = httptest.NewUnstartedServer(h)
		r.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
		r.srv.Start()
	}
	t.Cleanup(r.srv.Close)
	return r
}

func (r *recorder) requests() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n, append([]string(nil), r.got...)
}

// answerJSON answers with code and body as application/json.
func answerJSON(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

// SPEC §17.7: "`edgectl` sends its token to an `http://` CO URL only when `edgectl.co_insecure` is
// set, exits 2 on such a URL without it, follows no redirects and uses no HTTP proxy (§15.6)."
func TestSpec_17_7_EdgectlSendsTokenToHTTPOnlyWhenInsecure(t *testing.T) {
	f := newFixture(t)
	token := f.token

	// First: net/http reads HTTPS_PROXY once per process, so an edgectl that used it would have
	// read it as unset in an earlier request.
	t.Run("no proxy", func(t *testing.T) { noProxy(t, f) })

	t.Run("http without edgectl.co_insecure", func(t *testing.T) {
		co := newRecorder(t, false, answerJSON(http.StatusOK, "[]"))
		r := run(map[string]string{"EDGECTL_CO_URL": co.srv.URL, "EDGECTL_TOKEN": token}, "app", "list")
		if r.code != 2 || !strings.Contains(r.stderr, "EDGECTL_CO_INSECURE") {
			t.Errorf("%v\nwant exit 2 with an error naming EDGECTL_CO_INSECURE", r)
		}
		if n, _ := co.requests(); n != 0 {
			t.Errorf("the CO got %d requests", n)
		}
	})

	t.Run("http with EDGECTL_CO_INSECURE", func(t *testing.T) { httpWithInsecure(t, token, false) })
	t.Run("http with --co-insecure", func(t *testing.T) { httpWithInsecure(t, token, true) })

	t.Run("redirect", func(t *testing.T) {
		elsewhere := newRecorder(t, true, answerJSON(http.StatusOK, "[]"))
		co := newRecorder(t, true, func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, elsewhere.srv.URL+req.URL.Path, http.StatusFound)
		})
		r := run(map[string]string{
			"EDGECTL_CO_URL": co.srv.URL, "EDGECTL_TOKEN": token, "EDGECTL_TLS_CA_FILE": caFile(t, co.srv),
		}, "app", "list")
		if r.code != 1 || !strings.Contains(r.stderr, "302") {
			t.Errorf("%v\nwant exit 1 naming the 302", r)
		}
		if n, _ := elsewhere.requests(); n != 0 {
			t.Errorf("the redirect target got %d requests", n)
		}
	})

	t.Run("https verified against edgectl.tls.ca_file", func(t *testing.T) {
		if r := run(f.env(), "app", "list"); r.code != 0 {
			t.Errorf("with the CA file: %v\nwant exit 0", r)
		}
	})

	t.Run("https without edgectl.tls.ca_file uses the system roots", func(t *testing.T) {
		co := newRecorder(t, true, answerJSON(http.StatusOK, "[]"))
		r := run(map[string]string{"EDGECTL_CO_URL": co.srv.URL, "EDGECTL_TOKEN": token}, "app", "list")
		if r.code != 1 {
			t.Errorf("%v\nwant exit 1 for a certificate the system roots do not hold", r)
		}
		if n, _ := co.requests(); n != 0 {
			t.Errorf("the CO got %d requests over a certificate edgectl does not trust", n)
		}
	})

	t.Run("https with another CA file", func(t *testing.T) {
		co := newRecorder(t, true, answerJSON(http.StatusOK, "[]"))
		wrong := filepath.Join(t.TempDir(), "wrong.pem")
		if err := os.WriteFile(wrong, otherCertificate(t), 0o600); err != nil {
			t.Fatal(err)
		}
		r := run(map[string]string{"EDGECTL_CO_URL": co.srv.URL, "EDGECTL_TOKEN": token, "EDGECTL_TLS_CA_FILE": wrong},
			"app", "list")
		if r.code != 1 {
			t.Errorf("%v\nwant exit 1", r)
		}
		if n, _ := co.requests(); n != 0 {
			t.Errorf("the CO got %d requests", n)
		}
	})
}

// SPEC §17.7: "`edgectl` exits non-zero on failure and prints the problem `title` and `detail`."
// SPEC §11.3: exit code 1 on a failed request or an unreachable CO, 2 on a usage or configuration
// error; errors go to stderr.
func TestSpec_17_7_EdgectlExitsNonZeroAndPrintsProblemTitleAndDetail(t *testing.T) {
	f := newFixture(t)
	f.importHello()
	importBody := func(repository, v string) string {
		return fmt.Sprintf(`{"repository":%q,"version":%q}`, repository, v)
	}
	createBody := func(target string) string {
		return `{"appId":"` + appID + `","version":"` + version + `","target":"` + target + `"}`
	}
	unknown := uuid.New()

	for _, tc := range []struct {
		name               string
		args               []string
		method, path, body string // the same request, sent to the handler for the problem it answers
	}{
		{"404 app get", []string{"app", "get", "com-example-none"},
			http.MethodGet, operatorapi.AppPath("com-example-none"), ""},
		{"422 import of a tag the registry does not have", []string{"app", "import", "--repo", repo, "--version", "4.5.6"},
			http.MethodPost, operatorapi.AppsPath, importBody(repo, "4.5.6")},
		{"502 import from a registry that does not answer", []string{"app", "import", "--repo", slowRepo, "--version", version},
			http.MethodPost, operatorapi.AppsPath, importBody(slowRepo, version)},
		{"400 deploy to a site", []string{"deploy", "--app", appID, "--version", version, "--target", "site-1"},
			http.MethodPost, operatorapi.DeploymentsPath, createBody("site-1")},
		{"422 deploy to an unknown host", []string{"deploy", "--app", appID, "--version", version, "--target", "site-1/host-9"},
			http.MethodPost, operatorapi.DeploymentsPath, createBody("site-1/host-9")},
		{"404 deployment get", []string{"deployment", "get", unknown.String()},
			http.MethodGet, operatorapi.DeploymentPath(unknown), ""},
		{"404 deployment status", []string{"deployment", "status", unknown.String()},
			http.MethodGet, operatorapi.StatusPath(unknown), ""},
		{"404 deployment list of an unknown site", []string{"deployment", "list", "--site", "site-9"},
			http.MethodGet, operatorapi.DeploymentsPath + "?site=site-9", ""},
		{"404 device list of an unknown site", []string{"device", "list", "--site", "site-9"},
			http.MethodGet, operatorapi.DevicesPath + "?site=site-9", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := f.problem(tc.method, tc.path, tc.body)
			r := run(f.env(), tc.args...)
			if r.code != 1 {
				t.Errorf("%v\nwant exit 1", r)
			}
			if !strings.Contains(r.stderr, p.Title) || (p.Detail != "" && !strings.Contains(r.stderr, p.Detail)) {
				t.Errorf("%v\nwant the problem's title %q and detail %q on stderr", r, p.Title, p.Detail)
			}
			if r.stdout != "" {
				t.Errorf("%v\nwant nothing on stdout", r)
			}
		})
	}

	t.Run("5xx that is not a problem", func(t *testing.T) {
		co := newRecorder(t, true, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "upstream down", http.StatusServiceUnavailable)
		})
		r := run(map[string]string{"EDGECTL_CO_URL": co.srv.URL, "EDGECTL_TOKEN": f.token, "EDGECTL_TLS_CA_FILE": caFile(t, co.srv)},
			"app", "list")
		if r.code != 1 || !strings.Contains(r.stderr, "503") || !strings.Contains(r.stderr, "Service Unavailable") {
			t.Errorf("%v\nwant exit 1 with the status on stderr", r)
		}
	})

	t.Run("unreachable CO", func(t *testing.T) {
		gone := newTLSServer(http.NotFoundHandler())
		gone.Close()
		r := run(f.env("EDGECTL_CO_URL", gone.URL), "app", "list")
		if r.code != 1 || r.stderr == "" || r.stdout != "" {
			t.Errorf("%v\nwant exit 1 with an error on stderr", r)
		}
	})

	t.Run("usage and configuration errors", func(t *testing.T) {
		missingCA := filepath.Join(t.TempDir(), "none.pem")
		for _, tc := range []struct {
			name string
			env  map[string]string
			args []string
		}{
			{"no command", f.env(), nil},
			{"unknown command", f.env(), []string{"apps"}},
			{"unknown flag", f.env(), []string{"app", "list", "--colour"}},
			{"extra argument", f.env(), []string{"app", "list", "x"}},
			{"missing argument", f.env(), []string{"app", "get"}},
			{"deploy without --target", f.env(), []string{"deploy", "--app", appID, "--version", version}},
			{"deployment id that is not a UUID", f.env(), []string{"deployment", "get", "not-a-uuid"}},
			{"--output yaml", f.env(), []string{"app", "list", "--output", "yaml"}},
			{"no EDGECTL_CO_URL", f.env("EDGECTL_CO_URL", ""), []string{"app", "list"}},
			{"no EDGECTL_TOKEN", f.env("EDGECTL_TOKEN", ""), []string{"app", "list"}},
			{"EDGECTL_CO_URL without a host", f.env("EDGECTL_CO_URL", "https://"), []string{"app", "list"}},
			{"EDGECTL_CO_INSECURE not a boolean", f.env("EDGECTL_CO_INSECURE", "maybe"), []string{"app", "list"}},
			{"EDGECTL_TLS_CA_FILE missing", f.env("EDGECTL_TLS_CA_FILE", missingCA), []string{"app", "list"}},
		} {
			r := run(tc.env, tc.args...)
			if r.code != 2 || r.stderr == "" {
				t.Errorf("%s: %v\nwant exit 2 with an error on stderr", tc.name, r)
			}
		}
	})
}

// noProxy is a step of TestSpec_17_7_EdgectlSendsTokenToHTTPOnlyWhenInsecure: with HTTPS_PROXY
// and HTTP_PROXY naming a listening proxy, edgectl does not connect to it.
func noProxy(t *testing.T, f *fixture) {
	proxy, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proxy.Close() }()
	connected := make(chan struct{}, 1)
	go func() {
		for {
			c, err := proxy.Accept()
			if err != nil {
				return
			}
			connected <- struct{}{}
			_ = c.Close()
		}
	}()
	proxyURL := "http://" + proxy.Addr().String()
	t.Setenv("HTTPS_PROXY", proxyURL)
	t.Setenv("HTTP_PROXY", proxyURL)
	t.Setenv("NO_PROXY", "")
	// A name that does not resolve: through a proxy, the proxy would get the request (Go
	// sends requests to 127.0.0.1 directly whatever the environment says).
	r := run(f.env("EDGECTL_CO_URL", "https://co.invalid:8443"), "app", "list")
	if r.code != 1 {
		t.Errorf("%v\nwant exit 1", r)
	}
	select {
	case <-connected:
		t.Error("edgectl connected to the HTTPS_PROXY")
	default:
	}
}

// httpWithInsecure is a step of TestSpec_17_7_EdgectlSendsTokenToHTTPOnlyWhenInsecure: with
// edgectl.co_insecure, from EDGECTL_CO_INSECURE or, if viaFlag, --co-insecure, edgectl sends the
// token to an http:// CO and warns on stderr, naming the URL and not the token.
func httpWithInsecure(t *testing.T, token string, viaFlag bool) {
	co := newRecorder(t, false, answerJSON(http.StatusOK, "[]"))
	env := map[string]string{"EDGECTL_CO_URL": co.srv.URL, "EDGECTL_TOKEN": token}
	args := []string{"app", "list"}
	if viaFlag {
		args = append(args, "--co-insecure")
	} else {
		env["EDGECTL_CO_INSECURE"] = "true"
	}
	r := run(env, args...)
	if r.code != 0 {
		t.Fatalf("%v\nwant exit 0", r)
	}
	if !strings.Contains(r.stderr, co.srv.URL) {
		t.Errorf("%v\nwant a warning naming %s on stderr", r, co.srv.URL)
	}
	if strings.Contains(r.stdout+r.stderr, token) {
		t.Errorf("the output holds the token:\n%v", r)
	}
	if n, got := co.requests(); n != 1 || got[0] != "Bearer "+token {
		t.Errorf("the CO got %d requests, want 1 with the token", n)
	}
}

// otherCertificate returns a self-signed PEM certificate for 127.0.0.1 that did not sign the
// httptest servers' certificate.
func otherCertificate(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "other"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// SPEC §17.7: "No credential, key or token appears in logs or status messages.", for edgectl:
// every command, against a CO that refuses it, that answers with a redirect or an error, and that
// cannot be reached, and with a configuration edgectl refuses, prints the token on neither stdout
// nor stderr (SPEC §15.6).
func TestSpec_17_7_NoCredentialOrTokenInEdgectlOutput(t *testing.T) {
	f := newFixture(t)
	token := f.token
	id := uuid.NewString()
	commands := [][]string{
		{"app", "import", "--repo", repo, "--version", version},
		{"app", "list"},
		{"app", "get", appID},
		{"deploy", "--app", appID, "--version", version, "--target", "site-1/host-1", "--param", "greeting=hi"},
		{"deployment", "list"},
		{"deployment", "get", id},
		{"deployment", "status", id},
		{"device", "list", "--site", "site-1"},
	}
	echo := func(w http.ResponseWriter, req *http.Request) {
		// A CO that repeats what it got: edgectl must not print it either.
		w.Header().Set("Content-Type", contract.ProblemMediaType)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(contract.Problem{
			Type: contract.ProblemAboutBlank, Title: "Internal Server Error", Status: 500,
			Detail: "got " + req.Header.Get("Authorization"),
		})
	}
	refusing := newRecorder(t, true, answerJSON(http.StatusUnauthorized, ""))
	redirecting := newRecorder(t, true, func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "https://elsewhere.invalid/?token="+req.Header.Get("Authorization"), http.StatusFound)
	})
	failing := newRecorder(t, true, http.HandlerFunc(echo))
	plain := newRecorder(t, false, answerJSON(http.StatusBadGateway, "{}"))
	gone := newTLSServer(http.NotFoundHandler())
	gone.Close()
	envs := map[string]map[string]string{
		"401":            {"EDGECTL_CO_URL": refusing.srv.URL, "EDGECTL_TLS_CA_FILE": caFile(t, refusing.srv)},
		"redirect":       {"EDGECTL_CO_URL": redirecting.srv.URL, "EDGECTL_TLS_CA_FILE": caFile(t, redirecting.srv)},
		"500":            {"EDGECTL_CO_URL": failing.srv.URL, "EDGECTL_TLS_CA_FILE": caFile(t, failing.srv)},
		"http, insecure": {"EDGECTL_CO_URL": plain.srv.URL, "EDGECTL_CO_INSECURE": "true"},
		"http":           {"EDGECTL_CO_URL": plain.srv.URL},
		"unreachable":    {"EDGECTL_CO_URL": gone.URL, "EDGECTL_TLS_CA_FILE": f.ca},
		"untrusted":      {"EDGECTL_CO_URL": failing.srv.URL},
		"bad CA file":    {"EDGECTL_CO_URL": failing.srv.URL, "EDGECTL_TLS_CA_FILE": filepath.Join(t.TempDir(), "none")},
	}
	for name, env := range envs {
		env["EDGECTL_TOKEN"] = token
		for _, args := range commands {
			for _, output := range []string{"text", "json"} {
				r := run(env, append(args, "--output", output)...)
				if r.code == 0 {
					t.Errorf("%s, %v: exit 0, want a failure", name, args)
				}
				if strings.Contains(r.stdout+r.stderr, token) {
					t.Errorf("%s, %v --output %s: the output holds the token:\n%v", name, args, output, r)
				}
			}
		}
	}
	// The token is not printed in the help either, nor when it is passed where a flag would be.
	r := run(map[string]string{"EDGECTL_TOKEN": token}, "app", "list", "--token", token)
	if r.code != 2 || strings.Contains(r.stdout+r.stderr, token) {
		t.Errorf("--token: %v\nwant exit 2 without the token", r)
	}
}

// SPEC §11.3: "--version is REQUIRED on app import": edgectl exits 2 and sends nothing.
func TestAppImportRequiresVersion(t *testing.T) {
	co := newRecorder(t, true, answerJSON(http.StatusCreated, "{}"))
	r := run(map[string]string{"EDGECTL_CO_URL": co.srv.URL, "EDGECTL_TOKEN": rand.Text(), "EDGECTL_TLS_CA_FILE": caFile(t, co.srv)},
		"app", "import", "--repo", repo)
	if r.code != 2 || !strings.Contains(r.stderr, "--version") {
		t.Errorf("%v\nwant exit 2 naming --version", r)
	}
	if n, _ := co.requests(); n != 0 {
		t.Errorf("the CO got %d requests", n)
	}
}

// SPEC §11.3: "--param k=v splits at the first = and sends v as a JSON string ... A repeated name is
// a usage error."
func TestDeployParams(t *testing.T) {
	f := newFixture(t)
	f.importHello()
	r := run(f.env(), "deploy", "--app", appID, "--version", version, "--target", "site-1/host-1", "--param", "greeting=a=b")
	if r.code != 0 {
		t.Fatalf("%v\nwant exit 0", r)
	}
	id, err := uuid.Parse(strings.SplitN(r.stdout, "\n", 2)[0])
	if err != nil {
		t.Fatalf("first line of %q is not a deployment ID: %v", r.stdout, err)
	}
	var d operatorapi.Deployment
	if err := json.Unmarshal(f.raw(http.MethodGet, operatorapi.DeploymentPath(id), "").Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if got := d.Parameters["greeting"]; got != "a=b" {
		t.Errorf("greeting %#v, want the string \"a=b\"", got)
	}

	for name, params := range map[string][]string{
		"repeated name": {"--param", "greeting=a", "--param", "greeting=b"},
		"no =":          {"--param", "greeting"},
		"no name":       {"--param", "=a"},
	} {
		before := f.deployments()
		r := run(f.env(), append([]string{"deploy", "--app", appID, "--version", version, "--target", "site-1/host-1"}, params...)...)
		if r.code != 2 {
			t.Errorf("%s: %v\nwant exit 2", name, r)
		}
		if after := f.deployments(); after != before {
			t.Errorf("%s: a deployment was created", name)
		}
	}
}

// SPEC §11.3: "json prints the response body as the CO sent it, on stdout", and "deploy prints the
// new deployment's id on the first line of its text output".
func TestOutput(t *testing.T) {
	f := newFixture(t)
	d := f.createHello()
	f.report(d.ID, contract.StateInstalled)

	t.Run("json", func(t *testing.T) { jsonOutput(t, f, d) })
	t.Run("text", func(t *testing.T) { textOutput(t, f, d) })
	t.Run("deploy prints the id first", func(t *testing.T) { deployPrintsIDFirst(t, f) })
	t.Run("status before the first report", func(t *testing.T) { statusBeforeTheFirstReport(t, f) })
}

// jsonOutput is a step of TestOutput: --output json and -o json print the CO's response as it
// sent it, and text output goes to stdout with nothing on stderr.
func jsonOutput(t *testing.T, f *fixture, d operatorapi.Deployment) {
	for _, tc := range []struct {
		args               []string
		method, path, body string
	}{
		{[]string{"app", "import", "--repo", repo, "--version", version}, // a re-import: 200, the same body
			http.MethodPost, operatorapi.AppsPath, fmt.Sprintf(`{"repository":%q,"version":%q}`, repo, version)},
		{[]string{"app", "list"}, http.MethodGet, operatorapi.AppsPath, ""},
		{[]string{"app", "get", appID}, http.MethodGet, operatorapi.AppPath(appID), ""},
		{[]string{"device", "list"}, http.MethodGet, operatorapi.DevicesPath, ""},
		{[]string{"device", "list", "--site", "site-1"}, http.MethodGet, operatorapi.DevicesPath + "?site=site-1", ""},
		{[]string{"deployment", "list"}, http.MethodGet, operatorapi.DeploymentsPath, ""},
		{[]string{"deployment", "list", "--site", "site-1"}, http.MethodGet, operatorapi.DeploymentsPath + "?site=site-1", ""},
		{[]string{"deployment", "get", d.ID.String()}, http.MethodGet, operatorapi.DeploymentPath(d.ID), ""},
		{[]string{"deployment", "status", d.ID.String()}, http.MethodGet, operatorapi.StatusPath(d.ID), ""},
	} {
		want := f.raw(tc.method, tc.path, tc.body).Body.String()
		r := run(f.env(), append(tc.args, "--output", "json")...)
		if r.code != 0 || r.stdout != want {
			t.Errorf("%v --output json: %v\nwant stdout %q", tc.args, r, want)
		}
		r = run(f.env(), append(tc.args, "-o", "json")...)
		if r.code != 0 || r.stdout != want {
			t.Errorf("%v -o json: %v\nwant stdout %q", tc.args, r, want)
		}
		r = run(f.env(), tc.args...)
		if r.code != 0 || r.stdout == "" || r.stderr != "" {
			t.Errorf("%v: %v\nwant text on stdout and nothing on stderr", tc.args, r)
		}
	}
}

// textOutput is a step of TestOutput: the text output of each listing and get names what it lists.
func textOutput(t *testing.T, f *fixture, d operatorapi.Deployment) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"app", "list"}, []string{appID, version, repo}},
		{[]string{"device", "list"}, []string{"site-1/host-1"}},
		{[]string{"deployment", "list"}, []string{d.ID.String(), appID, "site-1/host-1"}},
		{[]string{"deployment", "get", d.ID.String()}, []string{d.ID.String(), appID, "hello-compose", "site-1/host-1"}},
		{[]string{"deployment", "status", d.ID.String()}, []string{"installed", "site-1/host-1", "web"}},
	} {
		r := run(f.env(), tc.args...)
		for _, w := range tc.want {
			if !strings.Contains(r.stdout, w) {
				t.Errorf("%v: %v\nwant %q in stdout", tc.args, r, w)
			}
		}
	}
}

// deployPrintsIDFirst is a step of TestOutput: deploy prints the new deployment's id on the first
// line of its text output, and the Deployment with --output json.
func deployPrintsIDFirst(t *testing.T, f *fixture) {
	for _, output := range []string{"text", ""} {
		args := []string{"deploy", "--app", appID, "--version", version, "--target", "site-1/*"}
		if output != "" {
			args = append(args, "--output", output)
		}
		r := run(f.env(), args...)
		if r.code != 0 {
			t.Fatalf("%v\nwant exit 0", r)
		}
		id, err := uuid.Parse(strings.SplitN(r.stdout, "\n", 2)[0])
		if err != nil {
			t.Fatalf("first line of %q: %v", r.stdout, err)
		}
		if rec := f.raw(http.MethodGet, operatorapi.DeploymentPath(id), ""); rec.Code != http.StatusOK {
			t.Errorf("deployment %s: %d", id, rec.Code)
		}
	}
	r := run(f.env(), "deploy", "--app", appID, "--version", version, "--target", "site-1/*", "--output", "json")
	var d operatorapi.Deployment
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &d) != nil || d.ID == uuid.Nil {
		t.Errorf("deploy --output json: %v\nwant the created Deployment", r)
	}
}

// statusBeforeTheFirstReport is a step of TestOutput: deployment status before the first report says so.
func statusBeforeTheFirstReport(t *testing.T, f *fixture) {
	d := f.createHello()
	r := run(f.env(), "deployment", "status", d.ID.String())
	if r.code != 0 || r.stdout == "" {
		t.Errorf("%v\nwant exit 0 with a line saying there is no status yet", r)
	}
}

// SPEC §11.3: "deployment status --watch MAY poll GET /api/v1/deployments/{id}/status until
// interrupted, printing each new current status".
func TestDeploymentStatusWatch(t *testing.T) {
	f := newFixture(t)
	d := f.createHello()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	stdout := &syncBuffer{}
	var stderr bytes.Buffer
	done := make(chan int)
	env := f.env()
	c := cli{getenv: func(k string) string { return env[k] }, stdout: stdout, stderr: &stderr,
		after: func(time.Duration) <-chan time.Time { return ticks }}
	go func() { done <- c.run(ctx, []string{"deployment", "status", d.ID.String(), "--watch"}) }()

	// edgectl prints the status after each poll, then waits for the next tick: a tick it takes
	// means the poll before it has printed.
	tick := func() {
		select {
		case ticks <- time.Now():
		case code := <-done:
			t.Fatalf("edgectl exited %d: %s", code, stderr.String())
		}
	}
	tick() // the first poll found no status
	f.report(d.ID, contract.StateInstalling)
	tick() // installing
	tick() // no change
	f.report(d.ID, contract.StateInstalled)
	tick() // installed
	tick()
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit %d after an interrupt, want 0: %s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Count(out, "installing") != 2 || strings.Count(out, "installed") != 2 { // state and component
		t.Errorf("stdout:\n%s\nwant installing and installed once each", out)
	}
	if i, j := strings.Index(out, "installing"), strings.Index(out, "installed"); i < 0 || j < i {
		t.Errorf("stdout:\n%s\nwant installing before installed", out)
	}
}

// syncBuffer is a bytes.Buffer safe for one writer and one reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
