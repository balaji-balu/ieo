// Package operator serves the CO's operator API to edgectl (SPEC §11.3). Every request needs the
// static operator token (SPEC §15.6); the CO holds only its SHA-256.
package operator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"oras.land/oras-go/v2/errdef"

	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/httpapi"
	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/operatorapi"
)

// Importer imports an application version and reports whether it stored a new one (SPEC §5.3);
// catalog.Catalog is one.
type Importer interface {
	Import(ctx context.Context, repository, version string) (catalog.App, bool, error)
}

// Creator creates a deployment (SPEC §8.1); deploy.Service is one.
type Creator interface {
	Create(ctx context.Context, req deploy.Request) (deploy.Deployment, error)
}

// Reader reads what the operator API lists and gets. Lists come in any order.
type Reader interface {
	// Apps returns every imported application version. Description may be left out.
	Apps(ctx context.Context) ([]catalog.App, error)
	// AppVersions returns the imported versions of application id. Description may be left out.
	AppVersions(ctx context.Context, id string) ([]catalog.App, error)
	// Site returns a site, and whether it exists.
	Site(ctx context.Context, id contract.SiteID) (deploy.Site, bool, error)
	// Devices returns the devices of site, or of every site when site is "".
	Devices(ctx context.Context, site contract.SiteID) ([]deploy.Device, error)
	// Deployments returns the deployments of site, or of every site when site is "", deleted
	// ones included.
	Deployments(ctx context.Context, site contract.SiteID) ([]deploy.Deployment, error)
	// Deployment returns a deployment, and whether it exists.
	Deployment(ctx context.Context, id uuid.UUID) (deploy.Deployment, bool, error)
	// CurrentStatus returns the current status of a deployment, and whether it has one.
	CurrentStatus(ctx context.Context, id uuid.UUID) (contract.DeploymentStatus, bool, error)
	// StatusHistory returns every status reported for a deployment, oldest first.
	StatusHistory(ctx context.Context, id uuid.UUID) ([]contract.DeploymentStatus, error)
}

// DefaultImportTimeout bounds an import, so that a registry that stalls does not hold the request
// open: the import then fails as an unreachable registry does (SPEC §11.3: `502` after 2 minutes).
const DefaultImportTimeout = 2 * time.Minute

// maxBody bounds a request body (SPEC §11.3): an import or create request is a few hundred bytes.
const maxBody = 1 << 20

// Config is what the operator API needs.
type Config struct {
	// Token is the SHA-256 of co.operator_token (SPEC §15.6).
	Token   auth.TokenHash
	Catalog Importer
	Deploy  Creator
	Store   Reader
	// ImportTimeout bounds an import; 0 is DefaultImportTimeout.
	ImportTimeout time.Duration
	// Log is where failed requests are logged; nil is slog.Default.
	Log *slog.Logger
}

type server struct {
	Config
	mux *http.ServeMux
}

// New returns the operator API handler.
func New(cfg Config) http.Handler {
	if cfg.ImportTimeout == 0 {
		cfg.ImportTimeout = DefaultImportTimeout
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	s := &server{Config: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /api/v1/apps", s.importApp)
	s.mux.HandleFunc("GET /api/v1/apps", s.listApps)
	s.mux.HandleFunc("GET /api/v1/apps/{appId}", s.getApp)
	s.mux.HandleFunc("GET /api/v1/devices", s.listDevices)
	s.mux.HandleFunc("POST /api/v1/deployments", s.create)
	s.mux.HandleFunc("GET /api/v1/deployments", s.listDeployments)
	s.mux.HandleFunc("GET /api/v1/deployments/{id}", s.getDeployment)
	s.mux.HandleFunc("GET /api/v1/deployments/{id}/status", s.status)
	// Other methods on a known path (SPEC §11.3: 405).
	s.mux.HandleFunc("/api/v1/apps", methodNotAllowed("GET, HEAD, POST"))
	s.mux.HandleFunc("/api/v1/apps/{appId}", methodNotAllowed("GET, HEAD"))
	s.mux.HandleFunc("/api/v1/devices", methodNotAllowed("GET, HEAD"))
	s.mux.HandleFunc("/api/v1/deployments", methodNotAllowed("GET, HEAD, POST"))
	s.mux.HandleFunc("/api/v1/deployments/{id}", methodNotAllowed("GET, HEAD"))
	s.mux.HandleFunc("/api/v1/deployments/{id}/status", methodNotAllowed("GET, HEAD"))
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, r, http.StatusNotFound, "no such resource")
	})
	return s
}

// ServeHTTP checks the operator token before anything else, so a caller without it learns nothing
// about the API and has nothing of its request read (SPEC §11.3, §15.6). The token is compared by
// its SHA-256, in constant time.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	hash := sha256.Sum256([]byte(token))
	if !strings.EqualFold(scheme, "Bearer") || token == "" || subtle.ConstantTimeCompare(hash[:], s.Token[:]) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		problem(w, r, http.StatusUnauthorized, "missing or wrong operator token")
		return
	}
	s.mux.ServeHTTP(w, r)
}

// importApp imports an application version (SPEC §5.3, §11.3).
func (s *server) importApp(w http.ResponseWriter, r *http.Request) {
	var req operatorapi.ImportRequest
	if !decode(w, r, &req) {
		return
	}
	if missing := required(field{"repository", req.Repository}, field{"version", req.Version}); missing != "" {
		problem(w, r, http.StatusBadRequest, missing)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.ImportTimeout)
	defer cancel()
	a, created, err := s.Catalog.Import(ctx, req.Repository, req.Version)
	// The error text names the repository and the reason, never a URL or what a registry sent
	// (SPEC §5.3, §15.4), so it is the detail.
	switch {
	case errors.Is(err, catalog.ErrConflict):
		problem(w, r, http.StatusConflict, err.Error())
	case errors.Is(err, catalog.ErrInvalidPackage), errors.Is(err, catalog.ErrInvalidRepository),
		errors.Is(err, catalog.ErrRegistry) && errors.Is(err, errdef.ErrNotFound):
		problem(w, r, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, catalog.ErrRegistry):
		problem(w, r, http.StatusBadGateway, err.Error())
	case err != nil:
		s.internalError(w, r, err)
	case created:
		writeJSON(w, http.StatusCreated, appOf(a))
	default:
		writeJSON(w, http.StatusOK, appOf(a))
	}
}

// listApps lists every imported version, by ID, then by version as exact strings (SPEC §11.3).
func (s *server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.Apps(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sortedApps(apps))
}

// getApp lists the imported versions of one application, by version (SPEC §11.3).
func (s *server) getApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("appId")
	apps, err := s.Store.AppVersions(r.Context(), id)
	switch {
	case err != nil:
		s.internalError(w, r, err)
	case len(apps) == 0:
		problem(w, r, http.StatusNotFound, "no version of application "+id+" is imported")
	default:
		writeJSON(w, http.StatusOK, sortedApps(apps))
	}
}

// listDevices lists the devices of every site, or of ?site=, by device ID (SPEC §11.3).
func (s *server) listDevices(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteQuery(w, r)
	if !ok {
		return
	}
	devices, err := s.Store.Devices(r.Context(), site)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]operatorapi.Device, len(devices))
	for i, d := range devices {
		out[i] = operatorapi.Device{DeviceID: d.ID, Capabilities: d.Capabilities}
	}
	slices.SortFunc(out, func(a, b operatorapi.Device) int { return strings.Compare(a.DeviceID.String(), b.DeviceID.String()) })
	writeJSON(w, http.StatusOK, out)
}

// create creates a deployment (SPEC §8.1, §11.3).
func (s *server) create(w http.ResponseWriter, r *http.Request) {
	var req operatorapi.CreateRequest
	if !decode(w, r, &req) {
		return
	}
	if missing := required(field{"appId", req.AppID}, field{"version", req.Version}, field{"target", req.Target}); missing != "" {
		problem(w, r, http.StatusBadRequest, missing)
		return
	}
	target, err := contract.ParseDeviceID(req.Target)
	if err != nil {
		problem(w, r, http.StatusBadRequest, "target: "+err.Error())
		return
	}
	params := req.Parameters
	if params == nil {
		params = map[string]any{}
	}
	d, err := s.Deploy.Create(r.Context(), deploy.Request{
		AppID: req.AppID, Version: req.Version, Profile: req.Profile, Target: target,
		Name: req.Name, Namespace: req.Namespace, Parameters: params,
	})
	switch {
	case errors.Is(err, deploy.ErrInvalidTarget): // SPEC §11.3: a site is not a target
		problem(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, deploy.ErrNotFound):
		problem(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, deploy.ErrInvalidRequest), errors.Is(err, deploy.ErrCheckFailed):
		problem(w, r, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, deploymentOf(d))
	}
}

// listDeployments lists the deployments of every site, or of ?site=, by ID (SPEC §11.3).
func (s *server) listDeployments(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteQuery(w, r)
	if !ok {
		return
	}
	ds, err := s.Store.Deployments(r.Context(), site)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]operatorapi.Deployment, len(ds))
	for i, d := range ds {
		out[i] = deploymentOf(d)
	}
	slices.SortFunc(out, func(a, b operatorapi.Deployment) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	writeJSON(w, http.StatusOK, out)
}

// getDeployment serves one deployment (SPEC §11.3).
func (s *server) getDeployment(w http.ResponseWriter, r *http.Request) {
	d, ok := s.deployment(w, r)
	if ok {
		writeJSON(w, http.StatusOK, deploymentOf(d))
	}
}

// status serves a deployment's current status and history (SPEC §8.1.2, §11.3).
func (s *server) status(w http.ResponseWriter, r *http.Request) {
	d, ok := s.deployment(w, r)
	if !ok {
		return
	}
	current, reported, err := s.Store.CurrentStatus(r.Context(), d.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	history, err := s.Store.StatusHistory(r.Context(), d.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := operatorapi.Status{History: history}
	if reported {
		out.Current = &current
	}
	if out.History == nil {
		out.History = []contract.DeploymentStatus{}
	}
	writeJSON(w, http.StatusOK, out)
}

// deployment returns the deployment the path names, or answers 400 or 404.
func (s *server) deployment(w http.ResponseWriter, r *http.Request) (deploy.Deployment, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil || id.String() != r.PathValue("id") {
		problem(w, r, http.StatusBadRequest, "not a deployment ID in canonical form (SPEC §4.2)")
		return deploy.Deployment{}, false
	}
	d, ok, err := s.Store.Deployment(r.Context(), id)
	switch {
	case err != nil:
		s.internalError(w, r, err)
		return deploy.Deployment{}, false
	case !ok:
		problem(w, r, http.StatusNotFound, "no deployment "+id.String())
		return deploy.Deployment{}, false
	}
	return d, true
}

// siteQuery returns the site ?site= names, "" when there is none, or answers 400 or 404.
func (s *server) siteQuery(w http.ResponseWriter, r *http.Request) (contract.SiteID, bool) {
	q := r.URL.Query()
	if !q.Has("site") {
		return "", true
	}
	id, err := contract.ParseSiteID(q.Get("site"))
	if err != nil {
		problem(w, r, http.StatusBadRequest, "site: "+err.Error())
		return "", false
	}
	_, ok, err := s.Store.Site(r.Context(), id)
	switch {
	case err != nil:
		s.internalError(w, r, err)
		return "", false
	case !ok:
		problem(w, r, http.StatusNotFound, "no site "+string(id))
		return "", false
	}
	return id, true
}

func appOf(a catalog.App) operatorapi.App {
	return operatorapi.App{ID: a.ID, Version: a.Version, Repository: a.Repository, Digest: a.Digest}
}

// sortedApps returns apps by ID, then by version, both as exact strings (SPEC §11.3).
func sortedApps(apps []catalog.App) []operatorapi.App {
	out := make([]operatorapi.App, len(apps))
	for i, a := range apps {
		out[i] = appOf(a)
	}
	slices.SortFunc(out, func(a, b operatorapi.App) int {
		if c := strings.Compare(a.ID, b.ID); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	return out
}

func deploymentOf(d deploy.Deployment) operatorapi.Deployment {
	params := d.Parameters
	if params == nil {
		params = map[string]any{}
	}
	return operatorapi.Deployment{
		ID: d.ID, AppID: d.AppID, Version: d.AppVersion, Profile: d.Profile, Target: d.Target,
		Name: d.Name, Namespace: d.Namespace, Parameters: params, Digest: d.Digest, Deleted: d.Deleted,
	}
}

// decode reads the request body into v, or answers 400 or 413: a body that is not one JSON value
// or has an unknown field is a 400 (SPEC §11.3).
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	b, code := httpapi.ReadBody(w, r, maxBody)
	switch code {
	case 0:
	case http.StatusRequestEntityTooLarge:
		problem(w, r, code, fmt.Sprintf("the request body is larger than %d bytes", maxBody))
		return false
	default:
		problem(w, r, code, "unreadable request body")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		problem(w, r, http.StatusBadRequest, "request body: "+err.Error())
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		problem(w, r, http.StatusBadRequest, "request body: more than one JSON value")
		return false
	}
	return true
}

// field is a request body field, by its JSON name.
type field struct{ name, value string }

// required returns a detail naming the first of fields that is empty, or "".
func required(fields ...field) string {
	for _, f := range fields {
		if f.value == "" {
			return "request body: " + f.name + " is required"
		}
	}
	return ""
}

// writeJSON writes v as the JSON body of a response with status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil { // cannot happen: parameters hold only what JSON decoded
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	_, _ = w.Write(b) // the client is gone
}

// methodNotAllowed answers 405, naming the methods the path serves (SPEC §11.3).
func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		problem(w, r, http.StatusMethodNotAllowed, r.Method+" is not served here; the methods are "+allow)
	}
}

// problem answers an about:blank problem whose title is the status phrase (SPEC §11.3).
func problem(w http.ResponseWriter, r *http.Request, code int, detail string) {
	httpapi.Problem(w, r, code, contract.ProblemAboutBlank, "", detail)
}

// internalError logs err and answers 500 without detail, so internals don't reach the caller.
// The log carries the method and path, never request headers (SPEC §15.4).
func (s *server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	httpapi.InternalError(w, r, s.Log, "operator api request failed", err)
}
