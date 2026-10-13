package registry

import "crypto/x509"

// WithRoots returns cfg with roots in place of the system roots: a test's HTTPS server has a
// certificate no system root signed.
func WithRoots(cfg Config, roots *x509.CertPool) Config {
	cfg.roots = roots
	return cfg
}
