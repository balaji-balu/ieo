package pull_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/archive/archivetest"
	"github.com/balaji-balu/ieo/internal/en/pull"
	"github.com/balaji-balu/ieo/internal/ocitest"
)

const (
	repository  = "registry.example/acme/web" // as the registry names it
	reference   = "oci://" + repository       // a component's `repository`
	revision    = "1.2.3_build.5"             // a component's `revision`: the tag (SPEC §4.2)
	description = "apiVersion: margo.org/v1-alpha1\nkind: ApplicationDescription\n"
)

var roomy = pull.Limits{MaxBytes: 1 << 20, Timeout: time.Minute}

type fixture struct {
	reg   *ocitest.Registry
	spool string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return fixture{reg: ocitest.NewRegistry(), spool: filepath.Join(t.TempDir(), "pull")}
}

func (f fixture) puller(l pull.Limits) *pull.Puller {
	return pull.New(f.reg.Open, f.spool, l)
}

// spooled returns the names of the files in the spool directory.
func (f fixture) spooled(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.spool)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read spool directory: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// assertFailedPull checks a pull that must fail without a Layer and without leaving a file.
func (f fixture) assertFailedPull(t *testing.T, layer *pull.Layer, err error) {
	t.Helper()
	if err == nil {
		_ = layer.Close()
		t.Fatal("ComposeArchive succeeded, want an error")
	}
	if layer != nil {
		t.Error("ComposeArchive returned a Layer with its error")
	}
	if left := f.spooled(t); len(left) != 0 {
		t.Errorf("files left in the spool directory: %v", left)
	}
}

// composeArchive returns a valid Compose archive of a few KiB that gzip cannot shrink, so its
// layer is larger than its manifest.
func composeArchive(t *testing.T) []byte {
	t.Helper()
	noise := make([]byte, 4096)
	if _, err := rand.New(rand.NewSource(1)).Read(noise); err != nil {
		t.Fatal(err)
	}
	return archivetest.Build(t, append(archivetest.Valid("app", "services: {}\n"), archivetest.File("app/data.bin", string(noise)))...)
}

// A pull returns the archive layer's bytes, kept in the spool directory until the Layer is closed
// (SPEC §8.9 step 4.2, §9.1).
func TestComposeArchivePulled(t *testing.T) {
	f := newFixture(t)
	want := composeArchive(t)
	f.reg.PushComposeArchive(t, repository, revision, want)

	layer, err := f.puller(roomy).ComposeArchive(context.Background(), reference, revision)
	if err != nil {
		t.Fatalf("ComposeArchive: %v", err)
	}
	got, err := io.ReadAll(layer)
	if err != nil {
		t.Fatalf("read layer: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("layer holds %d bytes that differ from the %d pushed", len(got), len(want))
	}
	if n := len(f.spooled(t)); n != 1 {
		t.Errorf("%d files in the spool directory while the layer is open, want 1", n)
	}
	if err := layer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if left := f.spooled(t); len(left) != 0 {
		t.Errorf("files left in the spool directory after Close: %v", left)
	}
}

// SPEC §17.6: "A layer whose bytes do not match its digest is not extracted."
func TestSpec_17_6_LayerDigestMismatch(t *testing.T) {
	good := composeArchive(t)
	flipped := bytes.Clone(good)
	flipped[len(flipped)/2] ^= 0xff
	// A valid archive, so only the digest check stands between it and extraction.
	other := archivetest.Build(t, archivetest.Valid("evil", "services: {}\n")...)
	cases := []struct {
		name   string
		served []byte
	}{
		{"one byte changed", flipped},
		{"cut short", good[:len(good)-1]},
		{"another archive, shorter", other},
		{"another archive, longer", append(bytes.Clone(other), good...)},
		{"nothing", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			_, desc := f.reg.PushComposeArchive(t, repository, revision, good)
			f.reg.Tamper(repository, desc, c.served)

			layer, err := f.puller(roomy).ComposeArchive(context.Background(), reference, revision)
			f.assertFailedPull(t, layer, err)
			if !errors.Is(err, pull.ErrDigestMismatch) {
				t.Errorf("error = %v, want one wrapping pull.ErrDigestMismatch", err)
			}
		})
	}
}

// SPEC §17.6: "A registry response larger than `en.pull.max_bytes`, or a pull that outlasts
// `en.pull.timeout`, fails the component with `IEO-PULL-FAILED`, and the EN reads no more than
// the limit." The error code is the EN's mapping of every pull error but ErrDigestMismatch
// (roadmap D4).
func TestSpec_17_6_PullLimits(t *testing.T) {
	archive := composeArchive(t)
	size := int64(len(archive))

	t.Run("layer at the limit", func(t *testing.T) {
		f := newFixture(t)
		f.reg.PushComposeArchive(t, repository, revision, archive)
		layer, err := f.puller(pull.Limits{MaxBytes: size, Timeout: time.Minute}).ComposeArchive(context.Background(), reference, revision)
		if err != nil {
			t.Fatalf("ComposeArchive: %v", err)
		}
		if err := layer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	t.Run("layer one byte over", func(t *testing.T) {
		f := newFixture(t)
		manifest, _ := f.reg.PushComposeArchive(t, repository, revision, archive)
		layer, err := f.puller(pull.Limits{MaxBytes: size - 1, Timeout: time.Minute}).ComposeArchive(context.Background(), reference, revision)
		f.assertFailedPull(t, layer, err)
		if errors.Is(err, pull.ErrDigestMismatch) {
			t.Errorf("error %v wraps ErrDigestMismatch; the layer was never read", err)
		}
		if got := f.reg.BytesServed(repository); got != manifest.Size {
			t.Errorf("read %d bytes from the registry, want only the manifest's %d", got, manifest.Size)
		}
	})
	t.Run("manifest over", func(t *testing.T) {
		f := newFixture(t)
		manifest, _ := f.reg.PushComposeArchive(t, repository, revision, archive)
		layer, err := f.puller(pull.Limits{MaxBytes: manifest.Size - 1, Timeout: time.Minute}).ComposeArchive(context.Background(), reference, revision)
		f.assertFailedPull(t, layer, err)
		if got := f.reg.BytesServed(repository); got != 0 {
			t.Errorf("read %d bytes from the registry, want none", got)
		}
	})
	// The manifest gives the layer's size; a registry that sends more is read only that far.
	t.Run("registry sends more than the manifest says", func(t *testing.T) {
		f := newFixture(t)
		manifest, desc := f.reg.PushComposeArchive(t, repository, revision, archive)
		f.reg.Tamper(repository, desc, bytes.Repeat([]byte{0}, 10*len(archive)))
		layer, err := f.puller(pull.Limits{MaxBytes: size, Timeout: time.Minute}).ComposeArchive(context.Background(), reference, revision)
		f.assertFailedPull(t, layer, err)
		if !errors.Is(err, pull.ErrDigestMismatch) {
			t.Errorf("error = %v, want one wrapping pull.ErrDigestMismatch", err)
		}
		if got := f.reg.BytesServed(repository) - manifest.Size; got > size {
			t.Errorf("read %d bytes of the layer, limit %d", got, size)
		}
	})
	t.Run("pull outlasts the timeout", func(t *testing.T) {
		f := newFixture(t)
		f.reg.PushComposeArchive(t, repository, revision, archive)
		f.reg.Stall(repository)
		layer, err := f.puller(pull.Limits{MaxBytes: size, Timeout: 20 * time.Millisecond}).ComposeArchive(context.Background(), reference, revision)
		f.assertFailedPull(t, layer, err)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want one wrapping context.DeadlineExceeded", err)
		}
	})
}

// SPEC §17.6: "A tag that holds anything but a Margo Compose Archive (§5.1) fails the component
// with `IEO-PULL-FAILED`." The layer is not read.
func TestSpec_17_6_NotAComposeArchive(t *testing.T) {
	archive := composeArchive(t)
	layer := ocitest.Layer{MediaType: contract.ComposeArchiveMediaType, Data: archive}
	cases := []struct {
		name string
		push func(t *testing.T, reg *ocitest.Registry) ocispec.Descriptor
	}{
		{"an application package", func(t *testing.T, reg *ocitest.Registry) ocispec.Descriptor {
			return reg.PushApp(t, repository, revision, []byte(description))
		}},
		{"another artifactType", func(t *testing.T, reg *ocitest.Registry) ocispec.Descriptor {
			return reg.Push(t, repository, revision, ocitest.Artifact{
				ArtifactType: "application/vnd.example.other+json", Layers: []ocitest.Layer{layer}})
		}},
		{"layer of another media type", func(t *testing.T, reg *ocitest.Registry) ocispec.Descriptor {
			return reg.Push(t, repository, revision, ocitest.Artifact{
				ArtifactType: contract.ComposeArchiveArtifactType,
				Layers:       []ocitest.Layer{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Data: archive}}})
		}},
		{"two archive layers", func(t *testing.T, reg *ocitest.Registry) ocispec.Descriptor {
			return reg.Push(t, repository, revision, ocitest.Artifact{
				ArtifactType: contract.ComposeArchiveArtifactType,
				Layers:       []ocitest.Layer{layer, {MediaType: contract.ComposeArchiveMediaType, Data: []byte("second")}}})
		}},
		{"an archive layer and another layer", func(t *testing.T, reg *ocitest.Registry) ocispec.Descriptor {
			return reg.Push(t, repository, revision, ocitest.Artifact{
				ArtifactType: contract.ComposeArchiveArtifactType,
				Layers:       []ocitest.Layer{layer, {MediaType: "text/plain", Data: []byte("notes")}}})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			manifest := c.push(t, f.reg)

			got, err := f.puller(roomy).ComposeArchive(context.Background(), reference, revision)
			f.assertFailedPull(t, got, err)
			if errors.Is(err, pull.ErrDigestMismatch) {
				t.Errorf("error %v wraps ErrDigestMismatch; no layer was read", err)
			}
			if served := f.reg.BytesServed(repository); served != manifest.Size {
				t.Errorf("read %d bytes from the registry, want only the manifest's %d", served, manifest.Size)
			}
		})
	}
}

// A missing artifact, and a `repository` that is not an oci:// reference, fail the pull (SPEC
// §8.9, §10).
func TestComposeArchiveMissing(t *testing.T) {
	cases := []struct {
		name, reference, revision string
	}{
		{"unknown tag", reference, "9.9.9"},
		{"unknown repository", "oci://registry.example/acme/other", revision},
		{"no oci:// scheme", repository, revision},
		{"another scheme", "https://" + repository, revision},
		{"no repository", "oci://", revision},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.reg.PushComposeArchive(t, repository, revision, composeArchive(t))

			layer, err := f.puller(roomy).ComposeArchive(context.Background(), c.reference, c.revision)
			f.assertFailedPull(t, layer, err)
			if errors.Is(err, pull.ErrDigestMismatch) {
				t.Errorf("error %v wraps ErrDigestMismatch", err)
			}
		})
	}
}

// A zero limit admits nothing (G-B5): it is not read as "no limit".
func TestComposeArchiveZeroLimits(t *testing.T) {
	for name, l := range map[string]pull.Limits{
		"no bytes": {MaxBytes: 0, Timeout: time.Minute},
		"no time":  {MaxBytes: 1 << 20, Timeout: 0},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.reg.PushComposeArchive(t, repository, revision, composeArchive(t))
			layer, err := f.puller(l).ComposeArchive(context.Background(), reference, revision)
			f.assertFailedPull(t, layer, err)
			if got := f.reg.BytesServed(repository); got != 0 {
				t.Errorf("read %d bytes from the registry, want none", got)
			}
		})
	}
}

// The caller's context ending stops the pull with that context's error, not a timeout.
func TestComposeArchiveContextCancelled(t *testing.T) {
	f := newFixture(t)
	f.reg.PushComposeArchive(t, repository, revision, composeArchive(t))
	f.reg.Stall(repository)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	layer, err := f.puller(roomy).ComposeArchive(ctx, reference, revision)
	f.assertFailedPull(t, layer, err)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want one wrapping context.Canceled", err)
	}
}
