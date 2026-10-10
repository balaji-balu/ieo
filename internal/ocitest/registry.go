// Package ocitest is the fake OCI registry for Core Conformance tests (SPEC §17, G-F4): an
// in-memory registry with helpers that push Margo packages, so tests need no network.
package ocitest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/errdef"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Registry is an in-memory OCI registry of named repositories. It is safe for concurrent use.
type Registry struct {
	mu       sync.Mutex
	repos    map[string]*memory.Store
	tampered map[blobKey][]byte
	stalled  map[string]bool
	served   map[string]int64
}

// blobKey names one blob of one repository.
type blobKey struct {
	repository string
	digest     string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		repos:    map[string]*memory.Store{},
		tampered: map[blobKey][]byte{},
		stalled:  map[string]bool{},
		served:   map[string]int64{},
	}
}

// Open returns the repository named repository, for code under test to read from. An unknown
// repository is an error wrapping errdef.ErrNotFound, as a real registry reports it.
func (r *Registry) Open(_ context.Context, repository string) (oras.ReadOnlyTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, ok := r.repos[repository]
	if !ok {
		return nil, fmt.Errorf("repository %q: %w", repository, errdef.ErrNotFound)
	}
	return &target{reg: r, repository: repository, store: repo}, nil
}

// Tamper makes the registry serve data in place of the blob d of repository, as a registry that
// is broken or hostile would. Descriptors, and the manifest that lists d, still give d's digest
// and size.
func (r *Registry) Tamper(repository string, d ocispec.Descriptor, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tampered[blobKey{repository, d.Digest.String()}] = data
}

// Stall makes every later request to repository block until the request's context ends, as an
// unreachable registry would.
func (r *Registry) Stall(repository string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stalled[repository] = true
}

// BytesServed returns how many bytes of manifests and blobs code under test has read from
// repository so far.
func (r *Registry) BytesServed(repository string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.served[repository]
}

// target is one repository as code under test reads it: the stored content, changed by Tamper and
// Stall, with the bytes read counted.
type target struct {
	reg        *Registry
	repository string
	store      *memory.Store
}

// wait blocks until ctx ends if the repository is stalled.
func (t *target) wait(ctx context.Context) error {
	t.reg.mu.Lock()
	stalled := t.reg.stalled[t.repository]
	t.reg.mu.Unlock()
	if !stalled {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (t *target) Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error) {
	if err := t.wait(ctx); err != nil {
		return ocispec.Descriptor{}, err
	}
	return t.store.Resolve(ctx, reference)
}

func (t *target) Exists(ctx context.Context, d ocispec.Descriptor) (bool, error) {
	if err := t.wait(ctx); err != nil {
		return false, err
	}
	return t.store.Exists(ctx, d)
}

func (t *target) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	if err := t.wait(ctx); err != nil {
		return nil, err
	}
	rc, err := t.store.Fetch(ctx, d)
	if err != nil {
		return nil, err
	}
	t.reg.mu.Lock()
	data, tampered := t.reg.tampered[blobKey{t.repository, d.Digest.String()}]
	t.reg.mu.Unlock()
	if tampered {
		_ = rc.Close() // an in-memory reader: nothing to lose
		rc = io.NopCloser(bytes.NewReader(data))
	}
	return &countedBody{ReadCloser: rc, t: t}, nil
}

// countedBody adds what is read from it to the repository's BytesServed.
type countedBody struct {
	io.ReadCloser
	t *target
}

func (c *countedBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.t.reg.mu.Lock()
	c.t.reg.served[c.t.repository] += int64(n)
	c.t.reg.mu.Unlock()
	return n, err
}

// Layer is one file of a pushed artifact. Title, when set, becomes the
// org.opencontainers.image.title annotation (the file name).
type Layer struct {
	MediaType string
	Title     string
	Data      []byte
}

// Artifact is an OCI image manifest to push: its artifactType, its config (empty when nil) and its
// layers.
type Artifact struct {
	ArtifactType string
	Config       *Layer
	Layers       []Layer
}

// Push stores a as an OCI image manifest and tags it in repository, creating the repository if
// needed. A tag that exists is moved, as on a real registry. It returns the manifest descriptor.
func (r *Registry) Push(t testing.TB, repository, tag string, a Artifact) ocispec.Descriptor {
	t.Helper()
	ctx := context.Background()
	repo := r.repo(repository)
	push := func(l Layer) ocispec.Descriptor {
		d := content.NewDescriptorFromBytes(l.MediaType, l.Data)
		if l.Title != "" {
			d.Annotations = map[string]string{ocispec.AnnotationTitle: l.Title}
		}
		if err := repo.Push(ctx, d, bytes.NewReader(l.Data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
			t.Fatalf("push blob %s to %s: %v", l.MediaType, repository, err)
		}
		return d
	}
	opts := oras.PackManifestOptions{}
	if a.Config != nil {
		config := push(*a.Config)
		opts.ConfigDescriptor = &config
	}
	for _, l := range a.Layers {
		opts.Layers = append(opts.Layers, push(l))
	}
	m, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, a.ArtifactType, opts)
	if err != nil {
		t.Fatalf("pack manifest for %s:%s: %v", repository, tag, err)
	}
	if err := repo.Tag(ctx, m, tag); err != nil {
		t.Fatalf("tag %s:%s: %v", repository, tag, err)
	}
	return m
}

// PushApp pushes a Margo application package (SPEC §5.1): the Application Description as
// margo.yaml plus one resource file, tagged tag in repository.
func (r *Registry) PushApp(t testing.TB, repository, tag string, description []byte) ocispec.Descriptor {
	t.Helper()
	return r.Push(t, repository, tag, Artifact{ArtifactType: contract.AppPackageArtifactType, Layers: []Layer{
		{MediaType: contract.AppDescriptionMediaType, Title: "margo.yaml", Data: description},
		{MediaType: "application/vnd.margo.app.licenseFile.v1+markdown", Title: "resources/license.md",
			Data: []byte("# License\n")},
	}})
}

// PushComposeArchive pushes a Margo Compose Archive (SPEC §5.1) holding the gzip tar archive,
// tagged tag in repository. It returns the descriptors of the manifest and of the archive layer.
func (r *Registry) PushComposeArchive(t testing.TB, repository, tag string, archive []byte) (manifest, layer ocispec.Descriptor) {
	t.Helper()
	manifest = r.Push(t, repository, tag, Artifact{ArtifactType: contract.ComposeArchiveArtifactType, Layers: []Layer{
		{MediaType: contract.ComposeArchiveMediaType, Data: archive},
	}})
	return manifest, content.NewDescriptorFromBytes(contract.ComposeArchiveMediaType, archive)
}

func (r *Registry) repo(repository string) *memory.Store {
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, ok := r.repos[repository]
	if !ok {
		repo = memory.New()
		r.repos[repository] = repo
	}
	return repo
}
