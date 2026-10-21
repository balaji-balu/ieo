package bearer

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Transport returns the transport of requests that carry a token (SPEC §15.6). It sends them
// directly to the CO, through no proxy whatever HTTP_PROXY and HTTPS_PROXY say, since the token
// goes only to the configured URL. It verifies the CO's certificate against the certificates in the
// PEM file caFile, or against the system roots when caFile is empty.
func Transport(caFile string) (*http.Transport, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	if caFile == "" {
		return t, nil
	}
	pemCerts, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemCerts) {
		return nil, errors.New("no PEM certificate in " + caFile)
	}
	t.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return t, nil
}

// CheckURL checks that u is a CO URL a token may be sent to (SPEC §15.6): https://, or http://
// when insecure is true, with a host, and no user info, query or fragment, since the API paths are
// appended to it. insecureSetting names the setting that allows http://, for the error. The error
// never repeats u.
func CheckURL(u string, insecure bool, insecureSetting string) error {
	p, err := url.Parse(u)
	if err != nil {
		return errors.New("not a URL") // the parse error repeats u
	}
	switch {
	case p.Scheme == "http" && !insecure:
		return fmt.Errorf("an http:// URL sends the token without TLS: use https://, or set %s to true", insecureSetting)
	case p.Scheme != "https" && p.Scheme != "http":
		return errors.New("the URL must start with https://")
	case p.Host == "":
		return errors.New("the URL has no host")
	case p.User != nil: // SPEC §15.4: credentials don't belong in a URL
		return errors.New("the URL must not hold a user name or password")
	case p.RawQuery != "" || p.ForceQuery || p.Fragment != "" || strings.Contains(u, "#"):
		return errors.New("the URL must not have a query or fragment: the API paths are appended to it")
	}
	return nil
}

// IsHTTP reports whether u, a URL CheckURL accepted, is http://, so that the caller warns that
// the token is sent without TLS (SPEC §15.6). The scheme is case-insensitive.
func IsHTTP(u string) bool { return strings.HasPrefix(strings.ToLower(u), "http://") }
