package bearer_test

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balaji-balu/ieo/internal/bearer"
)

// SPEC §15.6: requests go directly to the CO URL, through no proxy, and verify the CO against the
// CA file when it is set.
func TestTransportUsesNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.example:3128")

	tr, err := bearer.Transport("")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Error("Proxy is set without a CA file")
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.RootCAs != nil {
		t.Error("RootCAs is set without a CA file; want the system roots")
	}

	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	tr, err = bearer.Transport(ca)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Error("Proxy is set with a CA file")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Error("the CA file does not set RootCAs")
	}

	if _, err := bearer.Transport(filepath.Join(t.TempDir(), "none.pem")); err == nil {
		t.Error("a CA file that does not exist is accepted")
	}
	notPEM := filepath.Join(t.TempDir(), "ca.txt")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bearer.Transport(notPEM); err == nil {
		t.Error("a CA file without a PEM certificate is accepted")
	}
}

// SPEC §15.6: the token goes only to an https:// URL, or to an http:// one when allowed, and the
// error never repeats the URL (§15.4).
func TestCheckURL(t *testing.T) {
	for _, tc := range []struct {
		url      string
		insecure bool
		ok       bool
	}{
		{"https://co.example:8443", false, true},
		{"https://co.example:8443/", false, true},
		{"HTTPS://co.example", false, true},
		{"http://co.example", false, false},
		{"HTTP://co.example", false, false},
		{"http://co.example", true, true},
		{"ftp://co.example", true, false},
		{"co.example:8443", true, false},
		{"https://", false, false},
		{"https://user:secret@co.example", false, false},
		{"https://co.example?x=1", false, false},
		{"https://co.example#frag", false, false},
		{"https://co.example/%zz", false, false},
	} {
		err := bearer.CheckURL(tc.url, tc.insecure, "THE_SETTING")
		if (err == nil) != tc.ok {
			t.Errorf("CheckURL(%q, %v) = %v, want ok %v", tc.url, tc.insecure, err, tc.ok)
		}
		if err != nil && strings.Contains(err.Error(), "co.example") {
			t.Errorf("CheckURL(%q): error %q repeats the URL", tc.url, err)
		}
	}
	if err := bearer.CheckURL("http://co.example", false, "THE_SETTING"); err == nil || !strings.Contains(err.Error(), "THE_SETTING") {
		t.Errorf("http:// without insecure: %v, want an error naming THE_SETTING", err)
	}
}
