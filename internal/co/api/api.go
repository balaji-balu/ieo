// Package api serves the Margo Workload Management API to LOs (SPEC §11.1). The caller's site
// comes only from its credential, never from the path or body, and every read is scoped to that
// site. Until mutual TLS lands (Appendix B step 4) the credential is a per-site bearer token
// (SPEC §15.6).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/contract"
)

// Authenticator maps a bearer token to its site (SPEC §15.6). An unknown token wraps
// auth.ErrUnknownToken.
type Authenticator interface {
	Verify(ctx context.Context, token string) (contract.SiteID, error)
}

// Store reads what the API serves.
type Store interface {
	// Site returns a site, and whether it exists.
	Site(ctx context.Context, id contract.SiteID) (deploy.Site, bool, error)
	// Manifest returns a site's published manifest, and whether it has one.
	Manifest(ctx context.Context, site contract.SiteID) (deploy.Manifest, bool, error)
	// DeploymentYAML returns the YAML of deployment id under digest, and whether that digest was
	// the deployment's in a manifest of site.
	DeploymentYAML(ctx context.Context, site contract.SiteID, id uuid.UUID, digest contract.Digest) ([]byte, bool, error)
	// Bundle returns the bundle under digest, and whether a manifest of site named it.
	Bundle(ctx context.Context, site contract.SiteID, digest contract.Digest) ([]byte, bool, error)
}

// Reporter takes what an LO reports (SPEC §8.1.2, §8.3). site is always the caller's site.
type Reporter interface {
	ReportCapabilities(ctx context.Context, site contract.SiteID, id contract.DeviceID, caps contract.DeviceCapabilitiesManifest) (created bool, err error)
	RemoveDevice(ctx context.Context, site contract.SiteID, id contract.DeviceID) error
	ReportStatus(ctx context.Context, site contract.SiteID, id uuid.UUID, status contract.DeploymentStatus) (created bool, err error)
}

const (
	cacheManifest  = "private"                              // SPEC §11.1
	cacheImmutable = "private, max-age=31536000, immutable" // SPEC §11.1
)

type server struct {
	auth     Authenticator
	store    Store
	reporter Reporter
	log      *slog.Logger
	mux      *http.ServeMux
}

type siteKey struct{}

// New returns the API handler. A nil log uses slog.Default.
func New(auth Authenticator, store Store, reporter Reporter, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	s := &server{auth: auth, store: store, reporter: reporter, log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/v1/deployments", s.manifest)
	s.mux.HandleFunc("GET /api/v1/deployments/{deploymentId}/{digest}", s.deployment)
	s.mux.HandleFunc("GET /api/v1/bundles/{digest}", s.bundle)
	s.mux.HandleFunc("PUT /api/v1/capabilities/{deviceId...}", s.putCapabilities)
	s.mux.HandleFunc("DELETE /api/v1/capabilities/{deviceId...}", s.deleteCapabilities)
	s.mux.HandleFunc("POST /api/v1/deployments/{deploymentId}/status", s.status)
	// GET would otherwise reach the deployment route with digest "status".
	s.mux.HandleFunc("GET /api/v1/deployments/{deploymentId}/status", methodNotAllowed("POST"))
	// Other methods on a known path.
	s.mux.HandleFunc("/api/v1/deployments", methodNotAllowed("GET, HEAD"))
	s.mux.HandleFunc("/api/v1/bundles/{digest}", methodNotAllowed("GET, HEAD"))
	s.mux.HandleFunc("/api/v1/capabilities/{deviceId...}", methodNotAllowed("PUT, DELETE"))
	s.mux.HandleFunc("/api/v1/deployments/{deploymentId}/{digest}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("digest") == "status" {
			methodNotAllowed("POST")(w, r)
			return
		}
		methodNotAllowed("GET, HEAD")(w, r)
	})
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, r, http.StatusNotFound, contract.ProblemAboutBlank, "", "no such resource")
	})
	return s
}

// ServeHTTP identifies the caller's site before routing, so an unauthenticated caller learns
// nothing about the API, and refuses a retired site (SPEC §11.1, §15.6).
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		token = ""
	}
	siteID, err := s.auth.Verify(r.Context(), strings.TrimSpace(token))
	var site deploy.Site
	ok := false
	if err == nil {
		site, ok, err = s.store.Site(r.Context(), siteID)
	}
	switch {
	case errors.Is(err, auth.ErrUnknownToken) || err == nil && !ok:
		w.Header().Set("WWW-Authenticate", "Bearer")
		problem(w, r, http.StatusUnauthorized, contract.ProblemAboutBlank, "", "missing or unknown bearer token")
		return
	case err != nil:
		s.internalError(w, r, "", err)
		return
	case site.Retired:
		problem(w, r, http.StatusForbidden, contract.ProblemNotAuthorized, "Client Relationship Retired",
			"The WFM Client relationship has been retired by local policy.")
		return
	}
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), siteKey{}, site.ID)))
}

func siteOf(r *http.Request) contract.SiteID {
	return r.Context().Value(siteKey{}).(contract.SiteID)
}

// manifest serves the caller's State Manifest (SPEC §4.1.6, §8.1.3).
func (s *server) manifest(w http.ResponseWriter, r *http.Request) {
	if !accepts(r.Header.Get("Accept"), contract.ManifestMediaType) {
		problem(w, r, http.StatusNotAcceptable, contract.ProblemCannotGenerate, "Server Cannot Generate Response",
			"the manifest is available only as "+contract.ManifestMediaType)
		return
	}
	site := siteOf(r)
	m, ok, err := s.store.Manifest(r.Context(), site)
	if err != nil {
		s.internalError(w, r, site, err)
		return
	}
	if !ok { // every site gets a manifest when it is added
		s.internalError(w, r, site, errors.New("site has no manifest"))
		return
	}
	serve(w, r, true, contract.ManifestMediaType, m.ETag, cacheManifest, m.Body)
}

// deployment serves one deployment YAML of the caller's site (SPEC §11.1).
func (s *server) deployment(w http.ResponseWriter, r *http.Request) {
	site := siteOf(r)
	id, idErr := uuid.Parse(r.PathValue("deploymentId"))
	if idErr == nil && id.String() != r.PathValue("deploymentId") {
		idErr = errors.New("deployment ID not in canonical form") // one URL per resource (SPEC §4.2)
	}
	digest, digestErr := contract.ParseDigest(r.PathValue("digest"))
	var b []byte
	ok := false
	var err error
	if idErr == nil && digestErr == nil {
		b, ok, err = s.store.DeploymentYAML(r.Context(), site, id, digest)
	}
	if err != nil {
		s.internalError(w, r, site, err)
		return
	}
	if !ok {
		problem(w, r, http.StatusNotFound, contract.ProblemDeploymentNotFound, "Deployment Not Found",
			"no deployment with this ID and digest")
		return
	}
	// The Margo file lists no 304 for this endpoint.
	serve(w, r, false, contract.DeploymentMediaType, quoted(digest), cacheImmutable, b)
}

// bundle serves one bundle of the caller's site (SPEC §11.1, ADR 0012).
func (s *server) bundle(w http.ResponseWriter, r *http.Request) {
	site := siteOf(r)
	digest, digestErr := contract.ParseDigest(r.PathValue("digest"))
	var b []byte
	ok := false
	var err error
	if digestErr == nil {
		b, ok, err = s.store.Bundle(r.Context(), site, digest)
	}
	if err != nil {
		s.internalError(w, r, site, err)
		return
	}
	if !ok {
		problem(w, r, http.StatusNotFound, contract.ProblemInvalidBundle, "Invalid Bundle",
			"no bundle with this digest")
		return
	}
	serve(w, r, true, contract.BundleMediaType, quoted(digest), cacheImmutable, b)
}

// quoted is the entity tag of content stored under digest: the digest of its exact bytes, so it
// needs no hashing per request.
func quoted(digest contract.Digest) string { return `"` + string(digest) + `"` }

// serve writes body, or 304 when conditional and If-None-Match matches etag.
func serve(w http.ResponseWriter, r *http.Request, conditional bool, mediaType, etag, cache string, body []byte) {
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", cache)
	if conditional && noneMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", mediaType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// noneMatch reports whether an If-None-Match header matches etag, by weak comparison (RFC 9110
// §13.1.2).
func noneMatch(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}

// accepts reports whether an Accept header admits mediaType: the most specific range that
// matches it must have a non-zero quality (RFC 9110 §12.5.1). No header admits everything.
func accepts(header, mediaType string) bool {
	if strings.TrimSpace(header) == "" {
		return true
	}
	typ, _, _ := strings.Cut(mediaType, "/")
	best, admitted := 0, false
	for _, r := range strings.Split(header, ",") {
		rng, params, err := mime.ParseMediaType(strings.TrimSpace(r))
		if err != nil {
			continue
		}
		specificity := 0
		switch rng {
		case mediaType:
			specificity = 3
		case typ + "/*":
			specificity = 2
		case "*/*":
			specificity = 1
		}
		if specificity <= best {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if q, err = strconv.ParseFloat(v, 64); err != nil {
				continue
			}
		}
		best, admitted = specificity, q > 0
	}
	return admitted
}

// internalError logs err and answers 500 without detail, so internals don't reach the caller.
// The log carries the path and site, never request headers (SPEC §15.4).
func (s *server) internalError(w http.ResponseWriter, r *http.Request, site contract.SiteID, err error) {
	s.log.ErrorContext(r.Context(), "margo api request failed",
		"method", r.Method, "path", r.URL.Path, "site_id", site, "error", err)
	problem(w, r, http.StatusInternalServerError, contract.ProblemAboutBlank, "", "")
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		problem(w, r, http.StatusMethodNotAllowed, contract.ProblemAboutBlank, "", "")
	}
}

// problem writes an RFC 9457 problem (SPEC §11.1). An empty title is the status text, as
// about:blank requires.
func problem(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string) {
	if title == "" {
		title = http.StatusText(status)
	}
	writeProblem(w, contract.Problem{Type: typ, Title: title, Status: status, Detail: detail, Instance: r.URL.Path})
}

// writeProblem writes p as an RFC 9457 problem (SPEC §11.1).
func writeProblem(w http.ResponseWriter, p contract.Problem) {
	status := p.Status
	b, err := json.Marshal(p)
	if err != nil { // cannot happen: every field is a string or int
		b = []byte(`{"type":"about:blank","title":"Internal Server Error","status":500}`)
	}
	w.Header().Set("Content-Type", contract.ProblemMediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
