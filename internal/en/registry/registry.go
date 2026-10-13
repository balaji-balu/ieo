// Package registry opens the OCI repositories the EN pulls Compose archives from (SPEC §8.9 step
// 4.2, ADR 0021). It is the only EN package that knows how a registry is reached and which
// credential it is given: the transport, the redirects, the proxy and the `en.registry.*`
// settings. What is pulled, its limits and its digest check are package pull's (ADR 0019).
package registry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/http/httpproxy"
	"oras.land/oras-go/v2"
	orasregistry "oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
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
type Client struct {
	// http sends every request: to registries, to token services and to redirect targets.
	http *auth.Client
	// plainHTTP is en.registry.insecure.
	plainHTTP bool
}

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
	proxy := httpproxy.FromEnvironment().ProxyFunc()

	// ADR 0021: the hosts, so an operator sees which pulls are anonymous; never the entries.
	hosts := append([]string{}, file.Hosts()...) // logged as [], not null
	log.Info("registries with a credential; a pull from any other is anonymous", slog.Any("hosts", hosts))
	if len(file.Helpers) > 0 {
		log.Warn("the registry auth file asks for a credential helper, and the EN runs none: only its auths entries are used",
			slog.Any("keys", file.Helpers))
	}
	if cfg.Insecure {
		warnInsecure(log, len(hosts) > 0, proxy)
	}

	transport := http.RoundTripper(&http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			u, err := proxy(req.URL)
			if err != nil {
				return nil, errProxy // the error of the proxy package quotes the address
			}
			return u, nil
		},
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		// Certificates are always verified; nil roots are the system's.
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.roots},
	})
	if !cfg.Insecure {
		transport = httpsOnly{transport}
	}
	credentials := file.Credentials
	return &Client{
		plainHTTP: cfg.Insecure,
		// auth.Client answers a registry's challenge. It asks for the credential of the host the
		// request goes to, and keeps each token for that host alone.
		http: &auth.Client{
			// No retries: a failed pull is the LO's to try again (ADR 0021).
			Client: &http.Client{Transport: transport, CheckRedirect: checkRedirect},
			Header: http.Header{"User-Agent": {"ieo-en"}},
			Cache:  auth.NewCache(),
			Credential: func(_ context.Context, hostport string) (auth.Credential, error) {
				return credentials[registryOf(hostport)], nil // none: auth.EmptyCredential
			},
		},
	}, nil
}

// warnInsecure logs the warning of en.registry.insecure (ADR 0021). A proxy is named by its host:
// its address can hold a password.
func warnInsecure(log *slog.Logger, credentials bool, proxy func(*url.URL) (*url.URL, error)) {
	msg := "en.registry.insecure is set: registries are reached over plain HTTP"
	if credentials {
		msg += ", and the registry credentials are sent in the clear"
	}
	attrs := []any{slog.Bool("credentials_in_clear", credentials)}
	// What the environment gives for a plain-HTTP request to a host that NO_PROXY cannot name.
	if u, err := proxy(&url.URL{Scheme: "http", Host: "registry.invalid"}); err == nil && u != nil {
		msg += ", to the registry and to the proxy on the way"
		attrs = append(attrs, slog.String("proxy", u.Host))
	}
	log.Warn(msg, attrs...)
}

var (
	errProxy     = errors.New("the proxy address in the environment is not valid")
	errPlainHTTP = errors.New("a plain-HTTP request is not made unless en.registry.insecure is set")
)

// maxRedirects is how many redirects one request follows, as Go's HTTP client does by default.
const maxRedirects = 10

// checkRedirect follows a redirect, as registries send layer downloads to storage hosts, and
// keeps the Authorization header from another host: a target whose scheme or `host:port` is not
// exactly the first request's (ADR 0021). Go's own rule would send it on to another port of the
// same host, and to a subdomain.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if first := via[0].URL; req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

// httpsOnly refuses every request that is not HTTPS: to a registry, to the token service a
// registry names, and to the target of a redirect (ADR 0021).
type httpsOnly struct {
	next http.RoundTripper
}

func (h httpsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		if req.Body != nil {
			_ = req.Body.Close() // a RoundTripper closes the body; the request is not sent
		}
		return nil, errPlainHTTP
	}
	return h.next.RoundTrip(req)
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
func (c *Client) Open(_ context.Context, repository string) (oras.ReadOnlyTarget, error) {
	// Neither error quotes repository: what is not a repository may hold anything.
	ref, err := orasregistry.ParseReference(repository)
	if err != nil {
		return nil, errors.New("not a repository: want <registry host>/<path>")
	}
	if ref.Reference != "" {
		return nil, errors.New("not a repository: it holds a tag or a digest")
	}
	ref.Registry = registryOf(ref.Registry)
	return &target{
		repo:     &remote.Repository{Client: c.http, Reference: ref, PlainHTTP: c.plainHTTP},
		registry: ref.Registry,
		host:     ref.Host(),
	}, nil
}
