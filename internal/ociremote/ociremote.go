// Package ociremote opens OCI repositories on remote registries, for reading. It is how both the
// EN (SPEC §8.9 step 4.2) and the CO (SPEC §5.3) reach a registry: the transport, HTTPS unless
// told otherwise, the redirects, the proxy, and errors that hold no URL and no text a server sent
// (ADR 0021). Which credential a registry is given is the caller's: this package sends the one
// Options.Credential returns, and none without it.
package ociremote

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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

// Options is how a Client reaches registries.
type Options struct {
	// Insecure makes every registry be reached over plain HTTP. Without it the Client makes no
	// plain-HTTP request at all, and verifies each certificate against the system roots. It never
	// turns the verification of a certificate off.
	Insecure bool
	// InsecureKey is the configuration key of Insecure (`en.registry.insecure`), as the startup
	// warning and the errors name it. Every caller sets it.
	InsecureKey string
	// Credential returns the credential of the registry a request goes to; its host is
	// `host:port` as RegistryOf names it. It is asked only for the host a request was first sent
	// to, and for the token service that host names. Nil means every request is anonymous: no
	// credential is sent to any host.
	Credential auth.CredentialFunc
	// UserAgent is the User-Agent of every request.
	UserAgent string
	// Log receives the startup warning of Insecure; nil means slog.Default.
	Log *slog.Logger
	// Roots replaces the system roots, which a test's HTTPS server is not signed by. Every caller
	// but a test leaves it nil.
	Roots *x509.CertPool
}

// Client opens repositories. It is safe for concurrent use.
type Client struct {
	// http sends every request: to registries, to token services and to redirect targets.
	http *auth.Client
	// plainHTTP is Options.Insecure.
	plainHTTP bool
	// idle closes the connections kept for a next request.
	idle func()
}

// Close closes the connections the Client keeps open between requests. A request in flight is
// not ended, and the Client can still be used.
func (c *Client) Close() { c.idle() }

// New returns the Client for opts. It reads the proxy settings of the environment (`HTTPS_PROXY`,
// `HTTP_PROXY`, `NO_PROXY`), once.
//
// If opts.Insecure is set it logs a warning, which says whether credentials are then sent in the
// clear (opts.Credential is set), and through which proxy.
func New(opts Options) *Client {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.InsecureKey == "" {
		opts.InsecureKey = "the insecure setting"
	}
	proxy := httpproxy.FromEnvironment().ProxyFunc()
	if opts.Insecure {
		warnInsecure(log, opts.InsecureKey, opts.Credential != nil, proxy)
	}

	base := &http.Transport{
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
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.Roots},
	}
	transport := http.RoundTripper(base)
	if !opts.Insecure {
		transport = httpsOnly{next: transport, refusal: &plainHTTPRefused{key: opts.InsecureKey}}
	}
	transport = firstHostOnly{transport}
	return &Client{
		plainHTTP: opts.Insecure,
		idle:      base.CloseIdleConnections,
		// auth.Client answers a registry's challenge. It asks for the credential of the host the
		// request goes to, and keeps each token for that host alone.
		http: &auth.Client{
			// No retries: a failed request is the caller's to try again (ADR 0021).
			Client:     &http.Client{Transport: transport, CheckRedirect: checkRedirect},
			Header:     http.Header{"User-Agent": {opts.UserAgent}},
			Cache:      auth.NewCache(),
			Credential: opts.Credential, // nil: auth.EmptyCredential for every host
		},
	}
}

// warnInsecure logs the warning of the insecure key (ADR 0021). A proxy is named by its host: its
// address can hold a password.
func warnInsecure(log *slog.Logger, key string, credentials bool, proxy func(*url.URL) (*url.URL, error)) {
	msg := key + " is set: registries are reached over plain HTTP"
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

// The names of Docker Hub. They are one registry (ADR 0021): `docker login` writes its entry
// under index.docker.io, a repository names docker.io, and requests go to registry-1.docker.io.
const dockerHub = "docker.io"

// RegistryOf returns the registry a host belongs to: the host itself, or `docker.io` for each of
// Docker Hub's names. Hosts are compared exactly, port included (ADR 0021).
func RegistryOf(host string) string {
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return dockerHub
	}
	return host
}

var (
	errProxy        = errors.New("the proxy address in the environment is not valid")
	errRedirects    = errors.New("too many redirects")
	errBodyRedirect = errors.New("a request with a body is not redirected to another host")
)

// plainHTTPRefused is the error of a plain-HTTP request that is not made.
type plainHTTPRefused struct {
	key string // Options.InsecureKey
}

func (p *plainHTTPRefused) Error() string {
	return "a plain-HTTP request is not made unless " + p.key + " is set"
}

// maxRedirects is how many redirects one request follows, as Go's HTTP client does by default.
const maxRedirects = 10

// sameHost reports whether two URLs are of one host as ADR 0021 means it: the same scheme and
// the same `host:port`, compared exactly. Another port and a subdomain are other hosts.
func sameHost(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}

// checkRedirect follows a redirect, as registries send layer downloads to storage hosts, and
// keeps what is for the first host from another one (ADR 0021): the Authorization header, and
// the body of a request, which for a token service holds an identity token. Go's own rule would
// send the header on to another port of the same host, and to a subdomain.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errRedirects
	}
	if sameHost(req.URL, via[0].URL) {
		return nil
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return errBodyRedirect
	}
	req.Header.Del("Authorization")
	return nil
}

// firstHostOnly lets only the host a request was first sent to ask for a credential. The
// registry client answers the challenge of the last response of a redirect chain with the
// credential of the first host: a storage host could name its own token service and be sent the
// registry's password (ADR 0021). So the challenge of any other host is removed, and its 401 is
// the answer.
type firstHostOnly struct {
	next http.RoundTripper
}

func (f firstHostOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := f.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// req.Response is the redirect that led to req; the chain ends at the first request.
	first := req
	for first.Response != nil && first.Response.Request != nil {
		first = first.Response.Request
	}
	if !sameHost(req.URL, first.URL) {
		resp.Header.Del("WWW-Authenticate")
	}
	return resp, nil
}

// httpsOnly refuses every request that is not HTTPS: to a registry, to the token service a
// registry names, and to the target of a redirect (ADR 0021).
type httpsOnly struct {
	next    http.RoundTripper
	refusal error
}

func (h httpsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		if req.Body != nil {
			_ = req.Body.Close() // a RoundTripper closes the body; the request is not sent
		}
		return nil, h.refusal
	}
	return h.next.RoundTrip(req)
}

// Open returns the repository named repository (`registry.example/org/app`: a registry host and
// a path, with no tag or digest), for reading. It sends no request: a registry that cannot be
// reached fails the first read.
//
// The registry is sent the credential Options.Credential returns for its host, and so is the
// token service that registry names. No other host is sent one, neither another registry nor the
// target of a redirect.
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
	ref.Registry = RegistryOf(ref.Registry)
	return &target{
		repo:     &remote.Repository{Client: c.http, Reference: ref, PlainHTTP: c.plainHTTP},
		registry: ref.Registry,
		host:     ref.Host(),
	}, nil
}
