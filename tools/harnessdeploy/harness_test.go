package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store"
	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/pull"
)

// appDir is the harness's test application, from this package's directory.
var appDir = filepath.Join("..", "..", "tests", "harness", "hello")

var target = contract.DeviceID{Site: "00000000-0000-4000-8000-000000000001", Host: "host-1"}

// registry is an in-memory registry: one store per repository, created on first open.
type registry struct {
	mu    sync.Mutex
	repos map[string]*memory.Store
}

func (r *registry) open(_ context.Context, repository string) (oras.Target, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repos == nil {
		r.repos = map[string]*memory.Store{}
	}
	if r.repos[repository] == nil {
		r.repos[repository] = memory.New()
	}
	return r.repos[repository], nil
}

func (r *registry) openReadOnly(ctx context.Context, repository string) (oras.ReadOnlyTarget, error) {
	return r.open(ctx, repository)
}

// fakeImage returns a one-layer image in a store of its own, standing in for busybox on Docker Hub.
func fakeImage(t *testing.T) (oras.ReadOnlyTarget, ocispec.Descriptor) {
	t.Helper()
	ctx := context.Background()
	src := memory.New()
	layer := []byte("busybox")
	ld := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayerGzip, layer)
	if err := src.Push(ctx, ld, bytes.NewReader(layer)); err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	cd := content.NewDescriptorFromBytes(ocispec.MediaTypeImageConfig, config)
	if err := src.Push(ctx, cd, bytes.NewReader(config)); err != nil {
		t.Fatal(err)
	}
	m, err := oras.PackManifest(ctx, src, oras.PackManifestVersion1_1, "",
		oras.PackManifestOptions{ConfigDescriptor: &cd, Layers: []ocispec.Descriptor{ld}})
	if err != nil {
		t.Fatal(err)
	}
	// A registry resolves a manifest by its digest; the memory store, only by a tag.
	if err := src.Tag(ctx, m, m.Digest.String()); err != nil {
		t.Fatal(err)
	}
	return src, m
}

// newHarness returns a harness over reg, a CO store holding the target's site, and the image the
// harness copies.
func newHarness(t *testing.T, reg *registry) (harness, *store.Memory, ocispec.Descriptor) {
	t.Helper()
	src, img := fakeImage(t)
	s := store.NewMemory()
	if _, err := deploy.New(s).AddSite(context.Background(), target.Site); err != nil {
		t.Fatal(err)
	}
	return harness{
		appDir:        appDir,
		appRepository: defaultAppRepository,
		target:        target,
		open:          reg.open,
		image:         image{src: src, digest: img.Digest.String(), dest: harnessImage},
	}, s, img
}

// The archive built from a component's directory is valid for the EN (SPEC §5.2) and the same
// bytes each time, so a rerun pushes the same digest.
func TestArchiveIsValidAndStable(t *testing.T) {
	dir := filepath.Join(appDir, "web")
	a, err := packArchive(dir, "web")
	if err != nil {
		t.Fatalf("packArchive: %v", err)
	}
	// Owners and times are left out: a file touched since packs the same.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, archive.ComposeFile), later, later); err != nil {
		t.Fatal(err)
	}
	b, err := packArchive(dir, "web")
	if err != nil {
		t.Fatalf("packArchive: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("two archives of the same directory differ")
	}
	out := t.TempDir()
	top, err := archive.Extract(context.Background(), bytes.NewReader(a), out, archive.Limits{MaxEntries: 100, MaxExtractedBytes: 1 << 20})
	if err != nil {
		t.Fatalf("the EN rejects the archive: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, top, archive.ComposeFile))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(dir, archive.ComposeFile))
	if err != nil {
		t.Fatal(err)
	}
	if top != "web" || !bytes.Equal(got, want) {
		t.Errorf("extracted %s/compose.yaml = %q, want web/compose.yaml = %q", top, got, want)
	}
}

// The test application runs the image the tool copies, by the digest the tool copies.
func TestComposeFileUsesCopiedImage(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(appDir, "web", archive.ComposeFile))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, _ := strings.Cut(sourceImage, "@")
	if want := "image: " + harnessImage + "@" + digest; !strings.Contains(string(b), want) {
		t.Errorf("web/compose.yaml does not hold %q", want)
	}
}

// One run pushes the image, the archive and the package that the CO imports and the EN pulls
// (SPEC §5.1, §5.3, §8.9 step 4.2), and reports the site's gateway and the target host (SPEC
// §8.3). It imports and deploys nothing: the golden path does, through edgectl (tests/harness).
func TestPrepareHarness(t *testing.T) {
	ctx := context.Background()
	reg := &registry{}
	h, s, img := newHarness(t, reg)

	if err := h.prepare(ctx, s); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	dst, _ := reg.open(ctx, harnessImage)
	if ok, err := dst.Exists(ctx, img); err != nil || !ok {
		t.Errorf("image %s@%s not copied (err %v)", harnessImage, img.Digest, err)
	}

	if apps, err := s.Apps(ctx); err != nil || len(apps) != 0 {
		t.Errorf("prepare imported %d applications (err %v), want none", len(apps), err)
	}
	// The CO imports the package as `edgectl app import` asks it to.
	app, _, err := catalog.New(reg.openReadOnly, s).Import(ctx, defaultAppRepository, "1.0.0")
	if err != nil {
		t.Fatalf("the CO can't import the package: %v", err)
	}

	// The CO accepts a directed deployment to the reported host, and the EN can pull its archive.
	d, err := deploy.New(s).Create(ctx, deploy.Request{AppID: app.ID, Version: app.Version, Target: target})
	if err != nil {
		t.Fatalf("the CO refuses a deployment to %s: %v", target, err)
	}
	yaml, ok, err := s.Blob(ctx, d.Digest)
	if err != nil || !ok {
		t.Fatalf("deployment YAML %s not stored (err %v)", d.Digest, err)
	}
	dep, err := contract.DecodeApplicationDeployment(d.ID, yaml)
	if err != nil {
		t.Fatal(err)
	}
	comps := dep.Spec.DeploymentProfile.Components
	if len(comps) != 1 || comps[0].Name != "web" {
		t.Fatalf("components = %+v, want web", comps)
	}
	p := pull.New(reg.openReadOnly, t.TempDir(), pull.Limits{MaxBytes: 1 << 20, Timeout: time.Minute})
	layer, err := p.ComposeArchive(ctx, comps[0].Properties.Repository, comps[0].Properties.Revision)
	if err != nil {
		t.Fatalf("the EN can't pull the archive: %v", err)
	}
	defer func() { _ = layer.Close() }()
	if _, err := archive.Extract(ctx, layer, t.TempDir(), archive.Limits{MaxEntries: 100, MaxExtractedBytes: 1 << 20}); err != nil {
		t.Errorf("the EN rejects the pulled archive: %v", err)
	}
}

// A second run succeeds and leaves the site's manifest as it was: prepare deploys nothing.
func TestPrepareHarnessTwice(t *testing.T) {
	ctx := context.Background()
	reg := &registry{}
	h, s, _ := newHarness(t, reg)
	if err := h.prepare(ctx, s); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	m1, _, _ := s.Manifest(ctx, target.Site)
	if err := h.prepare(ctx, s); err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if m2, _, _ := s.Manifest(ctx, target.Site); m2.Version != m1.Version {
		t.Errorf("manifest version %d after the second run, want %d", m2.Version, m1.Version)
	}
}

// Without the site, nothing is pushed: the site is added with `co site add` first.
func TestPrepareHarnessUnknownSite(t *testing.T) {
	reg := &registry{}
	h, _, _ := newHarness(t, reg)
	err := h.prepare(context.Background(), store.NewMemory())
	if !errors.Is(err, deploy.ErrUnknownSite) {
		t.Errorf("prepare for a missing site: %v, want ErrUnknownSite", err)
	}
	if len(reg.repos) != 0 {
		t.Errorf("pushed to %d repositories for a missing site, want none", len(reg.repos))
	}
}

// The tool pushes only to the harness registry, through the address given.
func TestOpenMapsHarnessRegistry(t *testing.T) {
	open := openThrough("localhost:5000")
	got, err := open(context.Background(), "registry:5000/ieo-harness/hello")
	if err != nil {
		t.Fatal(err)
	}
	repo := got.(*remote.Repository)
	if repo.Reference.Registry != "localhost:5000" || repo.Reference.Repository != "ieo-harness/hello" || !repo.PlainHTTP {
		t.Errorf("opened %+v (plain HTTP %v), want localhost:5000/ieo-harness/hello over plain HTTP", repo.Reference, repo.PlainHTTP)
	}
	if _, err := open(context.Background(), "registry.example/ieo-harness/hello"); err == nil {
		t.Error("opened a repository outside the harness registry")
	}
}
