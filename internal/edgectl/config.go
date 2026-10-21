package edgectl

import (
	"fmt"
	"strconv"

	"github.com/spf13/pflag"

	"github.com/balaji-balu/ieo/internal/bearer"
)

// config is edgectl's configuration (SPEC §6.3). A config returned by loadConfig is valid.
type config struct {
	COURL      string // edgectl.co_url: https, or http when COInsecure, with a host
	COInsecure bool   // edgectl.co_insecure: allows an http COURL (SPEC §15.6)
	CAFile     string // edgectl.tls.ca_file: verifies the CO; empty means the system roots
	Token      string // edgectl.token (SPEC §15.6); never printed (§15.4)
}

// key is one configuration key: its environment variable and, except for the token, its flag.
type key struct {
	spec, env, flag string
}

var (
	keyCOURL      = key{"edgectl.co_url", "EDGECTL_CO_URL", "co-url"}
	keyCOInsecure = key{"edgectl.co_insecure", "EDGECTL_CO_INSECURE", "co-insecure"}
	keyCAFile     = key{"edgectl.tls.ca_file", "EDGECTL_TLS_CA_FILE", "tls-ca-file"}
	// SPEC §6.3: the token comes only from the environment, never from a flag, so it never shows
	// in a process listing or a shell history (§15.4). edgectl reads no configuration file.
	keyToken = key{spec: "edgectl.token", env: "EDGECTL_TOKEN"}
)

func (k key) String() string {
	if k.flag == "" {
		return fmt.Sprintf("%s (%s)", k.env, k.spec)
	}
	return fmt.Sprintf("%s or --%s (%s)", k.env, k.flag, k.spec)
}

// addConfigFlags adds the flags of the configuration keys to fs.
func addConfigFlags(fs *pflag.FlagSet) {
	fs.String(keyCOURL.flag, "", "the CO operator API's base URL (default $"+keyCOURL.env+")")
	fs.Bool(keyCOInsecure.flag, false, "allow an http:// CO URL: the token is sent without TLS (default $"+keyCOInsecure.env+")")
	fs.String(keyCAFile.flag, "", "PEM file of the CA that verifies the CO, instead of the system roots (default $"+keyCAFile.env+")")
}

// loadConfig reads the configuration from the flags in fs and getenv: each key from its
// environment variable, overridden by its flag. It returns a usageError naming the first key that
// is missing or invalid; the error never holds the token or the URL (SPEC §15.4).
func loadConfig(fs *pflag.FlagSet, getenv func(string) string) (config, error) {
	value := func(k key) string {
		if f := fs.Lookup(k.flag); f != nil && f.Changed {
			return f.Value.String()
		}
		return getenv(k.env)
	}
	var cfg config
	if s := value(keyCOInsecure); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return config{}, usageError(fmt.Sprintf("%s: %q is not true or false", keyCOInsecure, s))
		}
		cfg.COInsecure = b
	}
	if cfg.COURL = value(keyCOURL); cfg.COURL == "" {
		return config{}, usageError(keyCOURL.String() + " is not set")
	}
	if err := bearer.CheckURL(cfg.COURL, cfg.COInsecure, keyCOInsecure.String()); err != nil {
		return config{}, usageError(fmt.Sprintf("%s: %v", keyCOURL, err))
	}
	cfg.CAFile = value(keyCAFile)
	if cfg.Token = getenv(keyToken.env); cfg.Token == "" {
		return config{}, usageError(keyToken.String() + " is not set")
	}
	return cfg, nil
}
