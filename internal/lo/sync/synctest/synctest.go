// Package synctest provides a fake CO for tests of the LO sync loop (SPEC §8.2, §11.1, §17.3).
package synctest

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// URL is the CO URL a CO answers when it is used as an http.RoundTripper.
const URL = "https://co.test"

// CO is the CO side of the Margo API for one site, built from the contract encoders. It is safe
// for concurrent use. Content it once served stays served, as the CO keeps earlier digests (SPEC
// §11.1).
//
// A CO is an http.RoundTripper for URL, so requests never touch a socket (G-F4), and an
// http.Handler, to serve over a real connection with httptest. Either way it records every
// request it receives.
type CO struct {
	t     testing.TB
	token string

	mu        sync.Mutex
	current   contract.StateManifest
	manifest  []byte // body of GET /api/v1/deployments
	content   map[string][]byte
	overrides map[string]http.HandlerFunc
	down      bool
	requests  []*http.Request
}

// NewCO returns a CO for a site whose manifest is not published yet, with a token generated for
// the test: no token in fixtures (SPEC §15.6).
func NewCO(t testing.TB) *CO {
	t.Helper()
	return &CO{t: t, token: rand.Text(), content: map[string][]byte{}, overrides: map[string]http.HandlerFunc{}}
}

// Token is the site token the CO accepts.
func (c *CO) Token() string { return c.token }

// ManifestBody is the body the CO serves for the manifest.
func (c *CO) ManifestBody() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.manifest
}

// YAMLOf is the YAML for deployment id at revision rev. The sync tick treats YAML as opaque
// bytes, so any distinct bytes do.
func YAMLOf(id uuid.UUID, rev string) []byte {
	return []byte("id: " + id.String() + "\nrevision: " + rev + "\n")
}

// Publish makes the deployments the site's manifest at version v, serving each YAML and the
// bundle, and returns the manifest.
func (c *CO) Publish(v contract.ManifestVersion, yamls map[uuid.UUID][]byte) contract.StateManifest {
	c.t.Helper()
	m := contract.StateManifest{ManifestVersion: v}
	var files []contract.BundleFile
	for id, y := range yamls {
		d := contract.DigestOf(y)
		m.Deployments = append(m.Deployments, contract.DeploymentRef{
			DeploymentID: id, Digest: d, SizeBytes: uint64(len(y)), URL: contract.DeploymentURL(id, d),
		})
		files = append(files, contract.BundleFile{DeploymentID: id, YAML: y})
		c.serve(contract.DeploymentURL(id, d), y)
	}
	if len(files) > 0 {
		b, err := contract.EncodeBundle(files)
		if err != nil {
			c.t.Fatalf("encode bundle: %v", err)
		}
		m.Bundle = c.bundleRef(b)
	}
	c.setManifest(m)
	return m
}

// ReplaceBundle makes b the current manifest's bundle, whatever it holds, keeping the version.
func (c *CO) ReplaceBundle(b []byte) {
	c.t.Helper()
	c.mu.Lock()
	m := c.current
	c.mu.Unlock()
	m.Bundle = c.bundleRef(b)
	c.setManifest(m)
}

func (c *CO) bundleRef(b []byte) *contract.BundleRef {
	d := contract.DigestOf(b)
	c.serve(contract.BundleURL(d), b)
	return &contract.BundleRef{MediaType: contract.BundleMediaType, Digest: d, SizeBytes: uint64(len(b)), URL: contract.BundleURL(d)}
}

// setManifest serves m, encoded as the CO does, as the manifest.
func (c *CO) setManifest(m contract.StateManifest) {
	c.t.Helper()
	body, err := contract.EncodeStateManifest(m)
	if err != nil {
		c.t.Fatalf("encode manifest: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current, c.manifest = m, body
}

// SetManifestBody serves body, which need not be a valid manifest, as the manifest.
func (c *CO) SetManifestBody(body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.manifest = body
}

func (c *CO) serve(path string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.content[path] = body
}

// Override answers path with h instead of the normal response; a nil h removes the override.
func (c *CO) Override(path string, h http.HandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h == nil {
		delete(c.overrides, path)
		return
	}
	c.overrides[path] = h
}

// SetDown makes every request through RoundTrip fail with a transport error while down is true.
func (c *CO) SetDown(down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down = down
}

// TakeRequests returns the requests received since the last call, and forgets them.
func (c *CO) TakeRequests() []*http.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.requests
	c.requests = nil
	return r
}

func (c *CO) record(req *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req.Clone(req.Context()))
}

// RoundTrip serves req when it is for URL and the CO is up, and fails otherwise.
func (c *CO) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	down := c.down
	c.mu.Unlock()
	switch {
	case down:
		c.record(req)
		return nil, errors.New("fake CO: connection refused")
	case req.URL.Scheme+"://"+req.URL.Host != URL:
		c.record(req)
		return nil, errors.New("fake CO: no route to " + req.URL.Host)
	}
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, req)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

// ServeHTTP answers the Margo API requests of SPEC §11.1 for the site, as the CO does.
func (c *CO) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.record(r)
	c.mu.Lock()
	h := c.overrides[r.URL.Path]
	manifest := c.manifest
	body, ok := c.content[r.URL.Path]
	c.mu.Unlock()
	switch {
	case h != nil:
		h(w, r)
	case r.Header.Get("Authorization") != "Bearer "+c.token:
		w.Header().Set("WWW-Authenticate", "Bearer")
		WriteProblem(w, http.StatusUnauthorized, contract.ProblemAboutBlank)
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
		WriteProblem(w, http.StatusNotFound, contract.ProblemDeploymentNotFound)
	}
}

// WriteProblem answers with status and a problem of type typ (RFC 9457).
func WriteProblem(w http.ResponseWriter, status int, typ string) {
	w.Header().Set("Content-Type", contract.ProblemMediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(contract.Problem{Type: typ, Title: http.StatusText(status), Status: status})
}

// Respond returns a handler that answers with status, the headers given as name, value pairs, and
// body.
func Respond(status int, body []byte, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}
