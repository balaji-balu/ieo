package contract

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"slices"
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
