package catalog_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"oras.land/oras-go/v2/errdef"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/store"
	"github.com/balaji-balu/ieo/internal/ociremote"
	"github.com/balaji-balu/ieo/internal/ocitest"
)

// Made up for these tests (SPEC §15.6): what a registry puts in the URL it redirects a layer to,
// and in the message of an error.
const (
	signature  = "made-up-signature"
	serverText = "made-up-server-text"
	appPath    = "/example/hello"
)

// requests records what a server is sent.
type requests struct {
	mu             sync.Mutex
	n              int
	authorizations []string
}

func (r *requests) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.n++
		if a := req.Header.Get("Authorization"); a != "" {
			r.authorizations = append(r.authorizations, a)
		}
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

func (r *requests) count() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n, append([]string(nil), r.authorizations...)
}

// registryServer serves h on 127.0.0.1 (G-F4), over HTTPS when secure, and returns its
// `host:port`. A secure server's certificate is verified with roots.
func registryServer(t *testing.T, h http.Handler, secure bool) (host string, srv *httptest.Server) {
	t.Helper()
	if secure {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://"), srv
}

// redirectBlobs sends each blob request to the same path at to, with a signature in the query, as
// a registry that keeps its layers on a storage host does.
func redirectBlobs(to string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/blobs/") {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, to+r.URL.Path+"?sig="+signature, http.StatusTemporaryRedirect)
	})
}

// challenge asks every request for a Basic credential, with serverText in the error.
func challenge(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"errors":[{"code":"UNAUTHORIZED","message":"`+serverText+`"}]}`)
}

// importFrom imports version from host's repository through a CO registry client.
func importFrom(t *testing.T, reg *ociremote.Client, host, version string) error {
	t.Helper()
	_, err := catalog.New(reg.Open, store.NewMemory()).Import(context.Background(), host+appPath, version)
	return err
}

// mustBeClean fails the test unless err is an ErrRegistry whose text names host and holds no URL
// and nothing a server sent (SPEC §5.3, §15.4).
func mustBeClean(t *testing.T, what string, err error, host string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: imported; want an error", what)
		return
	}
	if !errors.Is(err, catalog.ErrRegistry) {
		t.Errorf("%s: %v; want an ErrRegistry", what, err)
	}
	text := err.Error()
	if !strings.Contains(text, host) {
		t.Errorf("%s: %q; want the host %s named", what, text, host)
	}
	for _, s := range []string{"http://", "https://", "/v2/", "?", signature, serverText} {
		if strings.Contains(text, s) {
			t.Errorf("%s: %q holds %q", what, text, s)
		}
	}
}

// SPEC §17.2: "Import sends no credential to a registry, and uses plain HTTP only when
// `co.registry.insecure` is true; an import error holds no URL and no registry text (§5.3)."
func TestSpec_17_2_ImportSendsNoCredentialAndPlainHTTPOnlyWhenInsecure(t *testing.T) {
	// A test names its own proxy; the one of the machine it runs on is not part of it.
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	quiet := slog.New(slog.DiscardHandler)
	oci := ocitest.NewRegistry()
	push := func(hosts ...string) {
		for _, host := range hosts {
			oci.PushApp(t, host+appPath, version, []byte(description))
		}
	}

	t.Run("no credential", func(t *testing.T) {
		var open, closed requests
		hostO, _ := registryServer(t, open.record(oci.Handler()), false)
		hostC, _ := registryServer(t, closed.record(http.HandlerFunc(challenge)), false)
		push(hostO)
		reg := catalog.NewRegistry(true, quiet)
		t.Cleanup(reg.Close)

		if err := importFrom(t, reg, hostO, version); err != nil {
			t.Fatalf("import from a registry that asks for no credential: %v", err)
		}
		mustBeClean(t, "import from a registry that asks for a credential", importFrom(t, reg, hostC, version), hostC)
		for name, r := range map[string]*requests{"open": &open, "challenging": &closed} {
			if n, sent := r.count(); n == 0 || len(sent) != 0 {
				t.Errorf("the %s registry got %d requests with Authorization %q; want requests and none", name, n, sent)
			}
		}
	})

	t.Run("plain HTTP only when insecure", func(t *testing.T) {
		var plain, storage requests
		hostP, _ := registryServer(t, plain.record(oci.Handler()), false)
		hostS, _ := registryServer(t, storage.record(oci.Handler()), false)
		hostR, srvR := registryServer(t, redirectBlobs("http://"+hostS, oci.Handler()), true)
		hostQ, _ := registryServer(t, redirectBlobs("http://"+hostS, oci.Handler()), false)
		push(hostP, hostS, hostR, hostQ)

		secure := catalog.NewRegistryWithRoots(false, srvR.Certificate(), quiet)
		t.Cleanup(secure.Close)
		mustBeClean(t, "import from an http:// registry without insecure", importFrom(t, secure, hostP, version), hostP)
		mustBeClean(t, "import over an http:// redirect without insecure", importFrom(t, secure, hostR, version), hostS)
		if n, _ := plain.count(); n != 0 {
			t.Errorf("the http:// registry got %d requests without insecure; want none", n)
		}
		if n, _ := storage.count(); n != 0 {
			t.Errorf("the http:// redirect target got %d requests without insecure; want none", n)
		}

		insecure := catalog.NewRegistry(true, quiet)
		t.Cleanup(insecure.Close)
		if err := importFrom(t, insecure, hostP, version); err != nil {
			t.Errorf("import from an http:// registry with insecure: %v", err)
		}
		if err := importFrom(t, insecure, hostQ, version); err != nil {
			t.Errorf("import over an http:// redirect with insecure: %v", err)
		}
		if n, _ := storage.count(); n == 0 {
			t.Error("the http:// redirect target got no request with insecure; want the layers read from it")
		}
	})

	t.Run("error holds no URL and no registry text", func(t *testing.T) {
		hostA, _ := registryServer(t, oci.Handler(), false)
		push(hostA)
		reg := catalog.NewRegistry(true, quiet)
		t.Cleanup(reg.Close)

		err := importFrom(t, reg, hostA, "9.9.9")
		mustBeClean(t, "import of a tag the registry does not have", err, hostA)
		if !errors.Is(err, errdef.ErrNotFound) {
			t.Errorf("import of a tag the registry does not have: %v; want errdef.ErrNotFound in it", err)
		}
	})
}
