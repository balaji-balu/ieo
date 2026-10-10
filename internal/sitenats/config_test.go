package sitenats_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balaji-balu/ieo/internal/sitenats"
)

// testPassword is made up for these tests; no error may ever hold it (SPEC §15.4).
const testPassword = "made-up-for-this-test"

// config returns a valid Config for url.
func config(url string) sitenats.Config {
	return sitenats.Config{URL: url, Username: "site-1", Password: testPassword}
}

// field returns the setting err is about, or "" if err is not a *ConfigError that wraps ErrConfig.
func field(err error) sitenats.Field {
	var ce *sitenats.ConfigError
	if !errors.As(err, &ce) || !errors.Is(err, sitenats.ErrConfig) {
		return ""
	}
	return ce.Field
}

// SPEC §17.7: "Until scoped NATS credentials, the LO and the EN accept a NATS URL other than
// `tls://` only when `lo.nats_insecure`/`en.nats_insecure` is set and exit at startup on one
// without it" (§15.6). This is the rule itself; the exit is in cmd/lo and cmd/en.
func TestSpec_17_7_NATSURLWithoutTLSNeedsInsecure(t *testing.T) {
	tests := []struct {
		url      string
		insecure bool
		ok       bool
	}{
		{"tls://nats.site.example:4222", false, true},
		{"tls://nats.site.example", false, true},
		{"TLS://nats.site.example:4222", false, true}, // a scheme is case-insensitive
		{"tls://[::1]:4222", false, true},
		{"tls://nats.site.example:4222", true, true}, // insecure allows, it does not require
		{"nats://nats:4222", true, true},
		{"NATS://nats:4222", true, true},
		{"nats://nats:4222", false, false},
		{"nats://127.0.0.1:4222", false, false},
		// SPEC §15.6: no other scheme is accepted, with or without insecure.
		{"ws://nats:8080", true, false},
		{"wss://nats:8443", true, false},
		{"wss://nats:8443", false, false},
		{"http://nats:4222", true, false},
		{"nats:4222", true, false},
		{"nats.site.example:4222", false, false},
		{"//nats:4222", true, false},
	}
	for _, tc := range tests {
		cfg := config(tc.url)
		cfg.Insecure = tc.insecure
		err := cfg.Check()
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s, insecure %v: Check: %v; want it accepted", tc.url, tc.insecure, err)
		case !tc.ok && field(err) != sitenats.FieldURL:
			t.Errorf("%s, insecure %v: Check error = %v; want a ConfigError about the URL", tc.url, tc.insecure, err)
		}
	}
}

// ADR 0020: one server URL, with nothing in it but the scheme, host and port.
func TestCheckRefusesURL(t *testing.T) {
	urls := []string{
		"",
		"tls://",
		"tls://:4222",
		"tls://nats:port",
		"tls://nats:4222/",
		"tls://nats:4222/path",
		"tls://nats:4222?tls=false",
		"tls://nats:4222#frag",
		"tls://a:4222,tls://b:4222", // one server, not a list
		"tls://site-1@nats:4222",
		"tls://site-1:" + testPassword + "@nats:4222", // SPEC §15.4: no credential in a URL
		"tls://:" + testPassword + "@nats:4222",
		"tls://nats:4222 ",
		"tls://na ts:4222",
	}
	for _, u := range urls {
		for _, insecure := range []bool{false, true} {
			cfg := config(u)
			cfg.Insecure = insecure
			if err := cfg.Check(); field(err) != sitenats.FieldURL {
				t.Errorf("%q, insecure %v: Check error = %v; want a ConfigError about the URL", u, insecure, err)
			}
		}
	}
}

// SPEC §6.3: the username and password are REQUIRED until Appendix B step 4.
func TestCheckRequiresCredentials(t *testing.T) {
	cfg := config("tls://nats:4222")
	cfg.Username = ""
	if err := cfg.Check(); field(err) != sitenats.FieldUsername {
		t.Errorf("no username: Check error = %v; want a ConfigError about the username", err)
	}
	cfg = config("tls://nats:4222")
	cfg.Password = ""
	if err := cfg.Check(); field(err) != sitenats.FieldPassword {
		t.Errorf("no password: Check error = %v; want a ConfigError about the password", err)
	}
}

// certificatePEM returns a self-signed certificate made for this test.
func certificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// SPEC §15.6: the server's certificate is verified against the CA file "when set".
func TestCheckCAFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, content []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := write("ca.pem", certificatePEM(t))
	for _, url := range []string{"tls://nats:4222", "nats://nats:4222"} {
		cfg := config(url)
		cfg.Insecure = true
		cfg.CAFile = good
		if err := cfg.Check(); err != nil {
			t.Errorf("%s with a CA file: Check: %v", url, err)
		}
	}
	bad := map[string]string{
		"a file that does not exist": filepath.Join(dir, "missing.pem"),
		"a directory":                dir,
		"an empty file":              write("empty.pem", nil),
		"a file with no certificate": write("text.pem", []byte("not a certificate\n")),
		"a private key only":         write("key.pem", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1, 2, 3}})),
	}
	for name, path := range bad {
		cfg := config("tls://nats:4222")
		cfg.CAFile = path
		if err := cfg.Check(); field(err) != sitenats.FieldCAFile {
			t.Errorf("%s: Check error = %v; want a ConfigError about the CA file", name, err)
		}
	}
}

// SPEC §15.4, ADR 0020: the text of an error holds neither the password nor the username, and not
// the URL, which is where a password ends up when someone puts one there.
func TestCheckErrorsHoldNoCredential(t *testing.T) {
	const username = "made-up-user"
	configs := []sitenats.Config{
		{URL: "tls://" + username + ":" + testPassword + "@nats:4222", Username: username, Password: testPassword},
		{URL: "nats://" + username + ":" + testPassword + "@nats:4222", Username: username, Password: testPassword},
		{URL: "http://nats:4222/" + testPassword, Username: username, Password: testPassword},
		{URL: "tls://nats:4222?password=" + testPassword, Username: username, Password: testPassword},
		{URL: "tls://nats:4222", Password: testPassword},
		{URL: "tls://nats:4222", Username: username, Password: testPassword, CAFile: filepath.Join(t.TempDir(), "missing.pem")},
		{URL: "://" + testPassword, Username: username, Password: testPassword},
	}
	for _, cfg := range configs {
		err := cfg.Check()
		if err == nil {
			t.Errorf("Check(%q) succeeded, want an error", strings.ReplaceAll(cfg.URL, testPassword, "…"))
			continue
		}
		for _, secret := range []string{testPassword, username} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error text holds %q: %v", secret, err)
			}
		}
	}
}
