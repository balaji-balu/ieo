// Package pull fetches Margo Compose Archives from an OCI registry for the EN (SPEC §5.1, §8.9
// step 4.2). It owns the layout of the archive's artifact, the pull limits and the check of the
// layer against its digest; what the archive holds is package archive's work.
package pull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"

	"github.com/balaji-balu/ieo/internal/contract"
)

// ErrDigestMismatch is returned, wrapped, when the bytes a registry serves for the archive layer
// are not the ones its manifest names. The EN reports it as IEO-DIGEST-MISMATCH; every other
// error of a pull is IEO-PULL-FAILED (SPEC §8.9, §10).
var ErrDigestMismatch = errors.New("compose archive layer does not match its digest")

// OpenRepository returns the OCI repository named repository (e.g. `registry.example/org/app`,
// without the `oci://`), for reading.
type OpenRepository func(ctx context.Context, repository string) (oras.ReadOnlyTarget, error)

// Limits bound one pull (SPEC §8.9, §6.3). A zero limit admits nothing.
type Limits struct {
	// MaxBytes is the most the EN reads of any one registry response, manifest or layer
	// (en.pull.max_bytes).
	MaxBytes int64
	// Timeout is the longest one pull may take, from opening the repository to the verified
	// layer (en.pull.timeout).
	Timeout time.Duration
}

// Puller pulls Compose archives. It is safe for concurrent use.
type Puller struct {
	open     OpenRepository
	spoolDir string
	limits   Limits
}

// New returns a Puller that reads registries through open and keeps each layer in spoolDir
// (<en.data_dir>/pull, SPEC §9.1) until its Layer is closed. spoolDir is created when first needed.
func New(open OpenRepository, spoolDir string, l Limits) *Puller {
	return &Puller{open: open, spoolDir: spoolDir, limits: l}
}

// Layer is a pulled Compose archive, a gzip tar, whose bytes have been checked against the digest
// in its manifest. It reads from the start of the archive.
type Layer struct {
	f *os.File
}

// Read reads the archive.
func (l *Layer) Read(p []byte) (int, error) {
	return l.f.Read(p)
}

// Close deletes the file the layer was kept in. The caller closes every Layer it is given.
func (l *Layer) Close() error {
	// Closed first: Windows does not delete an open file.
	return errors.Join(l.f.Close(), os.Remove(l.f.Name()))
}

// ComposeArchive pulls the Compose archive of a component: repository is the component's
// `repository`, an `oci://` reference, and revision its `revision`, the tag (SPEC §4.2). The
// layer is read into the spool directory and checked against its digest before it is returned,
// so nothing unverified reaches the caller (SPEC §8.9).
//
// On error no Layer is returned and no file is left. An error wraps ErrDigestMismatch if the
// layer's bytes are not what the manifest names. Every other error is a failed pull: the registry
// can't be reached, the tag does not exist or does not hold a Margo Compose Archive (SPEC §5.1), a
// response is larger than l.MaxBytes, or the pull outlasts l.Timeout; then the error wraps
// context.DeadlineExceeded. No more than l.MaxBytes of any response is read.
func (p *Puller) ComposeArchive(ctx context.Context, repository, revision string) (*Layer, error) {
	layer, err := p.pull(ctx, repository, revision)
	if err != nil {
		return nil, fmt.Errorf("pull compose archive %s at %s: %w", repository, revision, err)
	}
	return layer, nil
}

func (p *Puller) pull(ctx context.Context, repository, revision string) (*Layer, error) {
	name, ok := strings.CutPrefix(repository, "oci://")
	if !ok || name == "" {
		return nil, errors.New("repository is not an oci:// reference")
	}
	ctx, cancel := context.WithTimeout(ctx, p.limits.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil { // a registry client need not look before its first request
		return nil, err
	}
	if p.limits.MaxBytes <= 0 {
		// oras reads a limit of 0 as its own default.
		return nil, fmt.Errorf("the pull limit is %d bytes", p.limits.MaxBytes)
	}
	repo, err := p.open(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	// FetchBytes refuses a manifest over MaxBytes before reading it, and checks it against the
	// digest the tag resolves to.
	desc, manifest, err := oras.FetchBytes(ctx, repo, revision, oras.FetchBytesOptions{MaxBytes: p.limits.MaxBytes})
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	layer, err := archiveLayer(desc, manifest)
	if err != nil {
		return nil, fmt.Errorf("not a Margo Compose Archive: %w", err)
	}
	if layer.Size < 0 || layer.Size > p.limits.MaxBytes {
		return nil, fmt.Errorf("archive layer is %d bytes, limit %d", layer.Size, p.limits.MaxBytes)
	}
	return p.spool(ctx, repo, layer)
}

// archiveLayer returns the layer holding the archive, if the manifest is a Margo Compose Archive
// (SPEC §5.1): the artifactType, and exactly one layer, of the archive's media type.
func archiveLayer(desc ocispec.Descriptor, manifest []byte) (ocispec.Descriptor, error) {
	if desc.MediaType != ocispec.MediaTypeImageManifest {
		return ocispec.Descriptor{}, fmt.Errorf("manifest media type %q, want %q", desc.MediaType, ocispec.MediaTypeImageManifest)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("decode manifest: %w", err)
	}
	if m.ArtifactType != contract.ComposeArchiveArtifactType {
		return ocispec.Descriptor{}, fmt.Errorf("manifest artifactType %q, want %q", m.ArtifactType, contract.ComposeArchiveArtifactType)
	}
	if len(m.Layers) != 1 {
		return ocispec.Descriptor{}, fmt.Errorf("%d layers, want exactly one", len(m.Layers))
	}
	if got := m.Layers[0].MediaType; got != contract.ComposeArchiveMediaType {
		return ocispec.Descriptor{}, fmt.Errorf("layer media type %q, want %q", got, contract.ComposeArchiveMediaType)
	}
	return m.Layers[0], nil
}

// spool reads the layer into a new file of the spool directory and returns it once its bytes
// match the layer's digest. It reads layer.Size bytes and no more, whatever the registry sends.
func (p *Puller) spool(ctx context.Context, repo oras.ReadOnlyTarget, layer ocispec.Descriptor) (_ *Layer, err error) {
	if err := layer.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("archive layer digest %q: %w", layer.Digest, err)
	}
	if err := os.MkdirAll(p.spoolDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(p.spoolDir, "layer-*") // mode 0600
	if err != nil {
		return nil, err
	}
	spooled := &Layer{f: f}
	defer func() {
		if err != nil {
			_ = spooled.Close() // the pull has failed; its error is the one to report
		}
	}()
	body, err := repo.Fetch(ctx, layer)
	if err != nil {
		return nil, fmt.Errorf("fetch archive layer %s: %w", layer.Digest, err)
	}
	defer func() { _ = body.Close() }() // read-only: nothing to lose
	verifier := layer.Digest.Verifier()
	n, err := io.Copy(io.MultiWriter(f, verifier), io.LimitReader(untilDone{ctx, body}, layer.Size))
	if err != nil {
		return nil, fmt.Errorf("fetch archive layer %s: %w", layer.Digest, err)
	}
	if n != layer.Size || !verifier.Verified() {
		return nil, fmt.Errorf("%w: %s, %d bytes; read %d bytes", ErrDigestMismatch, layer.Digest, layer.Size, n)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return spooled, nil
}

// untilDone reads r until ctx ends, for registry clients that do not watch the context while a
// response is read.
type untilDone struct {
	ctx context.Context
	r   io.Reader
}

func (u untilDone) Read(p []byte) (int, error) {
	if err := u.ctx.Err(); err != nil {
		return 0, err
	}
	return u.r.Read(p)
}
