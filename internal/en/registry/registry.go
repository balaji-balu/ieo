// Package registry opens the OCI repositories the EN pulls Compose archives from (SPEC §8.9 step
// 4.2, ADR 0021). It holds the `en.registry.*` settings and which credential each registry is
// given; how a registry is reached (the transport, the redirects, the proxy and the errors) is
// package ociremote's, which the CO shares. What is pulled, its limits and its digest check are
// package pull's (ADR 0019).
package registry

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"

	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/balaji-balu/ieo/internal/ociremote"
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

// Client opens repositories; its Open is a pull.OpenRepository. A registry is sent the credential
// whose key in the auth file is its host (`host` or `host:port`, compared exactly; `docker.io`,
// `index.docker.io` and `registry-1.docker.io` are one registry), and so is the token service
// that registry names. No other host is sent one.
type Client = ociremote.Client

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
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	var file authFile
	if cfg.AuthFile != "" {
		var err error
		if file, err = readAuthFile(cfg.AuthFile); err != nil {
			return nil, err
		}
	}

	// ADR 0021: the hosts, so an operator sees which pulls are anonymous; never the entries.
	hosts := append([]string{}, file.Hosts()...) // logged as [], not null
	log.Info("registries with a credential; a pull from any other is anonymous", slog.Any("hosts", hosts))
	if len(file.Helpers) > 0 {
		log.Warn("the registry auth file asks for a credential helper, and the EN runs none: only its auths entries are used",
			slog.Any("keys", file.Helpers))
	}

	var credential auth.CredentialFunc // none: every pull is anonymous
	if len(hosts) > 0 {
		credentials := file.Credentials
		credential = func(_ context.Context, hostport string) (auth.Credential, error) {
			return credentials[ociremote.RegistryOf(hostport)], nil // none: auth.EmptyCredential
		}
	}
	return ociremote.New(ociremote.Options{
		Insecure:    cfg.Insecure,
		InsecureKey: "en.registry.insecure",
		Credential:  credential,
		UserAgent:   "ieo-en",
		Log:         log,
		Roots:       cfg.roots,
	}), nil
}
