// Package catalog imports Margo applications from an OCI registry into the CO's catalog (SPEC
// §5.1, §5.3). It owns the application package layout and the import rules; the registry and the
// storage are passed in.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"

	"github.com/balaji-balu/ieo/internal/contract"
)

// ErrInvalidPackage is returned, wrapped with the reason, when an import breaks a rule of SPEC
// §5.3. Nothing is stored.
var ErrInvalidPackage = errors.New("invalid application package")

// ErrConflict is returned, wrapped with the application ID and version, when that version was
// already imported with a different Application Description (SPEC §5.3). The stored version is
// kept.
var ErrConflict = errors.New("application version already imported with different content")

// App is one imported application version. Description holds the exact bytes of the Application
// Description and Digest their SHA-256; they never change once stored.
type App struct {
	ID          string
	Version     string
	Repository  string
	Description []byte
	Digest      contract.Digest
}

// OpenRepository returns the OCI repository named repository (e.g. `registry.example/org/app`),
// for reading.
type OpenRepository func(ctx context.Context, repository string) (oras.ReadOnlyTarget, error)

// Store keeps imported applications.
type Store interface {
	// PutApp stores app unless its (ID, Version) is already stored, and returns the app stored
	// for that (ID, Version): app itself, or the one stored before.
	PutApp(ctx context.Context, app App) (App, error)
}

// Catalog imports application versions.
type Catalog struct {
	open  OpenRepository
	store Store
}

// New returns a Catalog that reads packages through open and keeps them in store.
func New(open OpenRepository, store Store) *Catalog {
	return &Catalog{open: open, store: store}
}

// Import fetches the package tagged version from repository, validates it (SPEC §5.3) and stores
// it. Re-importing an identical version returns the stored app and changes nothing. A rule
// violation wraps ErrInvalidPackage, a different description for a stored version wraps
// ErrConflict, and registry failures are returned wrapped.
func (c *Catalog) Import(ctx context.Context, repository, version string) (App, error) {
	app, err := c.fetch(ctx, repository, version)
	if err != nil {
		return App{}, fmt.Errorf("import %s:%s: %w", repository, version, err)
	}
	stored, err := c.store.PutApp(ctx, app)
	if err != nil {
		return App{}, fmt.Errorf("import %s:%s: store: %w", repository, version, err)
	}
	if stored.Digest != app.Digest {
		return App{}, fmt.Errorf("import %s:%s: %w: %s %s was imported from %s with description %s, this one is %s",
			repository, version, ErrConflict, app.ID, app.Version, stored.Repository, stored.Digest, app.Digest)
	}
	return stored, nil
}

// Size limits for what a registry returns. A Margo manifest lists a handful of files and an
// Application Description is a few KiB; anything far larger is not a Margo package.
const (
	maxManifestBytes    = 256 << 10
	maxDescriptionBytes = 1 << 20
)

// fetch reads the package tagged version and checks it against SPEC §5.1 and §5.3. Rule
// violations wrap ErrInvalidPackage; other errors come from the registry.
func (c *Catalog) fetch(ctx context.Context, repository, version string) (App, error) {
	repo, err := c.open(ctx, repository)
	if err != nil {
		return App{}, fmt.Errorf("open repository: %w", err)
	}
	desc, manifestBytes, err := oras.FetchBytes(ctx, repo, version, oras.FetchBytesOptions{MaxBytes: maxManifestBytes})
	if err != nil {
		return App{}, fmt.Errorf("fetch manifest: %w", err)
	}
	layer, err := descriptionLayer(desc, manifestBytes)
	if err != nil {
		return App{}, fmt.Errorf("%w: %w", ErrInvalidPackage, err)
	}
	b, err := content.FetchAll(ctx, repo, layer) // verifies size and digest
	if err != nil {
		return App{}, fmt.Errorf("fetch application description %s: %w", layer.Digest, err)
	}
	parsed, err := contract.ParseApplicationDescription(b)
	if err != nil {
		return App{}, fmt.Errorf("%w: %w", ErrInvalidPackage, err)
	}
	if err := checkImportRules(parsed, version); err != nil {
		return App{}, fmt.Errorf("%w: %w", ErrInvalidPackage, err)
	}
	return App{
		ID:          parsed.ID,
		Version:     parsed.Metadata.Version,
		Repository:  repository,
		Description: b,
		Digest:      contract.DigestOf(b),
	}, nil
}

// descriptionLayer returns the layer holding the Application Description, if the manifest is a
// Margo application package (SPEC §5.1).
func descriptionLayer(desc ocispec.Descriptor, manifestBytes []byte) (ocispec.Descriptor, error) {
	if desc.MediaType != ocispec.MediaTypeImageManifest {
		return ocispec.Descriptor{}, fmt.Errorf("manifest media type %q, want %q", desc.MediaType, ocispec.MediaTypeImageManifest)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("decode manifest: %w", err)
	}
	if m.ArtifactType != contract.AppPackageArtifactType {
		return ocispec.Descriptor{}, fmt.Errorf("manifest artifactType %q, want %q", m.ArtifactType, contract.AppPackageArtifactType)
	}
	if m.Config.MediaType != ocispec.MediaTypeEmptyJSON {
		return ocispec.Descriptor{}, fmt.Errorf("manifest config media type %q, want the empty config %q",
			m.Config.MediaType, ocispec.MediaTypeEmptyJSON)
	}
	var found []ocispec.Descriptor
	for _, l := range m.Layers {
		if l.MediaType == contract.AppDescriptionMediaType {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		return ocispec.Descriptor{}, fmt.Errorf("want exactly one application description layer (%s), found %d",
			contract.AppDescriptionMediaType, len(found))
	}
	if found[0].Size > maxDescriptionBytes {
		return ocispec.Descriptor{}, fmt.Errorf("application description is %d bytes, limit %d", found[0].Size, maxDescriptionBytes)
	}
	return found[0], nil
}

// checkImportRules checks the SPEC §5.3 rules that the pinned schema does not express. The schema
// already holds the `oci://` repository and §4.2 revision forms.
func checkImportRules(d contract.ApplicationDescription, tag string) error {
	if d.Metadata.Version != tag {
		return fmt.Errorf("metadata.version %q differs from the tag %q", d.Metadata.Version, tag)
	}
	hasCompose := false
	var components []string
	for _, p := range d.DeploymentProfiles {
		hasCompose = hasCompose || p.Type == contract.ProfileTypeCompose
		for _, comp := range p.Components {
			components = append(components, comp.Name)
		}
	}
	if !hasCompose {
		return fmt.Errorf("no deployment profile of type %q", contract.ProfileTypeCompose)
	}
	for _, name := range slices.Sorted(maps.Keys(d.Parameters)) { // a stable first violation
		for _, target := range d.Parameters[name].Targets {
			for _, comp := range target.Components {
				if !slices.Contains(components, comp) {
					return fmt.Errorf("parameter %q targets component %q, which is in no deployment profile", name, comp)
				}
			}
		}
	}
	return nil
}
