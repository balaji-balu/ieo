package ociremote_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/balaji-balu/ieo/internal/ociremote"
	"github.com/balaji-balu/ieo/internal/ocitest"
)

// The transport cases of ADR 0021, through this package's own API. The EN's use of it, with its
// auth file and its puller, is tested in package registry.

// Made up for these tests (SPEC §15.6).
const (
	user       = "puller"
	password   = "made-up-registry-password"
	signature  = "made-up-signature"
	serverText = "made-up-server-text"
	key        = "test.registry.insecure"

	name     = "acme/web"
	revision = "1.2.3"
)

func newRegistry(t *testing.T) *ocitest.Registry {
	t.Helper()
	// A test names its own proxy; the one of the machine it runs on is not part of it.
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	return ocitest.NewRegistry()
}

// seen records the requests a server is sent.
type seen struct {
	mu             sync.Mutex
	authorizations []string
	n              int
}

func (s *seen) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.n++
		if a := r.Header.Get("Authorization"); a != "" {
			s.authorizations = append(s.authorizations, a)
		}
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (s *seen) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *seen) credentials() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.authorizations)
}

func start(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func startTLS(t *testing.T, h http.Handler) (string, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	return strings.TrimPrefix(srv.URL, "https://"), roots
}

// basic serves next to user and password, and asks anyone else for them with an error whose
// message is serverText.
func basic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == user && p == password {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"code":"UNAUTHORIZED","message":"`+serverText+`"}]}`)
	})
}

// redirectLayers sends each blob request to the same path at to, with a signature in the query.
func redirectLayers(to string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/blobs/") {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, to+r.URL.Path+"?sig="+signature, http.StatusTemporaryRedirect)
	})
}

// withPassword is a Credential that gives user and password to every host it is asked for.
func withPassword(context.Context, string) (auth.Credential, error) {
	return auth.Credential{Username: user, Password: password}, nil
}

// read reads the archive tagged revision from the repository at host, as a puller does.
func read(c *ociremote.Client, host string) error {
	repo, err := c.Open(context.Background(), host+"/"+name)
	if err != nil {
		return err
	}
	_, manifest, err := oras.FetchBytes(context.Background(), repo, revision, oras.DefaultFetchBytesOptions)
	if err != nil {
		return err
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil || len(m.Layers) != 1 {
		return errors.New("not the manifest pushed")
	}
	_, err = content.FetchAll(context.Background(), repo, m.Layers[0]) // over a redirect, if the registry sends one
	return err
}

// noSecret fails the test if text holds a URL, a credential or something a server sent.
func noSecret(t *testing.T, text string) {
	t.Helper()
	for _, s := range []string{"http://", "https://", "/v2/", password, signature, serverText} {
		if strings.Contains(text, s) {
			t.Errorf("%q holds %q", text, s)
		}
	}
}

// ADR 0021: with no Credential, no host is sent one.
func TestNoCredentialMeansAnonymous(t *testing.T) {
	reg := newRegistry(t)
	var open, closed seen
	hostO := start(t, open.record(reg.Handler()))
	hostC := start(t, closed.record(basic(reg.Handler())))
	reg.PushComposeArchive(t, hostO+"/"+name, revision, []byte("archive"))
	reg.PushComposeArchive(t, hostC+"/"+name, revision, []byte("archive"))
	c := ociremote.New(ociremote.Options{Insecure: true, InsecureKey: key, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(c.Close)

	if err := read(c, hostO); err != nil {
		t.Fatalf("read from an open registry: %v", err)
	}
	err := read(c, hostC)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), hostC) {
		t.Errorf("read from a registry that asks for a credential: %v; want a 401 that names %s", err, hostC)
	} else {
		noSecret(t, err.Error())
	}
	if got := append(open.credentials(), closed.credentials()...); len(got) != 0 {
		t.Errorf("the registries were sent %q; want no Authorization", got)
	}
}

// ADR 0021: a credential is not sent to the target of a redirect.
func TestCredentialIsNotSentToARedirectTarget(t *testing.T) {
	reg := newRegistry(t)
	var registry, storage seen
	hostS := start(t, storage.record(reg.Handler()))
	hostA := start(t, registry.record(basic(redirectLayers("http://"+hostS, reg.Handler()))))
	reg.PushComposeArchive(t, hostA+"/"+name, revision, []byte("archive"))
	reg.PushComposeArchive(t, hostS+"/"+name, revision, []byte("archive"))
	c := ociremote.New(ociremote.Options{Insecure: true, InsecureKey: key, Credential: withPassword, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(c.Close)

	if err := read(c, hostA); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(registry.credentials()) == 0 {
		t.Error("the registry was sent no credential; want its own")
	}
	if got := storage.credentials(); len(got) != 0 {
		t.Errorf("the redirect target was sent %q; want nothing", got)
	}
}

// ADR 0021: without Insecure no plain-HTTP request is made, to a registry or to the target of a
// redirect, and the error names the key that would allow it.
func TestNoPlainHTTPWithoutInsecure(t *testing.T) {
	reg := newRegistry(t)
	var plain, storage seen
	hostP := start(t, plain.record(reg.Handler()))
	hostS := start(t, storage.record(reg.Handler()))
	hostR, roots := startTLS(t, redirectLayers("http://"+hostS, reg.Handler()))
	for _, h := range []string{hostP, hostS, hostR} {
		reg.PushComposeArchive(t, h+"/"+name, revision, []byte("archive"))
	}
	c := ociremote.New(ociremote.Options{InsecureKey: key, Roots: roots})
	t.Cleanup(c.Close)

	// A registry is asked over HTTPS, which a plain-HTTP one cannot answer.
	if err := read(c, hostP); err == nil || !strings.Contains(err.Error(), hostP) {
		t.Errorf("read from a plain-HTTP registry: %v; want an error that names %s", err, hostP)
	} else {
		noSecret(t, err.Error())
	}
	// A plain-HTTP redirect target is not asked at all.
	if err := read(c, hostR); err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), hostS) {
		t.Errorf("read over a plain-HTTP redirect: %v; want an error that names %s and %s", err, hostS, key)
	} else {
		noSecret(t, err.Error())
	}
	if n := plain.count() + storage.count(); n != 0 {
		t.Errorf("%d plain-HTTP requests were made without Insecure; want none", n)
	}
}

// ADR 0021: an error holds no URL and no text a server sent, and errdef.ErrNotFound is found in
// it.
func TestErrorsHoldNoURLAndNoServerText(t *testing.T) {
	reg := newRegistry(t)
	host := start(t, reg.Handler())
	reg.PushComposeArchive(t, host+"/"+name, revision, []byte("archive"))
	c := ociremote.New(ociremote.Options{Insecure: true, InsecureKey: key, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(c.Close)

	repo, err := c.Open(context.Background(), host+"/"+name)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Resolve(context.Background(), "9.9.9")
	if !errors.Is(err, errdef.ErrNotFound) || !strings.Contains(err.Error(), host) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("resolve a tag that does not exist: %v; want errdef.ErrNotFound, %s and 404", err, host)
	}
	noSecret(t, err.Error())
}

// ADR 0021: Insecure logs one warning at startup that names its key, and says whether credentials
// are sent in the clear.
func TestInsecureWarnsAtStartup(t *testing.T) {
	newRegistry(t) // clears the proxy settings
	for _, credential := range []auth.CredentialFunc{nil, withPassword} {
		var logs bytes.Buffer
		ociremote.New(ociremote.Options{Insecure: true, InsecureKey: key, Credential: credential,
			Log: slog.New(slog.NewJSONHandler(&logs, nil))}).Close()
		var line map[string]any
		if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
			t.Fatalf("want one JSON log line, got %q: %v", logs.String(), err)
		}
		inClear := credential != nil
		if line["level"] != "WARN" || !strings.Contains(line["msg"].(string), key) || line["credentials_in_clear"] != inClear {
			t.Errorf("with credentials %t: warning %v; want one that names %s and says %t", inClear, line, key, inClear)
		}
	}
	var logs bytes.Buffer
	ociremote.New(ociremote.Options{InsecureKey: key, Log: slog.New(slog.NewJSONHandler(&logs, nil))}).Close()
	if logs.Len() != 0 {
		t.Errorf("without Insecure: logged %q; want nothing", logs.String())
	}
}

// Open takes a registry host and a path, and nothing that could carry a tag or a credential.
func TestOpenRefusesWhatIsNotARepository(t *testing.T) {
	c := ociremote.New(ociremote.Options{InsecureKey: key})
	t.Cleanup(c.Close)
	for _, repository := range []string{
		"", "web", "reg.example", "reg.example/acme/web:1.2.3",
		user + ":" + password + "@reg.example/acme/web", "https://reg.example/acme/web",
	} {
		if target, err := c.Open(context.Background(), repository); err == nil || target != nil {
			t.Errorf("Open(%q) = %v, %v; want an error", repository, target, err)
		} else {
			noSecret(t, err.Error())
		}
	}
}

// ADR 0021: Docker Hub's names are one registry.
func TestRegistryOf(t *testing.T) {
	for host, want := range map[string]string{
		"docker.io": "docker.io", "index.docker.io": "docker.io", "registry-1.docker.io": "docker.io",
		"reg.example:5000": "reg.example:5000", "docker.io:443": "docker.io:443",
	} {
		if got := ociremote.RegistryOf(host); got != want {
			t.Errorf("RegistryOf(%q) = %q; want %q", host, got, want)
		}
	}
}
