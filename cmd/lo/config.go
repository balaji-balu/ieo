package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/sitenats"
)

// The defaults of SPEC §6.3.
const (
	defaultPollInterval      = 60 * time.Second // lo.poll.interval
	defaultAckTimeout        = 5 * time.Second  // lo.command.ack_timeout
	defaultReconcileInterval = 5 * time.Minute  // lo.reconcile.interval
)

// config is the LO's configuration (SPEC §6.3). A config returned by loadConfig is valid.
type config struct {
	SiteID       contract.SiteID // lo.site_id
	COURL        string          // lo.co_url: http or https, with a host, and no user info, query or fragment
	COInsecure   bool            // lo.co_insecure: allows an http COURL (SPEC §15.6)
	CAFile       string          // lo.tls.ca_file: verifies the CO; empty means the system roots
	SiteToken    string          // lo.site_token (SPEC §15.6); never logged (§15.4)
	DataDir      string          // lo.data_dir
	PollInterval time.Duration   // lo.poll.interval, greater than 0

	// The site NATS server until Appendix B step 4 (SPEC §15.6); sitenats.Config.Check accepts them.
	NATSURL      string // lo.nats.url
	NATSInsecure bool   // lo.nats_insecure: allows a nats:// NATSURL
	NATSUsername string // lo.nats.username
	NATSPassword string // lo.nats.password; never logged (§15.4)
	NATSCAFile   string // lo.nats.ca_file: verifies the NATS server; empty means the system roots

	AckTimeout        time.Duration // lo.command.ack_timeout, greater than 0
	ReconcileInterval time.Duration // lo.reconcile.interval, greater than 0
}

// configError is a configuration the LO cannot run with (SPEC §6.1).
type configError string

func (e configError) Error() string { return string(e) }

// key is one configuration key: its environment variable and, except for the token and the NATS
// password, its flag.
type key struct {
	spec, env, flag string
}

var (
	keySiteID       = key{"lo.site_id", "LO_SITE_ID", "site-id"}
	keyCOURL        = key{"lo.co_url", "LO_CO_URL", "co-url"}
	keyCOInsecure   = key{"lo.co_insecure", "LO_CO_INSECURE", "co-insecure"}
	keyCAFile       = key{"lo.tls.ca_file", "LO_TLS_CA_FILE", "tls-ca-file"}
	keyDataDir      = key{"lo.data_dir", "LO_DATA_DIR", "data-dir"}
	keyPollInterval = key{"lo.poll.interval", "LO_POLL_INTERVAL", "poll-interval"}
	// SPEC §6.3: the token comes only from the environment, so it never shows in a process
	// listing (§15.4).
	keySiteToken = key{spec: "lo.site_token", env: "LO_SITE_TOKEN"}

	keyNATSURL      = key{"lo.nats.url", "LO_NATS_URL", "nats-url"}
	keyNATSInsecure = key{"lo.nats_insecure", "LO_NATS_INSECURE", "nats-insecure"}
	keyNATSUsername = key{"lo.nats.username", "LO_NATS_USERNAME", "nats-username"}
	keyNATSCAFile   = key{"lo.nats.ca_file", "LO_NATS_CA_FILE", "nats-ca-file"}
	// SPEC §6.3: like the token, the NATS password comes only from the environment.
	keyNATSPassword = key{spec: "lo.nats.password", env: "LO_NATS_PASSWORD"}

	keyAckTimeout        = key{"lo.command.ack_timeout", "LO_COMMAND_ACK_TIMEOUT", "command-ack-timeout"}
	keyReconcileInterval = key{"lo.reconcile.interval", "LO_RECONCILE_INTERVAL", "reconcile-interval"}
)

func (k key) String() string {
	if k.flag == "" {
		return fmt.Sprintf("%s (%s)", k.env, k.spec)
	}
	return fmt.Sprintf("%s or --%s (%s)", k.env, k.flag, k.spec)
}

// loadConfig reads the configuration from args and getenv: each key from its environment variable,
// overridden by its flag (SPEC §6.1). It validates every key, and returns a configError naming the
// first one that is missing or invalid.
func loadConfig(args []string, getenv func(string) string) (config, error) {
	fs := flag.NewFlagSet("lo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flags := map[key]*string{}
	for _, k := range []key{
		keySiteID, keyCOURL, keyCAFile, keyDataDir, keyPollInterval,
		keyNATSURL, keyNATSUsername, keyNATSCAFile, keyAckTimeout, keyReconcileInterval,
	} {
		flags[k] = fs.String(k.flag, "", "")
	}
	boolFlags := map[key]*bool{}
	for _, k := range []key{keyCOInsecure, keyNATSInsecure} {
		boolFlags[k] = fs.Bool(k.flag, false, "")
	}
	if err := fs.Parse(args); err != nil {
		return config{}, configError(err.Error())
	}
	if fs.NArg() > 0 {
		return config{}, configError(fmt.Sprintf("unexpected argument %q: lo takes only flags", fs.Arg(0)))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	value := func(k key) string {
		if set[k.flag] {
			return *flags[k]
		}
		return getenv(k.env)
	}
	required := func(k key) (string, error) {
		v := value(k)
		if v == "" {
			return "", configError(k.String() + " is not set")
		}
		return v, nil
	}
	// boolean returns the value of a key that is false unless it is set.
	boolean := func(k key) (bool, error) {
		if set[k.flag] {
			return *boolFlags[k], nil
		}
		s := getenv(k.env)
		if s == "" {
			return false, nil
		}
		b, err := strconv.ParseBool(s)
		if err != nil {
			return false, configError(fmt.Sprintf("%s: %q is not true or false", k, s))
		}
		return b, nil
	}
	// duration returns the value of a key that is a time greater than 0, or def if it is not set.
	// example is def as an operator would write it.
	duration := func(k key, def time.Duration, example string) (time.Duration, error) {
		s := value(k)
		if s == "" {
			return def, nil
		}
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return 0, configError(fmt.Sprintf("%s: %q is not a duration greater than 0, such as %s", k, s, example))
		}
		return d, nil
	}

	var cfg config
	v, err := required(keySiteID)
	if err != nil {
		return config{}, err
	}
	if cfg.SiteID, err = contract.ParseSiteID(v); err != nil {
		return config{}, configError(fmt.Sprintf("%s: %v", keySiteID, err))
	}
	// The site ID goes into every site NATS subject (SPEC §4.2, ADR 0020).
	if _, err := sitenats.ForSite(cfg.SiteID); err != nil {
		return config{}, configError(fmt.Sprintf("%s: %v", keySiteID, err))
	}
	if cfg.COInsecure, err = boolean(keyCOInsecure); err != nil {
		return config{}, err
	}
	if cfg.COURL, err = required(keyCOURL); err != nil {
		return config{}, err
	}
	if err := checkCOURL(cfg.COURL, cfg.COInsecure); err != nil {
		return config{}, configError(fmt.Sprintf("%s: %v", keyCOURL, err))
	}
	cfg.CAFile = value(keyCAFile)
	if cfg.SiteToken, err = required(keySiteToken); err != nil {
		return config{}, err
	}
	if cfg.DataDir, err = required(keyDataDir); err != nil {
		return config{}, err
	}
	if cfg.PollInterval, err = duration(keyPollInterval, defaultPollInterval, "60s"); err != nil {
		return config{}, err
	}

	cfg.NATSURL, cfg.NATSUsername, cfg.NATSPassword = value(keyNATSURL), value(keyNATSUsername), getenv(keyNATSPassword.env)
	cfg.NATSCAFile = value(keyNATSCAFile)
	if cfg.NATSInsecure, err = boolean(keyNATSInsecure); err != nil {
		return config{}, err
	}
	// Without the CA file, which run checks: loadConfig reads no file.
	withoutCA := cfg.nats()
	withoutCA.CAFile = ""
	if err := natsConfigError(withoutCA.Check()); err != nil {
		return config{}, err
	}
	if cfg.AckTimeout, err = duration(keyAckTimeout, defaultAckTimeout, "5s"); err != nil {
		return config{}, err
	}
	if cfg.ReconcileInterval, err = duration(keyReconcileInterval, defaultReconcileInterval, "5m"); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// nats returns how the LO reaches the site NATS server (SPEC §15.6). The caller adds the logger.
func (c config) nats() sitenats.Config {
	return sitenats.Config{
		URL: c.NATSURL, Username: c.NATSUsername, Password: c.NATSPassword, CAFile: c.NATSCAFile, Insecure: c.NATSInsecure,
	}
}

// natsConfigError returns the error of sitenats.Config.Check as a configError that names the LO's
// key for the setting, or nil if err is nil. The text holds neither the URL nor the password: a
// *sitenats.ConfigError never does (SPEC §15.4).
func natsConfigError(err error) error {
	if err == nil {
		return nil
	}
	var ce *sitenats.ConfigError
	if !errors.As(err, &ce) {
		return configError(err.Error())
	}
	k := keyNATSURL
	switch ce.Field {
	case sitenats.FieldUsername:
		k = keyNATSUsername
	case sitenats.FieldPassword:
		k = keyNATSPassword
	case sitenats.FieldCAFile:
		k = keyNATSCAFile
	}
	reason := ce.Reason
	if strings.Contains(reason, "nats_insecure") {
		// The reason speaks of "the tier's nats_insecure setting"; name the LO's.
		reason += " (the setting is " + keyNATSInsecure.String() + ")"
	}
	return configError(fmt.Sprintf("%s: %s", k, reason))
}

// checkCOURL checks that u is a CO URL the LO may send its token to (SPEC §15.6).
func checkCOURL(u string, insecure bool) error {
	p, err := url.Parse(u)
	if err != nil {
		return errors.New("not a URL") // the parse error repeats u
	}
	switch {
	case p.Scheme == "http" && !insecure:
		return fmt.Errorf("an http:// URL sends the site token without TLS: use https://, or set %s to true", keyCOInsecure)
	case p.Scheme != "https" && p.Scheme != "http":
		return errors.New("the URL must start with https://")
	case p.Host == "":
		return errors.New("the URL has no host")
	case p.User != nil: // SPEC §15.4: credentials don't belong in a URL
		return errors.New("the URL must not hold a user name or password")
	case p.RawQuery != "" || p.ForceQuery || p.Fragment != "" || strings.Contains(u, "#"):
		return errors.New("the URL must not have a query or fragment: the LO appends the Margo API paths to it")
	}
	return nil
}
