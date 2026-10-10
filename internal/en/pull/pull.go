// Package pull fetches Margo Compose Archives from an OCI registry for the EN (SPEC §5.1, §8.9
// step 4.2). It owns the layout of the archive's artifact, the pull limits and the check of the
// layer against its digest; what the archive holds is package archive's work.
package pull

import (
	"context"
	"errors"
	"time"

	"oras.land/oras-go/v2"
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
type Layer struct{}

// Read reads the archive.
func (l *Layer) Read(p []byte) (int, error) {
	return 0, errors.New("pull: Layer.Read is not implemented")
}

// Close deletes the file the layer was kept in. The caller closes every Layer it is given.
func (l *Layer) Close() error {
	return errors.New("pull: Layer.Close is not implemented")
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
	return nil, errors.New("pull: ComposeArchive is not implemented")
}
