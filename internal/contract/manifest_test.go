package contract_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
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

func TestDecodeStateManifest(t *testing.T) {
	idA, idB := uuid.MustParse(testUUID), uuid.MustParse(testUUID2)
	for _, m := range []contract.StateManifest{
		{ManifestVersion: 1}, // no deployments: `bundle: null`
		{ManifestVersion: 7, Deployments: []contract.DeploymentRef{
			{DeploymentID: idA, Digest: testDigest, SizeBytes: 7, URL: contract.DeploymentURL(idA, testDigest)},
			{DeploymentID: idB, Digest: testDigest, URL: contract.DeploymentURL(idB, testDigest)},
		}, Bundle: &contract.BundleRef{MediaType: contract.BundleMediaType, Digest: testDigest, URL: contract.BundleURL(testDigest)}},
	} {
		body, err := contract.EncodeStateManifest(m)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		got, err := contract.DecodeStateManifest(body)
		if err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		again, err := contract.EncodeStateManifest(got)
		if err != nil || !bytes.Equal(again, body) {
			t.Errorf("round trip changed the manifest:\n got %s (err %v)\nwant %s", again, err, body)
		}
		if got.ManifestVersion != m.ManifestVersion || len(got.Deployments) != len(m.Deployments) || (got.Bundle == nil) != (m.Bundle == nil) {
			t.Errorf("decoded %+v, want %+v", got, m)
		}
	}

	ref := func(id string) string {
		return `{"deploymentId":"` + id + `","digest":"` + testDigest + `","url":"/api/v1/deployments/` + id + `/` + testDigest + `"}`
	}
	bundle := `{"mediaType":"` + contract.BundleMediaType + `","digest":"` + testDigest + `","url":"/api/v1/bundles/` + testDigest + `"}`
	for _, tt := range []struct{ name, body string }{
		{"not JSON", `{"manifestVersion":`},
		{"trailing data", `{"manifestVersion":1,"deployments":[],"bundle":null} x`},
		{"schema: no deployments", `{"manifestVersion":1,"bundle":null}`},
		{"schema: no bundle", `{"manifestVersion":1,"deployments":[]}`},
		{"schema: version is a string", `{"manifestVersion":"1","deployments":[],"bundle":null}`},
		{"schema: deployment without digest", `{"manifestVersion":1,"deployments":[{"deploymentId":"` + testUUID + `","url":"/x"}],"bundle":` + bundle + `}`},
		{"negative version", `{"manifestVersion":-1,"deployments":[],"bundle":null}`},
		{"deploymentId not a UUID", `{"manifestVersion":1,"deployments":[` + ref("d1") + `],"bundle":` + bundle + `}`},
		{"digest not sha256", `{"manifestVersion":1,"deployments":[{"deploymentId":"` + testUUID + `","digest":"md5:x","url":"/x"}],"bundle":` + bundle + `}`},
		{"duplicate deploymentId", `{"manifestVersion":1,"deployments":[` + ref(testUUID) + `,` + ref(testUUID) + `],"bundle":` + bundle + `}`},
		{"schema: bundle without digest", `{"manifestVersion":1,"deployments":[` + ref(testUUID) + `],"bundle":{"mediaType":"` + contract.BundleMediaType + `","url":"/x"}}`},
		{"schema: bundle without url", `{"manifestVersion":1,"deployments":[` + ref(testUUID) + `],"bundle":{"mediaType":"` + contract.BundleMediaType + `","digest":"` + testDigest + `"}}`},
		{"schema: bundle without mediaType", `{"manifestVersion":1,"deployments":[` + ref(testUUID) + `],"bundle":{"digest":"` + testDigest + `","url":"/x"}}`},
		{"bundle null with deployments", `{"manifestVersion":1,"deployments":[` + ref(testUUID) + `],"bundle":null}`},
	} {
		if _, err := contract.DecodeStateManifest([]byte(tt.body)); !errors.Is(err, contract.ErrInvalidManifest) {
			t.Errorf("%s: err = %v, want ErrInvalidManifest", tt.name, err)
		}
	}
}

// TestDecodeBundle checks the inverse of EncodeBundle under ADR 0012: exactly one regular file per
// deployment, named `<lowercase deploymentId>.yaml`, at the archive root, and nothing else.
func TestDecodeBundle(t *testing.T) {
	files := []contract.BundleFile{
		{DeploymentID: uuid.MustParse(testUUID2), YAML: []byte("id: a\n")},
		{DeploymentID: uuid.MustParse(testUUID), YAML: []byte("id: b\n")},
	}
	b, err := contract.EncodeBundle(files)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := contract.DecodeBundle(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(files) {
		t.Fatalf("decoded %d files, want %d", len(got), len(files))
	}
	for i := range files {
		if got[i].DeploymentID != files[i].DeploymentID || !bytes.Equal(got[i].YAML, files[i].YAML) {
			t.Errorf("file %d: %s %q, want %s %q", i, got[i].DeploymentID, got[i].YAML, files[i].DeploymentID, files[i].YAML)
		}
	}

	reg := func(name string) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len("id: x\n"))}
	}
	gz := func(raw []byte) []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		return buf.Bytes()
	}
	for _, tt := range []struct {
		name   string
		bundle []byte
	}{
		{"not gzip", []byte("id: x\n")},
		{"not tar", gz([]byte("id: x\n"))},
		{"trailing data", append(bytes.Clone(b), 'x')},
		{"directory", tarGz(t, &tar.Header{Typeflag: tar.TypeDir, Name: "d/", Mode: 0o755})},
		{"file in a directory", tarGz(t, reg("d/"+testUUID+".yaml"))},
		{"dot-slash prefix", tarGz(t, reg("./"+testUUID+".yaml"))},
		{"symlink", tarGz(t, &tar.Header{Typeflag: tar.TypeSymlink, Name: testUUID + ".yaml", Linkname: "/etc/passwd"})},
		{"hard link", tarGz(t, reg(testUUID+".yaml"), &tar.Header{Typeflag: tar.TypeLink, Name: testUUID2 + ".yaml", Linkname: testUUID + ".yaml"})},
		{"not a UUID", tarGz(t, reg("deployment.yaml"))},
		{"not .yaml", tarGz(t, reg(testUUID+".yml"))},
		{"uppercase UUID", tarGz(t, reg(strings.ToUpper(testUUID)+".yaml"))},
		{"UUID without hyphens", tarGz(t, reg(strings.ReplaceAll(testUUID, "-", "")+".yaml"))},
		{"duplicate entry", tarGz(t, reg(testUUID+".yaml"), reg(testUUID+".yaml"))},
	} {
		if _, err := contract.DecodeBundle(tt.bundle); !errors.Is(err, contract.ErrInvalidBundle) {
			t.Errorf("%s: err = %v, want ErrInvalidBundle", tt.name, err)
		}
	}
}

// tarGz builds a gzip tar of the given headers; each regular file holds "id: x\n".
func tarGz(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar %s: %v", h.Name, err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte("id: x\n")); err != nil {
				t.Fatalf("tar %s: %v", h.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
