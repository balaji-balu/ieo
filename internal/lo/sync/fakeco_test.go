package sync_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

const coURL = "https://co.test"

// fakeCO is the CO side of the Margo API for one site, built from the contract encoders. It is an
// http.RoundTripper, so requests never touch a socket (G-F4). Content it once served stays served,
// as the CO keeps earlier digests (SPEC §11.1).
type fakeCO struct {
	t     *testing.T
	token string

	mu        sync.Mutex
	current   contract.StateManifest
	manifest  []byte // body of GET /api/v1/deployments
	content   map[string][]byte
	overrides map[string]http.HandlerFunc
	down      bool
	requests  []*http.Request
}

func newFakeCO(t *testing.T) *fakeCO {
	t.Helper()
	return &fakeCO{
		t: t, token: rand.Text(), // generated per test: no token in fixtures (SPEC §15.6)
		content: map[string][]byte{}, overrides: map[string]http.HandlerFunc{},
	}
}

// yamlOf is the YAML the fake CO serves for deployment id at revision rev. The sync tick treats
// YAML as opaque bytes, so any distinct bytes do.
func yamlOf(id uuid.UUID, rev string) []byte {
	return []byte("id: " + id.String() + "\nrevision: " + rev + "\n")
}

// publish makes the deployments the site's manifest at version v, serving each YAML and the
// bundle, and returns the manifest.
func (f *fakeCO) publish(v contract.ManifestVersion, yamls map[uuid.UUID][]byte) contract.StateManifest {
	f.t.Helper()
	m := contract.StateManifest{ManifestVersion: v}
	var files []contract.BundleFile
	for id, y := range yamls {
		d := contract.DigestOf(y)
		m.Deployments = append(m.Deployments, contract.DeploymentRef{
			DeploymentID: id, Digest: d, SizeBytes: uint64(len(y)), URL: contract.DeploymentURL(id, d),
		})
		files = append(files, contract.BundleFile{DeploymentID: id, YAML: y})
		f.serve(contract.DeploymentURL(id, d), y)
	}
	if len(files) > 0 {
		b, err := contract.EncodeBundle(files)
		if err != nil {
			f.t.Fatalf("encode bundle: %v", err)
		}
		m.Bundle = f.bundleRef(b)
	}
	f.setManifest(m)
	return m
}

// replaceBundle makes b the current manifest's bundle, whatever it holds, keeping the version.
func (f *fakeCO) replaceBundle(b []byte) {
	f.t.Helper()
	f.mu.Lock()
	m := f.current
	f.mu.Unlock()
	m.Bundle = f.bundleRef(b)
	f.setManifest(m)
}

func (f *fakeCO) bundleRef(b []byte) *contract.BundleRef {
	d := contract.DigestOf(b)
	f.serve(contract.BundleURL(d), b)
	return &contract.BundleRef{MediaType: contract.BundleMediaType, Digest: d, SizeBytes: uint64(len(b)), URL: contract.BundleURL(d)}
}

// setManifest serves m, encoded as the CO does, as the manifest.
func (f *fakeCO) setManifest(m contract.StateManifest) {
	f.t.Helper()
	body, err := contract.EncodeStateManifest(m)
	if err != nil {
		f.t.Fatalf("encode manifest: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current, f.manifest = m, body
}

// setManifestBody serves body, which need not be a valid manifest, as the manifest.
func (f *fakeCO) setManifestBody(body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifest = body
}

func (f *fakeCO) serve(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content[path] = body
}

// override answers path with h instead of the normal response; a nil h removes the override.
func (f *fakeCO) override(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h == nil {
		delete(f.overrides, path)
		return
	}
	f.overrides[path] = h
}

// setDown makes every request fail with a transport error while down is true.
func (f *fakeCO) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// takeRequests returns the requests received since the last call, and forgets them.
func (f *fakeCO) takeRequests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.requests
	f.requests = nil
	return r
}

func (f *fakeCO) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req.Clone(req.Context()))
	down := f.down
	f.mu.Unlock()
	if down {
		return nil, errors.New("fake CO: connection refused")
	}
	if req.URL.Scheme+"://"+req.URL.Host != coURL {
		return nil, errors.New("fake CO: no route to " + req.URL.Host)
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, req)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func (f *fakeCO) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	h := f.overrides[r.URL.Path]
	manifest := f.manifest
	body, ok := f.content[r.URL.Path]
	f.mu.Unlock()
	switch {
	case h != nil:
		h(w, r)
	case r.Header.Get("Authorization") != "Bearer "+f.token:
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(w, http.StatusUnauthorized, contract.ProblemAboutBlank)
	case r.URL.Path == "/api/v1/deployments":
		etag := contract.ETag(manifest)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", contract.ManifestMediaType)
		w.Header().Set("ETag", etag)
		_, _ = w.Write(manifest)
	case ok:
		_, _ = w.Write(body)
	default:
		writeProblem(w, http.StatusNotFound, contract.ProblemDeploymentNotFound)
	}
}

func writeProblem(w http.ResponseWriter, status int, typ string) {
	w.Header().Set("Content-Type", contract.ProblemMediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(contract.Problem{Type: typ, Title: http.StatusText(status), Status: status})
}

// respond returns a handler that answers with status, the headers given as name, value pairs, and
// body.
func respond(status int, body []byte, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

// tarEntry is one entry of a hand-built bundle.
type tarEntry struct {
	name     string
	typeflag byte
	body     []byte
}

// tarGz builds a gzip tar of entries, which need not follow ADR 0012.
func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o644, Size: int64(len(e.body))}
		if e.typeflag != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar %s: %v", e.name, err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatalf("tar %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// paths returns the URL paths of reqs.
func paths(reqs []*http.Request) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.URL.Path
	}
	return out
}

// countPrefix counts the paths that start with prefix.
func countPrefix(ps []string, prefix string) int {
	n := 0
	for _, p := range ps {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}
