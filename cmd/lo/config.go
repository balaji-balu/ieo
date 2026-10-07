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
)

const defaultPollInterval = 60 * time.Second // SPEC §6.3 lo.poll.interval

// config is the LO's configuration (SPEC §6.3). A config returned by loadConfig is valid.
type config struct {
	SiteID       contract.SiteID // lo.site_id
	COURL        string          // lo.co_url: http or https, with a host, and no user info, query or fragment
	COInsecure   bool            // lo.co_insecure: allows an http COURL (SPEC §15.6)
	CAFile       string          // lo.tls.ca_file: verifies the CO; empty means the system roots
	SiteToken    string          // lo.site_token (SPEC §15.6); never logged (§15.4)
	DataDir      string          // lo.data_dir
	PollInterval time.Duration   // lo.poll.interval, greater than 0
	LegacyPort   string          // LO_PORT: the old LO runs only when it is set (legacy.go)
}

// configError is a configuration the LO cannot run with (SPEC §6.1).
type configError string

func (e configError) Error() string { return string(e) }

// key is one configuration key: its environment variable and, except for the token, its flag.
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
	for _, k := range []key{keySiteID, keyCOURL, keyCAFile, keyDataDir, keyPollInterval} {
		flags[k] = fs.String(k.flag, "", "")
	}
	insecureFlag := fs.Bool(keyCOInsecure.flag, false, "")
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

	var cfg config
	v, err := required(keySiteID)
	if err != nil {
		return config{}, err
	}
	if cfg.SiteID, err = contract.ParseSiteID(v); err != nil {
		return config{}, configError(fmt.Sprintf("%s: %v", keySiteID, err))
	}
	if set[keyCOInsecure.flag] {
		cfg.COInsecure = *insecureFlag
	} else if s := getenv(keyCOInsecure.env); s != "" {
		if cfg.COInsecure, err = strconv.ParseBool(s); err != nil {
			return config{}, configError(fmt.Sprintf("%s: %q is not true or false", keyCOInsecure, s))
		}
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
	cfg.PollInterval = defaultPollInterval
	if s := value(keyPollInterval); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return config{}, configError(fmt.Sprintf("%s: %q is not a duration greater than 0, such as 60s", keyPollInterval, s))
		}
		cfg.PollInterval = d
	}
	cfg.LegacyPort = getenv("LO_PORT")
	return cfg, nil
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
