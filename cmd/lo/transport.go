package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
)

// newTransport returns the transport of the LO's Margo API requests (SPEC §15.6). It sends them
// directly to the CO, through no proxy whatever HTTP_PROXY and HTTPS_PROXY say, since the token
// goes only to lo.co_url. It verifies the CO's certificate against the certificates in the PEM
// file caFile, or against the system roots when caFile is empty.
func newTransport(caFile string) (*http.Transport, error) {
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
