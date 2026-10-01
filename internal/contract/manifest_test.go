package contract_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// SPEC §17.2: "Identical manifest content yields identical bytes and identical ETag (RFC 8785)."
//
// The deployments' order in the Go value doesn't matter: the encoding orders them by ID.
func TestSpec_17_2_IdenticalManifestContentYieldsIdenticalBytesAndETag(t *testing.T) {
	a := contract.DeploymentRef{DeploymentID: uuid.MustParse(testUUID2), Digest: testDigest, SizeBytes: 12,
		URL: contract.DeploymentURL(uuid.MustParse(testUUID2), testDigest)}
	b := contract.DeploymentRef{DeploymentID: uuid.MustParse(testUUID), Digest: testDigest, SizeBytes: 7,
		URL: contract.DeploymentURL(uuid.MustParse(testUUID), testDigest)}
	bundle := &contract.BundleRef{MediaType: contract.BundleMediaType, Digest: testDigest, SizeBytes: 300,
		URL: contract.BundleURL(testDigest)}

	first, err := contract.EncodeStateManifest(contract.StateManifest{ManifestVersion: 4, Deployments: []contract.DeploymentRef{a, b}, Bundle: bundle})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	second, err := contract.EncodeStateManifest(contract.StateManifest{ManifestVersion: 4, Deployments: []contract.DeploymentRef{b, a}, Bundle: bundle})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := `{"bundle":{"digest":"` + testDigest + `","mediaType":"application/vnd.margo.bundle.v1+tar+gzip",` +
		`"sizeBytes":300,"url":"/api/v1/bundles/` + testDigest + `"},"deployments":[` +
		`{"deploymentId":"` + testUUID2 + `","digest":"` + testDigest + `","sizeBytes":12,"url":"/api/v1/deployments/` + testUUID2 + `/` + testDigest + `"},` +
		`{"deploymentId":"` + testUUID + `","digest":"` + testDigest + `","sizeBytes":7,"url":"/api/v1/deployments/` + testUUID + `/` + testDigest + `"}` +
		`],"manifestVersion":4}`
	if string(first) != want {
		t.Errorf("encoded manifest:\n got %s\nwant %s", first, want)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("same content, different bytes:\n%s\n%s", first, second)
	}
	if got, wantTag := contract.ETag(first), `"`+string(contract.DigestOf(first))+`"`; got != wantTag || contract.ETag(second) != got {
		t.Errorf("ETag = %s, want %s for both", got, wantTag)
	}
}

// TestEncodeBundle checks the archive layout of ADR 0012: one `<deploymentId>.yaml` per
// deployment, ordered by ID, with fixed metadata, so equal files give equal bytes.
func TestEncodeBundle(t *testing.T) {
	files := []contract.BundleFile{
		{DeploymentID: uuid.MustParse(testUUID), YAML: []byte("id: b\n")},
		{DeploymentID: uuid.MustParse(testUUID2), YAML: []byte("id: a\n")},
	}
	first, err := contract.EncodeBundle(files)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	second, err := contract.EncodeBundle([]contract.BundleFile{files[1], files[0]})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same files in another order give different bytes")
	}

	zr, err := gzip.NewReader(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if zr.Name != "" || zr.Comment != "" || !zr.ModTime.IsZero() {
		t.Errorf("gzip header: name %q, comment %q, mtime %v; want none", zr.Name, zr.Comment, zr.ModTime)
	}
	tr := tar.NewReader(zr)
	want := []struct{ name, data string }{{testUUID2 + ".yaml", "id: a\n"}, {testUUID + ".yaml", "id: b\n"}}
	for i := 0; ; i++ {
		h, err := tr.Next()
		if err == io.EOF {
			if i != len(want) {
				t.Errorf("%d entries, want %d", i, len(want))
			}
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if i >= len(want) {
			t.Fatalf("unexpected entry %q", h.Name)
		}
		data, _ := io.ReadAll(tr)
		if h.Name != want[i].name || string(data) != want[i].data || h.Typeflag != tar.TypeReg ||
			h.Mode != 0o644 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || !h.ModTime.Equal(time.Unix(0, 0)) {
			t.Errorf("entry %d: %+v %q; want %s %q, regular, 0644, root, epoch", i, h, data, want[i].name, want[i].data)
		}
	}
}
