package main

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/contract"
)

const (
	// harnessRegistry is the registry as the hosts and the CO's packages name it (compose.yaml).
	harnessRegistry = "registry:5000"
	// defaultAppRepository is where the test application's package is pushed and imported from.
	defaultAppRepository = harnessRegistry + "/ieo-harness/hello"
	// sourceImage is the image the test application runs, pinned by the digest of its index.
	sourceImage = "registry-1.docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	// harnessImage is where sourceImage is copied; tests/harness/hello/web/compose.yaml names it.
	harnessImage = harnessRegistry + "/ieo-harness/busybox"
	// descriptionFile is the Application Description in the application's directory.
	descriptionFile = "margo.yaml"
)

// createdAt is the creation time written into every manifest the tool packs, so a rerun packs
// the same manifests.
const createdAt = "1970-01-01T00:00:00Z"

// openFunc returns a repository named as the hosts name it (`registry:5000/<path>`), to push to.
type openFunc func(ctx context.Context, repository string) (oras.Target, error)

// image is an image to copy into the harness registry: digest in src, copied to repository dest.
type image struct {
	src    oras.ReadOnlyTarget
	digest string
	dest   string
}

// harness deploys the test application in appDir to target.
type harness struct {
	appDir        string
	appRepository string
	target        contract.DeviceID
	open          openFunc
	image         image
}

// coStore is the CO store the tool writes, as the CO's own commands do.
type coStore interface {
	catalog.Store
	deploy.Store
}

// description reads and parses the application's description.
func (h harness) description() ([]byte, contract.ApplicationDescription, error) {
	b, err := os.ReadFile(filepath.Join(h.appDir, descriptionFile))
	if err != nil {
		return nil, contract.ApplicationDescription{}, err
	}
	desc, err := contract.ParseApplicationDescription(b)
	return b, desc, err
}

// components returns the names of the components of deployment profile id, in order.
func (h harness) components(id string) ([]string, error) {
	_, desc, err := h.description()
	if err != nil {
		return nil, err
	}
	for _, p := range desc.DeploymentProfiles {
		if p.ID == id {
			var names []string
			for _, c := range p.Components {
				names = append(names, c.Name)
			}
			return names, nil
		}
	}
	return nil, fmt.Errorf("no deployment profile %q in %s", id, descriptionFile)
}

// deploy pushes the image, the application's Compose archives and its package, imports it (SPEC
// §5.3), stands in for the LO's capability reports (SPEC §8.3, until roadmap slice H) and creates
// a directed deployment of it to h.target (SPEC §8.1). When the site already has a deployment of
// this application version to h.target, it returns that one and creates none: created is false.
func (h harness) deploy(ctx context.Context, s coStore) (d deploy.Deployment, created bool, err error) {
	descBytes, desc, err := h.description()
	if err != nil {
		return deploy.Deployment{}, false, err
	}
	if err := h.copyImage(ctx); err != nil {
		return deploy.Deployment{}, false, err
	}
	for _, p := range desc.DeploymentProfiles {
		if p.Type != contract.ProfileTypeCompose {
			continue
		}
		for _, c := range p.Components {
			if err := h.pushComponent(ctx, c); err != nil {
				return deploy.Deployment{}, false, err
			}
		}
	}
	if err := h.pushPackage(ctx, desc.Metadata.Version, descBytes); err != nil {
		return deploy.Deployment{}, false, err
	}
	readOnly := func(ctx context.Context, repository string) (oras.ReadOnlyTarget, error) {
		return h.open(ctx, repository)
	}
	app, err := catalog.New(readOnly, s).Import(ctx, h.appRepository, desc.Metadata.Version)
	if err != nil {
		return deploy.Deployment{}, false, err
	}
	svc := deploy.New(s)
	if err := reportDevices(ctx, svc, h.target); err != nil {
		return deploy.Deployment{}, false, err
	}
	return deployOnce(ctx, s, svc, deploy.Request{AppID: app.ID, Version: app.Version, Target: h.target})
}

// copyImage copies h.image, every platform of it, into the harness registry. It is pushed by
// digest, with no tag: the test application names it by digest.
func (h harness) copyImage(ctx context.Context) error {
	root, err := h.image.src.Resolve(ctx, h.image.digest)
	if err != nil {
		return fmt.Errorf("resolve image %s: %w", h.image.digest, err)
	}
	dst, err := h.open(ctx, h.image.dest)
	if err != nil {
		return err
	}
	if err := oras.CopyGraph(ctx, h.image.src, dst, root, oras.DefaultCopyGraphOptions); err != nil {
		return fmt.Errorf("copy image %s to %s: %w", h.image.digest, h.image.dest, err)
	}
	return nil
}

// pushComponent pushes the Compose archive of component c, built from the directory named after
// it, to its repository at its revision (SPEC §5.1).
func (h harness) pushComponent(ctx context.Context, c contract.Component) error {
	repository, ok := strings.CutPrefix(c.Properties.Repository, "oci://")
	if !ok {
		return fmt.Errorf("component %s: repository %q is not an oci:// reference", c.Name, c.Properties.Repository)
	}
	a, err := packArchive(filepath.Join(h.appDir, c.Name), c.Name)
	if err != nil {
		return fmt.Errorf("component %s: %w", c.Name, err)
	}
	layer := content.NewDescriptorFromBytes(contract.ComposeArchiveMediaType, a)
	err = h.pushArtifact(ctx, repository, c.Properties.Revision, contract.ComposeArchiveArtifactType, layer, a)
	if err != nil {
		return fmt.Errorf("component %s: %w", c.Name, err)
	}
	return nil
}

// pushPackage pushes the application package: the description as its one layer, tagged version
// (SPEC §5.1).
func (h harness) pushPackage(ctx context.Context, version string, desc []byte) error {
	layer := content.NewDescriptorFromBytes(contract.AppDescriptionMediaType, desc)
	layer.Annotations = map[string]string{ocispec.AnnotationTitle: descriptionFile}
	return h.pushArtifact(ctx, h.appRepository, version, contract.AppPackageArtifactType, layer, desc)
}

// pushArtifact pushes data as the one layer of an OCI image manifest with artifactType and the
// empty config, and tags it in repository. A tag that exists is moved.
func (h harness) pushArtifact(ctx context.Context, repository, tag, artifactType string, layer ocispec.Descriptor, data []byte) error {
	dst, err := h.open(ctx, repository)
	if err != nil {
		return err
	}
	if err := dst.Push(ctx, layer, bytes.NewReader(data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return fmt.Errorf("push %s to %s: %w", layer.MediaType, repository, err)
	}
	m, err := oras.PackManifest(ctx, dst, oras.PackManifestVersion1_1, artifactType, oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{layer},
		ManifestAnnotations: map[string]string{ocispec.AnnotationCreated: createdAt},
	})
	if err != nil {
		return fmt.Errorf("push manifest to %s: %w", repository, err)
	}
	if err := dst.Tag(ctx, m, tag); err != nil {
		return fmt.Errorf("tag %s:%s: %w", repository, tag, err)
	}
	return nil
}

// packArchive returns a Margo Compose Archive (SPEC §5.2) of dir: a gzip tar whose one top-level
// directory top holds dir's files and directories. Owners and times are left out, so the same
// directory gives the same bytes. dir may hold nothing but regular files and directories.
func packArchive(dir, top string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf) // no name and no time in the gzip header
	tw := tar.NewWriter(gz)
	root := os.DirFS(dir)
	err := fs.WalkDir(root, ".", func(name string, e fs.DirEntry, err error) error { // in lexical order
		if err != nil {
			return err
		}
		h := &tar.Header{Name: path.Join(top, name), Format: tar.FormatUSTAR}
		switch {
		case e.IsDir():
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, h.Name+"/", 0o755
			return tw.WriteHeader(h)
		case e.Type().IsRegular():
			b, err := fs.ReadFile(root, name)
			if err != nil {
				return err
			}
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o644, int64(len(b))
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			_, err = tw.Write(b)
			return err
		default:
			return fmt.Errorf("%s is not a regular file or a directory", name)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", dir, err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// reportDevices stores capabilities for the site's gateway and for host target, as the LO's
// reports would (SPEC §8.3): the CO creates a directed deployment only to a host it knows (SPEC
// §8.1.1). The LO reports none until roadmap slice H. A report replaces the one before.
func reportDevices(ctx context.Context, svc *deploy.Service, target contract.DeviceID) error {
	gateway := contract.DeviceID{Site: target.Site}
	reports := []contract.DeviceCapabilities{
		{ID: gateway, Vendor: "ieo", ModelNumber: "dev-harness-lo", SerialNumber: string(target.Site)},
		{ID: target, Vendor: "ieo", ModelNumber: "dev-harness-host", SerialNumber: string(target.Host),
			SupportedDeploymentTypes: []string{contract.ProfileTypeCompose}},
	}
	for _, caps := range reports { // the gateway first (SPEC §8.3)
		if _, err := svc.ReportCapabilities(ctx, target.Site, caps.ID, contract.DeviceCapabilitiesManifest{Properties: caps}); err != nil {
			return err
		}
	}
	return nil
}

// deployOnce creates the deployment req asks for, unless the site has one of the same application
// version to the same target that is not deleted: then it returns that one, and created is false.
func deployOnce(ctx context.Context, s deploy.Store, svc *deploy.Service, req deploy.Request) (d deploy.Deployment, created bool, err error) {
	var found []deploy.Deployment
	err = s.ChangeSite(ctx, req.Target.Site, func(state deploy.SiteState, _ bool) (deploy.SiteChange, error) {
		for _, d := range state.Deployments {
			if !d.Deleted && d.AppID == req.AppID && d.AppVersion == req.Version && d.Target == req.Target {
				found = append(found, d)
			}
		}
		return deploy.SiteChange{}, nil // a read: nothing is written
	})
	if err != nil {
		return deploy.Deployment{}, false, err
	}
	if len(found) > 0 {
		return slices.MinFunc(found, func(a, b deploy.Deployment) int { return cmp.Compare(a.ID.String(), b.ID.String()) }), false, nil
	}
	d, err = svc.Create(ctx, req)
	return d, err == nil, err
}

// openThrough returns an openFunc that reaches the harness registry at addr (the laptop's
// localhost:5000), over plain HTTP, as the hosts do. It opens no other registry.
func openThrough(addr string) openFunc {
	return func(_ context.Context, repository string) (oras.Target, error) {
		name, ok := strings.CutPrefix(repository, harnessRegistry+"/")
		if !ok {
			return nil, fmt.Errorf("repository %s is not in the harness registry %s", repository, harnessRegistry)
		}
		repo, err := remote.NewRepository(addr + "/" + name)
		if err != nil {
			return nil, err
		}
		repo.PlainHTTP = true // the harness registry is plain HTTP (deploy/dev/compose.yaml)
		return repo, nil
	}
}
