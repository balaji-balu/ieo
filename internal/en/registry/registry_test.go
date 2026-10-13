package registry_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"oras.land/oras-go/v2/errdef"

	"github.com/balaji-balu/ieo/internal/en/pull"
	"github.com/balaji-balu/ieo/internal/en/registry"
	"github.com/balaji-balu/ieo/internal/ocitest"
)

// Everything a registry, a token service or a proxy could be given here is made up for these
// tests, and none is kept in a file of the repository (SPEC §15.6).
const (
	user           = "puller"
	password       = "made-up-registry-password"
	identityToken  = "made-up-identity-token"
	bearerToken    = "made-up-bearer-token"
	anonymousToken = "made-up-anonymous-token"
	proxyPassword  = "made-up-proxy-password"
	// signature stands for what a registry puts in the URL it redirects a layer to.
	signature = "made-up-signature"
	// serverText is what the servers here put in the message of an error.
	serverText = "made-up-server-text"

	name     = "acme/web" // a repository, as its registry names it
	revision = "1.2.3"
)

// basicAuth is the `auth` value of user and password, and what a Basic Authorization holds.
var basicAuth = base64.StdEncoding.EncodeToString([]byte(user + ":" + password))

// secrets must be in no log line and in no error (SPEC §15.4).
var secrets = []string{password, basicAuth, identityToken, bearerToken, anonymousToken, proxyPassword, signature, serverText}

var roomy = pull.Limits{MaxBytes: 1 << 20, Timeout: time.Minute}

// fixture is the content of the registries of a test, and what the code under test logged and
// returned. When the test ends, it fails if any of that holds a secret.
type fixture struct {
	reg     *ocitest.Registry
	archive []byte
	logs    *bytes.Buffer
	texts   []string // the errors seen
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// A test names its own proxy; the one of the machine it runs on is not part of it.
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	f := &fixture{reg: ocitest.NewRegistry(), archive: []byte("a Compose archive, not opened here"), logs: &bytes.Buffer{}}
	t.Cleanup(func() {
		for _, text := range append(f.texts, f.logs.String()) {
			for _, secret := range secrets {
				if strings.Contains(text, secret) {
					t.Errorf("%q is in a log line or an error:\n%s", secret, text)
				}
			}
		}
	})
	return f
}

// push puts the archive in the repository of the registry at each host.
func (f *fixture) push(t *testing.T, hosts ...string) {
	t.Helper()
	for _, host := range hosts {
		f.reg.PushComposeArchive(t, host+"/"+name, revision, f.archive)
	}
}

// new returns registry.New(cfg), logging to the fixture.
func (f *fixture) new(cfg registry.Config) (*registry.Client, error) {
	cfg.Log = slog.New(slog.NewJSONHandler(f.logs, nil))
	c, err := registry.New(cfg)
	if err != nil {
		f.texts = append(f.texts, err.Error())
	}
	return c, err
}

// client returns the Client for cfg, which must be a valid one.
func (f *fixture) client(t *testing.T, cfg registry.Config) *registry.Client {
	t.Helper()
	c, err := f.new(cfg)
	if err != nil || c == nil {
		t.Fatalf("New = %v, %v; want a Client", c, err)
	}
	return c
}

// pull pulls the archive from the registry at host with limits l, as the EN does, and returns the
// error of the pull. A pull that succeeds must give the archive pushed.
func (f *fixture) pull(t *testing.T, c *registry.Client, host string, l pull.Limits) error {
	t.Helper()
	layer, err := pull.New(c.Open, filepath.Join(t.TempDir(), "pull"), l).
		ComposeArchive(context.Background(), "oci://"+host+"/"+name, revision)
	if err != nil {
		f.texts = append(f.texts, err.Error())
		return err
	}
	got, err := io.ReadAll(layer)
	if cerr := layer.Close(); err != nil || cerr != nil {
		t.Fatalf("read the pulled archive: %v, %v", err, cerr)
	}
	if !bytes.Equal(got, f.archive) {
		t.Fatalf("pulled %q from %s; want the archive pushed", got, host)
	}
	return nil
}

// mustPull is a pull that must succeed.
func (f *fixture) mustPull(t *testing.T, c *registry.Client, host string) {
	t.Helper()
	if err := f.pull(t, c, host, roomy); err != nil {
		t.Fatalf("pull from %s: %v", host, err)
	}
}

// mustFail is a pull that must fail with an error that holds each of texts.
func (f *fixture) mustFail(t *testing.T, c *registry.Client, host string, texts ...string) {
	t.Helper()
	err := f.pull(t, c, host, roomy)
	if err == nil {
		t.Fatalf("pull from %s succeeded; want an error", host)
	}
	for _, text := range texts {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("pull from %s: error %q; want %q in it", host, err, text)
		}
	}
}

// logged returns the log lines that have the attribute key.
func (f *fixture) logged(t *testing.T, key string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(f.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var attrs map[string]any
		if err := json.Unmarshal([]byte(line), &attrs); err != nil {
			t.Fatalf("log line %q is not one JSON object: %v", line, err)
		}
		if _, ok := attrs[key]; ok {
			lines = append(lines, attrs)
		}
	}
	return lines
}

// The attributes that tell the startup lines of New apart.
const (
	attrHosts    = "hosts"                // the registries with a credential
	attrHelpers  = "keys"                 // the credential helper keys that are ignored
	attrInsecure = "credentials_in_clear" // the en.registry.insecure warning
	attrProxy    = "proxy"                // in that warning: the proxy a plain-HTTP request goes through
)

// authFile writes a Docker config.json with the given content and returns its path.
func authFile(t *testing.T, content any) string {
	t.Helper()
	data, ok := content.(string)
	if !ok {
		encoded, err := json.Marshal(content)
		if err != nil {
			t.Fatalf("encode the auth file: %v", err)
		}
		data = string(encoded)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write the auth file: %v", err)
	}
	return path
}

type entry = map[string]string

// auths is a config.json with these `auths`.
func auths(entries map[string]entry) map[string]any {
	return map[string]any{"auths": entries}
}

// withPassword is the entry `docker login` writes for user and password.
func withPassword() entry { return entry{"auth": basicAuth} }

// request is what a server was sent.
type request struct {
	method, host, path, authorization string
}

// seen records the requests a server is sent.
type seen struct {
	mu       sync.Mutex
	requests []request
}

func (s *seen) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, request{r.Method, r.Host, r.URL.Path, r.Header.Get("Authorization")})
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (s *seen) all() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// authorizations returns the different Authorization headers the server was sent, sorted.
func (s *seen) authorizations() []string {
	var values []string
	for _, r := range s.all() {
		if r.authorization != "" && !slices.Contains(values, r.authorization) {
			values = append(values, r.authorization)
		}
	}
	slices.Sort(values)
	return values
}

// start serves h on 127.0.0.1 over plain HTTP and returns the server's `host:port`.
func start(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// startTLS serves h on 127.0.0.1 over HTTPS, with a certificate no system root signed. It returns
// the server's `host:port` and the roots that verify it.
func startTLS(t *testing.T, h http.Handler) (string, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	return strings.TrimPrefix(srv.URL, "https://"), roots
}

// refuse answers with status and an error whose message is serverText.
func refuse(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"errors":[{"code":"`+code+`","message":"`+serverText+`"}]}`) // the client under test sees a failed write
}

// basic serves next to user and password, and asks anyone else for them.
func basic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == user && p == password {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		refuse(w, http.StatusUnauthorized, "UNAUTHORIZED")
	})
}

// bearer serves next to a request with the bearer token, and sends anyone else to the token
// service at realm.
func bearer(realm, token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+token {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="registry.test"`)
		refuse(w, http.StatusUnauthorized, "UNAUTHORIZED")
	})
}

// tokenService gives bearerToken for user and password or for identityToken, and anonymousToken to
// a request with no credential.
func tokenService() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := func(field, token string) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"`+field+`":"`+token+`"}`) // the client under test sees a failed write
		}
		u, p, hasBasic := r.BasicAuth()
		switch {
		case r.Method == http.MethodPost && r.PostFormValue("grant_type") == "refresh_token" &&
			r.PostFormValue("refresh_token") == identityToken:
			answer("access_token", bearerToken)
		case r.Method == http.MethodGet && hasBasic && u == user && p == password:
			answer("token", bearerToken)
		case r.Method == http.MethodGet && r.Header.Get("Authorization") == "":
			answer("token", anonymousToken)
		default:
			refuse(w, http.StatusUnauthorized, "UNAUTHORIZED")
		}
	})
}

// redirectLayers redirects each request for a blob to the same path at the base URL to, with a
// signature in the query, as a registry that keeps its layers on a storage host does.
func redirectLayers(to string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/blobs/") {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, to+r.URL.Path+"?sig="+signature, http.StatusTemporaryRedirect)
	})
}

// SPEC §17.7: "The EN sends a registry credential only to the registry it is listed for and to the
// token service that registry names, never to the target of a redirect". Here: another registry,
// on another port of the same host, is sent none.
func TestSpec_17_7_RegistryCredentialGoesOnlyToItsRegistry(t *testing.T) {
	f := newFixture(t)
	var listed, other seen
	hostA := start(t, listed.record(basic(f.reg.Handler())))
	hostB := start(t, other.record(basic(f.reg.Handler())))
	f.push(t, hostA, hostB)
	c := f.client(t, registry.Config{Insecure: true, AuthFile: authFile(t, auths(map[string]entry{hostA: withPassword()}))})

	f.mustPull(t, c, hostA)
	if got := listed.authorizations(); !slices.Equal(got, []string{"Basic " + basicAuth}) {
		t.Errorf("the registry with an entry was sent %q; want its credential", got)
	}

	f.mustFail(t, c, hostB, hostB, "401")
	if len(other.all()) == 0 {
		t.Fatal("the registry with no entry was sent no request")
	}
	if got := other.authorizations(); len(got) != 0 {
		t.Errorf("the registry with no entry was sent %q; want no credential", got)
	}
}

// SPEC §17.7: "The EN sends a registry credential only to the registry it is listed for and to the
// token service that registry names, never to the target of a redirect". Here: the redirect, to
// another port of the registry's own host, which Go's HTTP client takes for the same host.
func TestSpec_17_7_RegistryCredentialIsNotSentToARedirectTarget(t *testing.T) {
	f := newFixture(t)
	var storage, broken seen
	hostS := start(t, storage.record(f.reg.Handler()))
	hostA := start(t, basic(redirectLayers("http://"+hostS, f.reg.Handler())))
	hostX := start(t, broken.record(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refuse(w, http.StatusForbidden, "DENIED")
	})))
	hostC := start(t, basic(redirectLayers("http://"+hostX, f.reg.Handler())))
	f.push(t, hostA, hostS, hostC)
	c := f.client(t, registry.Config{Insecure: true, AuthFile: authFile(t, auths(map[string]entry{
		hostA: withPassword(), hostC: withPassword(),
	}))})

	f.mustPull(t, c, hostA)
	if len(storage.all()) == 0 {
		t.Fatal("the layer did not come from the storage host")
	}
	if got := storage.authorizations(); len(got) != 0 {
		t.Errorf("the storage host was sent %q; want no credential", got)
	}

	// The error names both hosts and the status, and not the signed URL.
	f.mustFail(t, c, hostC, hostC, hostX, "403")
	if got := broken.authorizations(); len(got) != 0 {
		t.Errorf("the storage host that refuses was sent %q; want no credential", got)
	}
}

// SPEC §17.7: "The EN sends a registry credential only to the registry it is listed for and to the
// token service that registry names, never to the target of a redirect". Here: the token service,
// which gets the credential only for the registry that has one.
func TestSpec_17_7_RegistryCredentialGoesToTheTokenServiceItsRegistryNames(t *testing.T) {
	f := newFixture(t)
	var tokens, listed, other seen
	realm := "http://" + start(t, tokens.record(tokenService())) + "/token"
	hostA := start(t, listed.record(bearer(realm, bearerToken, f.reg.Handler())))
	// B names the same token service, and takes no token that service gives.
	hostB := start(t, other.record(bearer(realm, "a-token-nobody-is-given", f.reg.Handler())))
	f.push(t, hostA, hostB)
	c := f.client(t, registry.Config{Insecure: true, AuthFile: authFile(t, auths(map[string]entry{hostA: withPassword()}))})

	f.mustPull(t, c, hostA)
	if got := tokens.authorizations(); !slices.Equal(got, []string{"Basic " + basicAuth}) {
		t.Errorf("the token service was sent %q for the registry with an entry; want its credential", got)
	}
	if got := listed.authorizations(); !slices.Equal(got, []string{"Bearer " + bearerToken}) {
		t.Errorf("the registry was sent %q; want only the token of its token service", got)
	}

	before := len(tokens.all())
	f.mustFail(t, c, hostB, hostB, "401")
	asked := tokens.all()[before:]
	if len(asked) == 0 {
		t.Fatal("the token service was not asked for the registry with no entry")
	}
	for _, r := range asked {
		if r.authorization != "" {
			t.Errorf("the token service was sent %q for the registry with no entry; want no credential", r.authorization)
		}
	}
	if got := other.authorizations(); !slices.Equal(got, []string{"Bearer " + anonymousToken}) {
		t.Errorf("the registry with no entry was sent %q; want only the anonymous token", got)
	}
}

// ADR 0021: an `identitytoken` is a credential too. It goes to the token service, and the
// registry is sent only the token that service gives.
func TestIdentityTokenIsSentToTheTokenService(t *testing.T) {
	f := newFixture(t)
	var tokens, listed seen
	realm := "http://" + start(t, tokens.record(tokenService())) + "/token"
	hostA := start(t, listed.record(bearer(realm, bearerToken, f.reg.Handler())))
	f.push(t, hostA)
	// What `docker login` writes for an identity token: an `auth` with no password beside it.
	noPassword := base64.StdEncoding.EncodeToString([]byte("00000000-0000-0000-0000-000000000000:"))
	c := f.client(t, registry.Config{Insecure: true, AuthFile: authFile(t, auths(map[string]entry{
		hostA: {"auth": noPassword, "identitytoken": identityToken},
	}))})

	f.mustPull(t, c, hostA)
	if asked := tokens.all(); len(asked) == 0 || asked[0].method != http.MethodPost {
		t.Errorf("the token service was sent %+v; want the identity token in a POST", asked)
	}
	if got := listed.authorizations(); !slices.Equal(got, []string{"Bearer " + bearerToken}) {
		t.Errorf("the registry was sent %q; want only the token of its token service", got)
	}
}

// ADR 0021: Docker Hub's three names are one registry, in a key and in a repository; and the
// proxy of the environment is used, and named without its password.
func TestDockerHubNamesAreOneRegistryAndTheProxyIsUsed(t *testing.T) {
	f := newFixture(t)
	const hub, other = "registry-1.docker.io", "registry.other.test"
	// Over plain HTTP a proxy is sent the whole request, so this one answers as the registries.
	var proxy seen
	proxyHost := start(t, proxy.record(basic(f.reg.Handler())))
	t.Setenv("HTTP_PROXY", "http://proxyuser:"+proxyPassword+"@"+proxyHost)
	f.push(t, hub, other)
	c := f.client(t, registry.Config{Insecure: true, AuthFile: authFile(t, auths(map[string]entry{
		"https://index.docker.io/v1/": withPassword(), // the key `docker login` writes
	}))})

	for _, repositoryHost := range []string{"docker.io", "index.docker.io", hub} {
		f.mustPull(t, c, repositoryHost)
	}
	f.mustFail(t, c, other, other, "401")
	for _, r := range proxy.all() {
		switch {
		case r.host == hub && r.authorization != "" && r.authorization != "Basic "+basicAuth:
			t.Errorf("Docker Hub was sent %q; want its credential", r.authorization)
		case r.host == other && r.authorization != "":
			t.Errorf("%s was sent %q; want no credential", other, r.authorization)
		case r.host != hub && r.host != other:
			t.Errorf("a request for %s; want only %s and %s", r.host, hub, other)
		}
	}

	hosts := f.logged(t, attrHosts)
	if len(hosts) != 1 || !slices.Equal(anyStrings(hosts[0][attrHosts]), []string{"docker.io"}) {
		t.Errorf("the hosts with a credential were logged as %v; want docker.io once", hosts)
	}
	warned := f.logged(t, attrInsecure)
	if len(warned) != 1 || warned[0][attrProxy] != proxyHost {
		t.Errorf("the insecure warning is %v; want one that names the proxy %s", warned, proxyHost)
	}
}

// anyStrings returns a decoded JSON array of strings.
func anyStrings(v any) []string {
	values, _ := v.([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.(string))
	}
	return out
}

// ADR 0021: "`credsStore` and `credHelpers` are ignored, with one warning at startup that names
// the key", and an entry with none of the three forms is no entry.
func TestCredentialHelpersAreIgnoredWithOneWarning(t *testing.T) {
	f := newFixture(t)
	var listed seen
	hostA := start(t, listed.record(basic(f.reg.Handler())))
	f.push(t, hostA)
	c := f.client(t, registry.Config{Insecure: true, AuthFile: authFile(t, map[string]any{
		"auths":       map[string]entry{hostA: withPassword(), "helper.example": {}},
		"credsStore":  "a-helper-that-is-not-run",
		"credHelpers": map[string]string{"cloud.example": "another-helper-that-is-not-run"},
	})})

	f.mustPull(t, c, hostA)
	warned := f.logged(t, attrHelpers)
	if len(warned) != 1 || warned[0]["level"] != "WARN" ||
		!slices.Equal(anyStrings(warned[0][attrHelpers]), []string{"credHelpers", "credsStore"}) {
		t.Errorf("the credential helper warnings are %v; want one that names credHelpers and credsStore", warned)
	}
	hosts := f.logged(t, attrHosts)
	if len(hosts) != 1 || !slices.Equal(anyStrings(hosts[0][attrHosts]), []string{hostA}) {
		t.Errorf("the hosts with a credential were logged as %v; want only %s", hosts, hostA)
	}
}

// ADR 0021: "The EN never runs a credential helper". The package cannot: it does not import the
// package that runs programs.
func TestPackageRunsNoProgram(t *testing.T) {
	notTests := func(info fs.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", notTests, parser.ImportsOnly)
	if err != nil || len(pkgs) == 0 {
		t.Fatalf("parse the package: %v, %d packages", err, len(pkgs))
	}
	for _, pkg := range pkgs {
		for file, parsed := range pkg.Files {
			for _, imported := range parsed.Imports {
				if path, _ := strconv.Unquote(imported.Path.Value); path == "os/exec" {
					t.Errorf("%s imports os/exec", file)
				}
			}
		}
	}
}

// ADR 0021: "A file that is set but missing, unreadable or not valid is a configuration error
// (exit code 2) that names the file and never its contents."
func TestBadAuthFileIsAConfigurationError(t *testing.T) {
	passwordOnly := base64.StdEncoding.EncodeToString([]byte(password)) // no `user:` before it
	cases := map[string]struct {
		path func(t *testing.T) string
		want string // also in the error
	}{
		"missing":     {path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "none.json") }},
		"a directory": {path: func(t *testing.T) string { return t.TempDir() }},
		"not JSON": {path: func(t *testing.T) string {
			return authFile(t, `{"auths":{"reg.example":{"auth":"`+basicAuth+`" "`+password+`"}}}`)
		}},
		"not an object":   {path: func(t *testing.T) string { return authFile(t, []string{password}) }},
		"auths not a map": {path: func(t *testing.T) string { return authFile(t, map[string]any{"auths": password}) }},
		"auth not a string": {path: func(t *testing.T) string {
			return authFile(t, map[string]any{"auths": map[string]any{"reg.example": map[string]any{"auth": []string{password}}}})
		}},
		"auth not base64": {want: "reg.example", path: func(t *testing.T) string {
			return authFile(t, auths(map[string]entry{"reg.example": {"auth": "%" + password}}))
		}},
		"auth without a user name": {want: "reg.example", path: func(t *testing.T) string {
			return authFile(t, auths(map[string]entry{"reg.example": {"auth": passwordOnly}}))
		}},
		"password without username": {want: "reg.example", path: func(t *testing.T) string {
			return authFile(t, auths(map[string]entry{"reg.example": {"password": password}}))
		}},
		"username without password": {want: "reg.example", path: func(t *testing.T) string {
			return authFile(t, auths(map[string]entry{"reg.example": {"username": user}}))
		}},
		"one registry twice": {want: "reg.example:5000", path: func(t *testing.T) string {
			return authFile(t, auths(map[string]entry{"https://reg.example:5000/v1/": withPassword(), "reg.example:5000": withPassword()}))
		}},
		"Docker Hub twice": {want: "docker.io", path: func(t *testing.T) string {
			return authFile(t, auths(map[string]entry{"https://index.docker.io/v1/": withPassword(), "registry-1.docker.io": withPassword()}))
		}},
		"a key with no host": {path: func(t *testing.T) string {
			return authFile(t, auths(map[string]entry{"https://": withPassword()}))
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			path := tc.path(t)
			c, err := f.new(registry.Config{AuthFile: path})
			var ce *registry.ConfigError
			if c != nil || !errors.Is(err, registry.ErrConfig) || !errors.As(err, &ce) {
				t.Fatalf("New = %v, %v; want no Client and a *ConfigError that wraps ErrConfig", c, err)
			}
			if ce.File != path || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, File %q; want the file %s and %q named", err, ce.File, path, tc.want)
			}
		})
	}
}

// ADR 0021: "No file set means every pull is anonymous."
func TestNoAuthFileMeansAnonymousPulls(t *testing.T) {
	f := newFixture(t)
	var open seen
	hostA := start(t, open.record(f.reg.Handler()))
	f.push(t, hostA)
	c := f.client(t, registry.Config{Insecure: true})

	f.mustPull(t, c, hostA)
	if got := open.authorizations(); len(got) != 0 {
		t.Errorf("the registry was sent %q; want no credential", got)
	}
	hosts := f.logged(t, attrHosts)
	if len(hosts) != 1 || len(anyStrings(hosts[0][attrHosts])) != 0 {
		t.Errorf("the hosts with a credential were logged as %v; want one line with none", hosts)
	}
}

// ADR 0021: "By default HTTPS, with the registry's certificate verified against the system roots",
// and there is no HTTPS without verification.
func TestHTTPSIsVerifiedByDefault(t *testing.T) {
	f := newFixture(t)
	var plain, secure seen
	hostP := start(t, plain.record(basic(f.reg.Handler())))
	hostS, roots := startTLS(t, secure.record(basic(f.reg.Handler())))
	f.push(t, hostP, hostS)
	cfg := registry.Config{AuthFile: authFile(t, auths(map[string]entry{hostP: withPassword(), hostS: withPassword()}))}
	c := f.client(t, cfg)

	f.mustFail(t, c, hostP, hostP)
	f.mustFail(t, c, hostS, hostS, "certificate")
	if n := len(plain.all()) + len(secure.all()); n != 0 {
		t.Errorf("%d requests reached a registry over plain HTTP or with a certificate that is not verified; want none", n)
	}
	if warned := f.logged(t, attrInsecure); len(warned) != 0 {
		t.Errorf("an insecure warning without en.registry.insecure: %v", warned)
	}

	f.mustPull(t, f.client(t, registry.WithRoots(cfg, roots)), hostS)
	if got := secure.authorizations(); !slices.Equal(got, []string{"Basic " + basicAuth}) {
		t.Errorf("the HTTPS registry was sent %q; want its credential", got)
	}
}

// ADR 0021: "Unless `en.registry.insecure` is true, the EN makes no plain-HTTP request at all: not
// to a registry, not to the token service a registry names, and not to the target of a redirect."
func TestNoPlainHTTPWithoutInsecure(t *testing.T) {
	f := newFixture(t)
	var tokens, storage seen
	hostT := start(t, tokens.record(tokenService()))
	hostS := start(t, storage.record(f.reg.Handler()))
	hostA, roots := startTLS(t, bearer("http://"+hostT+"/token", bearerToken, f.reg.Handler()))
	hostB, _ := startTLS(t, redirectLayers("http://"+hostS, f.reg.Handler()))
	f.push(t, hostA, hostB, hostS)
	c := f.client(t, registry.WithRoots(registry.Config{
		AuthFile: authFile(t, auths(map[string]entry{hostA: withPassword()})),
	}, roots))

	f.mustFail(t, c, hostA, hostA, hostT)
	f.mustFail(t, c, hostB, hostB, hostS)
	if n := len(tokens.all()) + len(storage.all()); n != 0 {
		t.Errorf("%d plain-HTTP requests were made without en.registry.insecure; want none", n)
	}
}

// ADR 0021: `en.registry.insecure` "logs a warning at startup, which also says so when credentials
// are configured, since they are then sent in the clear".
func TestInsecureWarnsAtStartup(t *testing.T) {
	for _, withFile := range []bool{false, true} {
		f := newFixture(t)
		cfg := registry.Config{Insecure: true}
		if withFile {
			cfg.AuthFile = authFile(t, auths(map[string]entry{"reg.example": withPassword()}))
		}
		f.client(t, cfg)
		warned := f.logged(t, attrInsecure)
		if len(warned) != 1 || warned[0]["level"] != "WARN" || warned[0][attrInsecure] != withFile {
			t.Errorf("with credentials %t: the insecure warnings are %v; want one that says %t", withFile, warned, withFile)
			continue
		}
		if _, named := warned[0][attrProxy]; named {
			t.Errorf("with credentials %t: the warning names a proxy and there is none: %v", withFile, warned[0])
		}
	}
}

// ADR 0019, 0021: the puller's limits apply whatever this package returns, and its errors keep
// what the puller and the executor look for in them.
func TestPullLimitsAndErrorsStay(t *testing.T) {
	f := newFixture(t)
	hostA := start(t, f.reg.Handler())
	f.push(t, hostA)
	c := f.client(t, registry.Config{Insecure: true})

	if err := f.pull(t, c, hostA, pull.Limits{MaxBytes: int64(len(f.archive)) - 1, Timeout: time.Minute}); err == nil {
		t.Error("a pull over en.pull.max_bytes succeeded")
	}

	f.reg.PushComposeArchive(t, hostA+"/acme/slow", revision, f.archive)
	f.reg.Stall(hostA + "/acme/slow")
	_, err := pull.New(c.Open, filepath.Join(t.TempDir(), "pull"), pull.Limits{MaxBytes: 1 << 20, Timeout: 50 * time.Millisecond}).
		ComposeArchive(context.Background(), "oci://"+hostA+"/acme/slow", revision)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a pull that outlasts en.pull.timeout: %v; want context.DeadlineExceeded", err)
	}

	_, err = pull.New(c.Open, filepath.Join(t.TempDir(), "pull"), roomy).
		ComposeArchive(context.Background(), "oci://"+hostA+"/"+name, "9.9.9")
	if !errors.Is(err, errdef.ErrNotFound) || !strings.Contains(err.Error(), hostA) {
		t.Errorf("a tag that does not exist: %v; want errdef.ErrNotFound and the host %s", err, hostA)
	}
}

// Open takes a registry host and a path, and nothing that could carry a tag or a credential.
func TestOpenRefusesWhatIsNotARepository(t *testing.T) {
	f := newFixture(t)
	c := f.client(t, registry.Config{})
	digest := "sha256:" + strings.Repeat("0", 64)
	for _, repository := range []string{
		"", "web", "reg.example", "reg.example/acme/web:1.2.3", "reg.example/acme/web@" + digest,
		user + ":" + password + "@reg.example/acme/web", "https://reg.example/acme/web", "reg.example/Acme/Web",
	} {
		target, err := c.Open(context.Background(), repository)
		if err == nil || target != nil {
			t.Errorf("Open(%q) = %v, %v; want an error", repository, target, err)
			continue
		}
		f.texts = append(f.texts, err.Error())
	}
	if target, err := c.Open(context.Background(), "reg.example:5000/acme/web"); err != nil || target == nil {
		t.Errorf("Open of a repository = %v, %v; want it", target, err)
	}
}
