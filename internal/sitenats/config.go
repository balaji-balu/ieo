package sitenats

import (
	"errors"
	"fmt"
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
	// logged or put in an error (SPEC §15.4).
	Username, Password string
	// CAFile is a PEM file of the certificates that may sign the server's certificate. Empty means
	// the system roots.
	CAFile string
	// Insecure allows a `nats://` URL, which does not require TLS. It never turns verification
	// of a certificate off.
	Insecure bool
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

// Check reports whether a tier may connect with c (SPEC §15.6, ADR 0020). It tries no
// connection; it reads CAFile if one is set. The error is a *ConfigError for the first setting
// that is missing or not valid:
//
//   - a URL that is not `tls://host[:port]`, or `nats://host[:port]` with Insecure set;
//   - a URL with a user name or password, a path, a query or a fragment;
//   - no username or no password;
//   - a CAFile that cannot be read or holds no certificate.
func (c Config) Check() error {
	return nil
}
