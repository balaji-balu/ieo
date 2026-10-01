// Package ocitest is the fake OCI registry for Core Conformance tests (SPEC §17, G-F4): an
// in-memory registry with helpers that push Margo packages, so tests need no network.
package ocitest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	mu    sync.Mutex
	repos map[string]*memory.Store
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{repos: map[string]*memory.Store{}}
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
	return repo, nil
}

// Layer is one file of a pushed artifact. Title, when set, becomes the
// org.opencontainers.image.title annotation (the file name).
type Layer struct {
	MediaType string
	Title     string
	Data      []byte
}

// Push stores an OCI image manifest with the given artifactType, an empty config and the layers,
// and tags it in repository, creating the repository if needed. A tag that exists is moved, as on
// a real registry. It returns the manifest descriptor.
func (r *Registry) Push(t testing.TB, repository, tag, artifactType string, layers ...Layer) ocispec.Descriptor {
	t.Helper()
	ctx := context.Background()
	repo := r.repo(repository)
	var descs []ocispec.Descriptor
	for _, l := range layers {
		d := content.NewDescriptorFromBytes(l.MediaType, l.Data)
		if l.Title != "" {
			d.Annotations = map[string]string{ocispec.AnnotationTitle: l.Title}
		}
		if err := repo.Push(ctx, d, bytes.NewReader(l.Data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
			t.Fatalf("push layer %s to %s: %v", l.Title, repository, err)
		}
		descs = append(descs, d)
	}
	m, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, artifactType,
		oras.PackManifestOptions{Layers: descs})
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
	return r.Push(t, repository, tag, contract.AppPackageArtifactType,
		Layer{MediaType: contract.AppDescriptionMediaType, Title: "margo.yaml", Data: description},
		Layer{MediaType: "application/vnd.margo.app.licenseFile.v1+markdown", Title: "resources/license.md",
			Data: []byte("# License\n")},
	)
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
