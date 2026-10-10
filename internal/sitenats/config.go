package sitenats

import (
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is how a tier reaches the site NATS server until Appendix B step 4 (SPEC §15.6): an
// external server, and one username and password for the whole site. In SPEC §6.3 the LO's keys
// are `lo.nats.*` and `lo.nats_insecure`, the EN's `en.nats_url`, `en.nats.*` and
// `en.nats_insecure`.
type Config struct {
	// URL is the server's URL: `tls://host[:port]`, or `nats://host[:port]` when Insecure is set.
	// It holds no user name or password, and no path, query or fragment.
	URL string
	// Username and Password are the site's credentials. Both are required. The password is never
	// logged or put in an error (SPEC §15.4): printing or logging a Config leaves it out.
	Username, Password string
	// CAFile is a PEM file of the certificates that may sign the server's certificate. Empty means
	// the system roots.
	CAFile string
	// Insecure allows a `nats://` URL, which does not require TLS. It never turns verification
	// of a certificate off.
	Insecure bool
}

// String returns c without its password, so that a Config can be printed (SPEC §15.4). A URL
// that holds a user name or password, which Check refuses, is not shown either.
func (c Config) String() string {
	password := "not set"
	if c.Password != "" {
		password = "set, not shown"
	}
	return fmt.Sprintf("{URL: %s, Username: %q, Password: (%s), CAFile: %q, Insecure: %t}",
		c.printableURL(), c.Username, password, c.CAFile, c.Insecure)
}

// GoString returns what String does: `%#v` shows no password either.
func (c Config) GoString() string { return c.String() }

// LogValue returns c without its password, so that a Config can be logged (SPEC §15.4).
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("url", c.printableURL()),
		slog.String("username", c.Username),
		slog.String("ca_file", c.CAFile),
		slog.Bool("insecure", c.Insecure),
	)
}

// printableURL returns the URL if it can hold no credential, and a note in its place otherwise.
func (c Config) printableURL() string {
	p, err := url.Parse(c.URL)
	if err != nil || p.User != nil || p.Path != "" || p.RawQuery != "" || p.Fragment != "" || p.Opaque != "" {
		return "(not shown: not a plain scheme://host:port)"
	}
	return c.URL
}

// ErrConfig is what every error of Config.Check wraps: the tier cannot run with this
// configuration (SPEC §6.1).
var ErrConfig = errors.New("invalid site NATS configuration")

// Field names the setting of a Config an error is about, so a tier can name its own key for it.
type Field string

// The settings of a Config.
const (
	FieldURL      Field = "URL"
	FieldUsername Field = "username"
	FieldPassword Field = "password"
	FieldCAFile   Field = "CA file"
)

// ConfigError says which setting of a Config is missing or not valid, and why. Its text never
// holds the URL, the username or the password. It wraps ErrConfig.
type ConfigError struct {
	Field  Field
	Reason string
}

func (e *ConfigError) Error() string { return fmt.Sprintf("site NATS %s: %s", e.Field, e.Reason) }

// Unwrap returns ErrConfig.
func (e *ConfigError) Unwrap() error { return ErrConfig }

// The URL schemes a tier accepts (SPEC §15.6).
const (
	schemeTLS   = "tls"  // requires TLS
	schemePlain = "nats" // does not; only with Insecure
)

// Check reports whether a tier may connect with c (SPEC §15.6, ADR 0020). It tries no
// connection; it reads CAFile if one is set. The error is a *ConfigError for the first setting
// that is missing or not valid:
//
//   - a URL that is not `tls://host[:port]`, or `nats://host[:port]` with Insecure set;
//   - a URL with a user name or password, a path, a query or a fragment;
//   - no username or no password;
//   - a CAFile that cannot be read or holds no certificate.
func (c Config) Check() error {
	if reason := checkURL(c.URL, c.Insecure); reason != "" {
		return &ConfigError{Field: FieldURL, Reason: reason}
	}
	if c.Username == "" {
		return &ConfigError{Field: FieldUsername, Reason: "not set"}
	}
	if c.Password == "" {
		return &ConfigError{Field: FieldPassword, Reason: "not set"}
	}
	if _, err := c.rootCAs(); err != nil {
		return &ConfigError{Field: FieldCAFile, Reason: err.Error()}
	}
	return nil
}

// checkURL returns why u is not a server URL a tier may use, or "" if it is one. The reason
// never repeats u: a URL is where a password ends up when someone puts one there (SPEC §15.4).
func checkURL(u string, insecure bool) string {
	if u == "" {
		return "not set"
	}
	p, err := url.Parse(u)
	if err != nil {
		return "not a URL" // the parse error repeats u
	}
	switch strings.ToLower(p.Scheme) {
	case schemeTLS:
	case schemePlain:
		if !insecure {
			return "a nats:// URL does not require TLS: use tls://, or allow it with the tier's nats_insecure setting"
		}
	default:
		return "the URL must start with tls:// (or nats://, with the tier's nats_insecure setting)"
	}
	switch {
	case p.User != nil:
		return "the URL must not hold a user name or password"
	case p.Hostname() == "":
		return "the URL has no host"
	case strings.HasSuffix(p.Host, ":"):
		return "the URL has a `:` after the host but no port"
	case p.Path != "" || p.RawQuery != "" || p.ForceQuery || p.Fragment != "" || strings.Contains(u, "#"):
		return "the URL must hold only a scheme, a host and a port"
	}
	if port := p.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "the URL's port is not between 1 and 65535"
		}
	}
	return ""
}

// rootCAs returns the certificates of CAFile, or nil, which means the system roots, if none is
// set.
func (c Config) rootCAs() (*x509.CertPool, error) {
	if c.CAFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, err // names the file
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificate", c.CAFile)
	}
	return pool, nil
}
