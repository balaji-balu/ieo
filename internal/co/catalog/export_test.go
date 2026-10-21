package catalog

import (
	"crypto/x509"
	"log/slog"

	"github.com/balaji-balu/ieo/internal/ociremote"
)

// NewRegistryWithRoots is NewRegistry with cert as the only root: a test's HTTPS server has a
// certificate no system root signed.
func NewRegistryWithRoots(insecure bool, cert *x509.Certificate, log *slog.Logger) *ociremote.Client {
	opts := registryOptions(insecure, log)
	opts.Roots = x509.NewCertPool()
	opts.Roots.AddCert(cert)
	return ociremote.New(opts)
}
