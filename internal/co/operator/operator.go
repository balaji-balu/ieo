// Package operator serves the CO's operator API to edgectl (SPEC §11.3). Every request needs the
// static operator token (SPEC §15.6); the CO holds only its SHA-256.
package operator

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/contract"
)

// Importer imports an application version (SPEC §5.3); catalog.Catalog is one.
type Importer interface {
	Import(ctx context.Context, repository, version string) (catalog.App, error)
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
// open: the import then fails as an unreachable registry does (SPEC §11.3, `502`).
const DefaultImportTimeout = 2 * time.Minute

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

// New returns the operator API handler.
func New(cfg Config) http.Handler {
	return http.NotFoundHandler() // roadmap E4: not yet
}
