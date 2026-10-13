// Package registry opens the OCI repositories the EN pulls Compose archives from (SPEC §8.9 step
// 4.2, ADR 0021). It is the only EN package that knows how a registry is reached and which
// credential it is given: the transport, the redirects, the proxy and the `en.registry.*`
// settings. What is pulled, its limits and its digest check are package pull's (ADR 0019).
package registry

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"

	"oras.land/oras-go/v2"
)

// Config is how the EN reaches registries (SPEC §6.3).
type Config struct {
	// AuthFile is `en.registry.auth_file`: a file in the Docker `config.json` format, of which
	// only the static `auths` entries are read. Empty means every pull is anonymous.
	AuthFile string
	// Insecure is `en.registry.insecure`: every registry is reached over plain HTTP. Without it
	// the EN makes no plain-HTTP request at all, and verifies each certificate against the system
	// roots. It never turns the verification of a certificate off.
	Insecure bool
	// Log receives the startup lines; nil means slog.Default.
	Log *slog.Logger

	// roots replaces the system roots in this package's tests, which cannot add to them.
	roots *x509.CertPool
}

// ErrConfig is what every error of New wraps: the EN cannot run with this configuration (SPEC
// §6.1).
var ErrConfig = errors.New("invalid registry configuration")

// ConfigError says why the file of `en.registry.auth_file` cannot be used. Its text names the
// file, and a registry host where one entry is at fault; it never holds anything else the file
// contains (SPEC §15.4). It wraps ErrConfig.
type ConfigError struct {
	File   string
	Reason string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("registry auth file %s: %s", e.File, e.Reason)
}

// Unwrap returns ErrConfig.
func (e *ConfigError) Unwrap() error { return ErrConfig }

// Client opens repositories. It is safe for concurrent use.
type Client struct{}

// New returns the Client for cfg. It reads cfg.AuthFile, once, and the proxy settings of the
// environment (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`); neither is read again.
//
// It logs the registry hosts that have a credential, never the credentials; a warning if the file
// names a credential helper, which the EN does not run; and a warning if cfg.Insecure is set,
// which says whether credentials are then sent in the clear, and through which proxy.
//
// The error is a *ConfigError: the file is set and is missing, cannot be read, is not a Docker
// `config.json`, has an entry that cannot be decoded, or names one registry twice.
func New(cfg Config) (*Client, error) {
	return &Client{}, nil
}

// Open returns the repository named repository (`registry.example/org/app`: a registry host and
// a path, with no tag or digest), for reading. It is a pull.OpenRepository. It sends no request:
// a registry that cannot be reached fails the first read.
//
// The registry is sent the credential whose key in the auth file is its host (`host` or
// `host:port`, compared exactly; `docker.io`, `index.docker.io` and `registry-1.docker.io` are one
// registry), and so is the token service that registry names. No other host is sent one,
// neither another registry nor the target of a redirect.
//
// An error of the repository returned names the registry host, the host that answered if it is
// another, and the HTTP status or the network failure. It holds no URL, no credential and nothing
// a server sent but its error codes, so it can be logged and put in a status message (SPEC
// §15.4). errors.Is finds context.DeadlineExceeded, context.Canceled and errdef.ErrNotFound in it.
func (c *Client) Open(ctx context.Context, repository string) (oras.ReadOnlyTarget, error) {
	return nil, errors.New("not implemented")
}
