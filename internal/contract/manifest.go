package contract

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gowebpki/jcs"
)

// EncodeStateManifest returns the body of GET /api/v1/deployments for m (SPEC §8.1.3): its
// deployments ordered by deploymentId, serialized with RFC 8785, so equal content always gives
// equal bytes.
func EncodeStateManifest(m StateManifest) ([]byte, error) {
	m.Deployments = slices.SortedFunc(slices.Values(m.Deployments), func(a, b DeploymentRef) int {
		return cmp.Compare(a.DeploymentID.String(), b.DeploymentID.String())
	})
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode state manifest: %w", err)
	}
	b, err = jcs.Transform(b)
	if err != nil {
		return nil, fmt.Errorf("encode state manifest: canonicalize: %w", err)
	}
	return b, nil
}

// ETag returns the HTTP entity tag of a response body: its quoted digest (SPEC §8.1.3).
func ETag(body []byte) string { return `"` + string(DigestOf(body)) + `"` }

// DeploymentURL is the path of one deployment's YAML in the Margo API (SPEC §11.1).
func DeploymentURL(id uuid.UUID, digest Digest) string {
	return "/api/v1/deployments/" + id.String() + "/" + string(digest)
}

// BundleURL is the path of a bundle in the Margo API (SPEC §11.1).
func BundleURL(digest Digest) string { return "/api/v1/bundles/" + string(digest) }

// ErrInvalidBundle is returned, wrapped with the reason, for a bundle that is not laid out as ADR
// 0012 says.
var ErrInvalidBundle = errors.New("invalid bundle")

// BundleFile is one deployment's YAML in a bundle.
type BundleFile struct {
	DeploymentID uuid.UUID
	YAML         []byte
}

// EncodeBundle returns the bundle holding files (SPEC §4.1.6, ADR 0012): a gzip tar with one
// `<deploymentId>.yaml` per file, built so that the same files always give the same bytes, in any
// order.
func EncodeBundle(files []BundleFile) ([]byte, error) {
	files = slices.SortedFunc(slices.Values(files), func(a, b BundleFile) int {
		return cmp.Compare(a.DeploymentID.String(), b.DeploymentID.String())
	})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf) // zero header: no name, comment or modification time
	tw := tar.NewWriter(zw)
	for _, f := range files {
		h := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     f.DeploymentID.String() + ".yaml",
			Size:     int64(len(f.YAML)),
			Mode:     0o644,
			ModTime:  time.Unix(0, 0),
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, fmt.Errorf("encode bundle: %s: %w", h.Name, err)
		}
		if _, err := tw.Write(f.YAML); err != nil {
			return nil, fmt.Errorf("encode bundle: %s: %w", h.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeBundle returns the files of a bundle, in archive order: the inverse of EncodeBundle (SPEC
// §4.1.6, ADR 0012). The archive must hold only regular files at its root, each named
// `<deploymentId>.yaml` with the lowercase UUID, one per deployment; anything else wraps
// ErrInvalidBundle. The caller verifies the bundle's digest before calling it, and matches the
// files against the manifest after (SPEC §8.2 step 5, §15.3).
func DecodeBundle(b []byte) ([]BundleFile, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBundle, err)
	}
	tr := tar.NewReader(zr)
	var files []BundleFile
	seen := map[uuid.UUID]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidBundle, err)
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%w: entry %q is not a regular file", ErrInvalidBundle, h.Name)
		}
		id, err := bundleEntryID(h.Name)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, fmt.Errorf("%w: entry %q appears more than once", ErrInvalidBundle, h.Name)
		}
		seen[id] = true
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("%w: entry %q: %w", ErrInvalidBundle, h.Name, err)
		}
		files = append(files, BundleFile{DeploymentID: id, YAML: data})
	}
	// Read the gzip stream to its end: that verifies its checksum and rejects trailing data.
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBundle, err)
	}
	return files, nil
}

// bundleEntryID returns the deployment ID an entry name stands for: the name must be exactly
// `<lowercase deploymentId>.yaml`, so each deployment has one possible name.
func bundleEntryID(name string) (uuid.UUID, error) {
	stem, ok := strings.CutSuffix(name, ".yaml")
	id, err := uuid.Parse(stem)
	if !ok || err != nil || id.String() != stem {
		return uuid.UUID{}, fmt.Errorf("%w: entry %q is not named <lowercase deploymentId>.yaml", ErrInvalidBundle, name)
	}
	return id, nil
}
