// Package postgres is the Postgres backend of the CO store (SPEC §12, roadmap B4). It keeps the
// same rules as store.Memory: changes to one site are atomic and serialized, a digest is served to
// a site only once a manifest of that site published it, only a token's SHA-256 is stored, a write
// never keeps the caller's maps or slices, and a site's manifestVersion never decreases.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/app"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/blob"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/deployment"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/publication"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/site"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/status"
	"github.com/balaji-balu/ieo/internal/contract"
)

// Store is a Postgres store. It is safe for concurrent use, also by several CO processes sharing
// one database.
type Store struct {
	db     *sql.DB
	client *ent.Client
}

// Open connects to the database at dsn (a lib/pq URL or key=value string), applies the migrations
// it lacks, and returns the store. Errors never repeat dsn, which may hold a password (SPEC §15.4).
func Open(ctx context.Context, dsn string) (*Store, error) {
	connector, err := pq.NewConnector(dsn)
	if err != nil { // lib/pq's parse errors quote dsn
		return nil, errors.New("open co store: malformed database URL or connection string")
	}
	db := sql.OpenDB(connector)
	if err := migrate(ctx, db); err != nil {
		_ = db.Close() // the migration error is the one to report
		return nil, fmt.Errorf("open co store: %w", err)
	}
	return &Store{db: db, client: ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))}, nil
}

// Close closes the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// PutApp stores app unless its (ID, Version) is already stored, and returns the app stored for
// that (ID, Version).
func (s *Store) PutApp(ctx context.Context, a catalog.App) (catalog.App, error) {
	err := s.client.App.Create().
		SetAppID(a.ID).SetVersion(a.Version).SetRepository(a.Repository).
		SetDescription(a.Description).SetDigest(string(a.Digest)).
		OnConflictColumns(app.FieldAppID, app.FieldVersion).DoNothing().
		Exec(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) { // no row: the version was stored before
		return catalog.App{}, fmt.Errorf("put app %s %s: %w", a.ID, a.Version, err)
	}
	stored, ok, err := s.App(ctx, a.ID, a.Version)
	switch {
	case err != nil:
		return catalog.App{}, err
	case !ok:
		return catalog.App{}, fmt.Errorf("put app %s %s: not stored", a.ID, a.Version)
	}
	return stored, nil
}

// App returns the stored app with that ID and version, and whether there is one.
func (s *Store) App(ctx context.Context, id, version string) (catalog.App, bool, error) {
	a, err := s.client.App.Query().Where(app.AppID(id), app.Version(version)).Only(ctx)
	if ent.IsNotFound(err) {
		return catalog.App{}, false, nil
	}
	if err != nil {
		return catalog.App{}, false, fmt.Errorf("get app %s %s: %w", id, version, err)
	}
	return catalog.App{
		ID: a.AppID, Version: a.Version, Repository: a.Repository, Description: a.Description,
		Digest: contract.Digest(a.Digest),
	}, true, nil
}

// PutDevice replaces the latest capabilities reported for a device of an existing site (SPEC
// §8.3).
func (s *Store) PutDevice(ctx context.Context, id contract.DeviceID, caps contract.DeviceCapabilitiesManifest) error {
	return s.ChangeDevice(ctx, id.Site, id, func(deploy.DeviceState, bool) (deploy.SiteChange, error) {
		return deploy.SiteChange{Device: &deploy.DeviceChange{ID: id, Capabilities: &caps}}, nil
	})
}

// Deployment returns a deployment, and whether it exists.
func (s *Store) Deployment(ctx context.Context, id uuid.UUID) (deploy.Deployment, bool, error) {
	row, err := s.client.Deployment.Get(ctx, id)
	if ent.IsNotFound(err) {
		return deploy.Deployment{}, false, nil
	}
	if err != nil {
		return deploy.Deployment{}, false, fmt.Errorf("get deployment %s: %w", id, err)
	}
	d, err := deploymentOf(row)
	return d, err == nil, err
}

// DeploymentSite returns the site of a deployment, and whether the deployment exists.
func (s *Store) DeploymentSite(ctx context.Context, id uuid.UUID) (contract.SiteID, bool, error) {
	row, err := s.client.Deployment.Query().Where(deployment.ID(id)).Select(deployment.FieldSiteID).Only(ctx)
	if ent.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get site of deployment %s: %w", id, err)
	}
	return contract.SiteID(row.SiteID), true, nil
}

// Site returns a site, and whether it exists.
func (s *Store) Site(ctx context.Context, id contract.SiteID) (deploy.Site, bool, error) {
	row, ok, err := siteRow(ctx, s.client.Site, id)
	return siteOf(row), ok, err
}

// Manifest returns a site's published manifest, and whether the site has one.
func (s *Store) Manifest(ctx context.Context, id contract.SiteID) (deploy.Manifest, bool, error) {
	row, ok, err := siteRow(ctx, s.client.Site, id)
	if err != nil || !ok || row.ManifestVersion == 0 {
		return deploy.Manifest{}, false, err
	}
	return manifestOf(row), true, nil
}

// Blob returns the content stored under digest, and whether there is any.
func (s *Store) Blob(ctx context.Context, digest contract.Digest) ([]byte, bool, error) {
	b, err := s.client.Blob.Get(ctx, string(digest))
	if ent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get blob %s: %w", digest, err)
	}
	return b.Content, true, nil
}

// DeploymentYAML returns the YAML of deployment id under digest, and whether site published it:
// the deployment is at site and had digest in one of its manifests.
func (s *Store) DeploymentYAML(ctx context.Context, site contract.SiteID, id uuid.UUID, digest contract.Digest) ([]byte, bool, error) {
	return s.publishedBlob(ctx, site, false, id, digest)
}

// Bundle returns the bundle under digest, and whether one of site's manifests named it.
func (s *Store) Bundle(ctx context.Context, site contract.SiteID, digest contract.Digest) ([]byte, bool, error) {
	return s.publishedBlob(ctx, site, true, uuid.Nil, digest)
}

func (s *Store) publishedBlob(ctx context.Context, siteID contract.SiteID, bundle bool, id uuid.UUID,
	digest contract.Digest) ([]byte, bool, error) {
	if digest == "" {
		return nil, false, nil
	}
	ok, err := s.client.Publication.Query().Where(
		publication.SiteID(string(siteID)), publication.Bundle(bundle),
		publication.DeploymentID(id), publication.Digest(string(digest)),
	).Exist(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("find publication of %s at site %s: %w", digest, siteID, err)
	}
	if !ok {
		return nil, false, nil
	}
	return s.Blob(ctx, digest)
}

// PutSiteToken replaces the token hash of site, and reports false if there is no such site.
func (s *Store) PutSiteToken(ctx context.Context, id contract.SiteID, hash auth.TokenHash) (bool, error) {
	n, err := s.client.Site.Update().Where(site.ID(string(id))).SetTokenHash(hash[:]).Save(ctx)
	if err != nil {
		return false, fmt.Errorf("put token hash of site %s: %w", id, err) // never the hash itself
	}
	return n == 1, nil
}

// SiteByToken returns the site whose token has hash, and whether there is one.
func (s *Store) SiteByToken(ctx context.Context, hash auth.TokenHash) (contract.SiteID, bool, error) {
	id, err := s.client.Site.Query().Where(site.TokenHash(hash[:])).OnlyID(ctx)
	if ent.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find site by token hash: %w", err)
	}
	return contract.SiteID(id), true, nil
}

// CurrentStatus returns the current status of a deployment, and whether it has one.
func (s *Store) CurrentStatus(ctx context.Context, id uuid.UUID) (contract.DeploymentStatus, bool, error) {
	row, err := s.client.Deployment.Query().Where(deployment.ID(id)).Select(deployment.FieldCurrentStatus).Only(ctx)
	if ent.IsNotFound(err) || err == nil && row.CurrentStatus == nil {
		return contract.DeploymentStatus{}, false, nil
	}
	if err != nil {
		return contract.DeploymentStatus{}, false, fmt.Errorf("get current status of %s: %w", id, err)
	}
	st, err := decodeStatus(*row.CurrentStatus)
	return st, err == nil, err
}

// StatusHistory returns every status reported for a deployment, oldest first.
func (s *Store) StatusHistory(ctx context.Context, id uuid.UUID) ([]contract.DeploymentStatus, error) {
	rows, err := s.client.Status.Query().Where(status.DeploymentID(id)).Order(ent.Asc(status.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("get status history of %s: %w", id, err)
	}
	out := make([]contract.DeploymentStatus, len(rows))
	for i, r := range rows {
		if out[i], err = decodeStatus(r.Status); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// blobs returns the content stored under each of digests.
func blobs(ctx context.Context, tx *ent.Tx, digests []string) (map[string][]byte, error) {
	rows, err := tx.Blob.Query().Where(blob.IDIn(digests...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("get blobs: %w", err)
	}
	out := make(map[string][]byte, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Content
	}
	return out, nil
}

// Sites returns every site in ID order, retired ones included.
func (s *Store) Sites(ctx context.Context) ([]deploy.Site, error) {
	rows, err := s.client.Site.Query().Select(site.FieldID, site.FieldRetired).Order(ent.Asc(site.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	sites := make([]deploy.Site, len(rows))
	for i, row := range rows {
		sites[i] = siteOf(row)
	}
	return sites, nil
}

// Ping reports whether the database answers, for the CO's health check (SPEC §13).
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping co store: %w", err)
	}
	return nil
}
