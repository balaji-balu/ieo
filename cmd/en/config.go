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
	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/override"
	"github.com/balaji-balu/ieo/internal/en/pull"
	"github.com/balaji-balu/ieo/internal/sitenats"
)

// The defaults of SPEC §6.3.
const (
	defaultRuntime           = "docker"         // en.runtime (ADR 0017)
	defaultInventoryInterval = 60 * time.Second // en.inventory.interval
	defaultPullMaxBytes      = 268435456        // en.pull.max_bytes: 256 MiB
	defaultPullTimeout       = 5 * time.Minute  // en.pull.timeout
	defaultMaxExtractedBytes = 1073741824       // en.archive.max_extracted_bytes: 1 GiB
	defaultMaxEntries        = 10000            // en.archive.max_entries
	defaultStartTimeout      = 5 * time.Minute  // en.start_timeout
)

// config is the EN's configuration (SPEC §6.3). A config returned by loadConfig is valid.
type config struct {
	SiteID  contract.SiteID // en.site_id
	HostID  contract.HostID // en.host_id; empty means the one in host.id, or a new one (ADR 0016)
	DataDir string          // en.data_dir
	Runtime string          // en.runtime: docker or podman

	// The site NATS server until Appendix B step 4 (SPEC §15.6); sitenats.Config.Check accepts them.
	NATSURL      string // en.nats_url
	NATSInsecure bool   // en.nats_insecure: allows a nats:// NATSURL
	NATSUsername string // en.nats.username
	NATSPassword string // en.nats.password; never logged (§15.4)
	NATSCAFile   string // en.nats.ca_file: verifies the NATS server; empty means the system roots

	InventoryInterval time.Duration // en.inventory.interval, greater than 0
	OTel              override.OTel // en.otel.http_endpoint and en.otel.grpc_endpoint

	RegistryAuthFile string // en.registry.auth_file; empty means anonymous pulls (ADR 0021)
	RegistryInsecure bool   // en.registry.insecure

	Pull         pull.Limits    // en.pull.max_bytes and en.pull.timeout, both greater than 0
	Archive      archive.Limits // en.archive.max_entries and en.archive.max_extracted_bytes, both greater than 0
	StartTimeout time.Duration  // en.start_timeout, greater than 0
}

// configError is a configuration the EN cannot run with (SPEC §6.1).
type configError string

func (e configError) Error() string { return string(e) }

// key is one configuration key: its environment variable and, except for the NATS password, its
// flag.
type key struct {
	spec, env, flag string
}

var (
	keySiteID  = key{"en.site_id", "EN_SITE_ID", "site-id"}
	keyHostID  = key{"en.host_id", "EN_HOST_ID", "host-id"}
	keyDataDir = key{"en.data_dir", "EN_DATA_DIR", "data-dir"}
	keyRuntime = key{"en.runtime", "EN_RUNTIME", "runtime"}

	keyNATSURL      = key{"en.nats_url", "EN_NATS_URL", "nats-url"}
	keyNATSInsecure = key{"en.nats_insecure", "EN_NATS_INSECURE", "nats-insecure"}
	keyNATSUsername = key{"en.nats.username", "EN_NATS_USERNAME", "nats-username"}
	keyNATSCAFile   = key{"en.nats.ca_file", "EN_NATS_CA_FILE", "nats-ca-file"}
	// SPEC §6.3: the NATS password comes only from the environment, so it never shows in a
	// process listing (§15.4).
	keyNATSPassword = key{spec: "en.nats.password", env: "EN_NATS_PASSWORD"}

	keyInventoryInterval = key{"en.inventory.interval", "EN_INVENTORY_INTERVAL", "inventory-interval"}
	keyOTelHTTP          = key{"en.otel.http_endpoint", "EN_OTEL_HTTP_ENDPOINT", "otel-http-endpoint"}
	keyOTelGRPC          = key{"en.otel.grpc_endpoint", "EN_OTEL_GRPC_ENDPOINT", "otel-grpc-endpoint"}

	keyRegistryAuthFile = key{"en.registry.auth_file", "EN_REGISTRY_AUTH_FILE", "registry-auth-file"}
	keyRegistryInsecure = key{"en.registry.insecure", "EN_REGISTRY_INSECURE", "registry-insecure"}

	keyPullMaxBytes      = key{"en.pull.max_bytes", "EN_PULL_MAX_BYTES", "pull-max-bytes"}
	keyPullTimeout       = key{"en.pull.timeout", "EN_PULL_TIMEOUT", "pull-timeout"}
	keyMaxExtractedBytes = key{"en.archive.max_extracted_bytes", "EN_ARCHIVE_MAX_EXTRACTED_BYTES", "archive-max-extracted-bytes"}
	keyMaxEntries        = key{"en.archive.max_entries", "EN_ARCHIVE_MAX_ENTRIES", "archive-max-entries"}
	keyStartTimeout      = key{"en.start_timeout", "EN_START_TIMEOUT", "start-timeout"}
)

func (k key) String() string {
	if k.flag == "" {
		return fmt.Sprintf("%s (%s)", k.env, k.spec)
	}
	return fmt.Sprintf("%s or --%s (%s)", k.env, k.flag, k.spec)
}

// loadConfig reads the configuration from args and getenv: each key from its environment variable,
// overridden by its flag (SPEC §6.1). It validates every key, and returns a configError naming the
// first one that is missing or invalid. It reads no file.
func loadConfig(args []string, getenv func(string) string) (config, error) {
	fs := flag.NewFlagSet("en", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flags := map[key]*string{}
	for _, k := range []key{
		keySiteID, keyHostID, keyDataDir, keyRuntime, keyNATSURL, keyNATSUsername, keyNATSCAFile,
		keyInventoryInterval, keyOTelHTTP, keyOTelGRPC, keyRegistryAuthFile,
		keyPullMaxBytes, keyPullTimeout, keyMaxExtractedBytes, keyMaxEntries, keyStartTimeout,
	} {
		flags[k] = fs.String(k.flag, "", "")
	}
	boolFlags := map[key]*bool{}
	for _, k := range []key{keyNATSInsecure, keyRegistryInsecure} {
		boolFlags[k] = fs.Bool(k.flag, false, "")
	}
	if err := fs.Parse(args); err != nil {
		return config{}, configError(err.Error())
	}
	if fs.NArg() > 0 {
		return config{}, configError(fmt.Sprintf("unexpected argument %q: en takes only flags", fs.Arg(0)))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	r := reader{getenv: getenv, flags: flags, boolFlags: boolFlags, set: set}

	var cfg config
	var err error
	v, err := r.required(keySiteID)
	if err != nil {
		return config{}, err
	}
	if cfg.SiteID, err = contract.ParseSiteID(v); err != nil {
		return config{}, configError(fmt.Sprintf("%s: %v", keySiteID, err))
	}
	// The site and host IDs go into every site NATS subject (SPEC §4.2, ADR 0020).
	site, err := sitenats.ForSite(cfg.SiteID)
	if err != nil {
		return config{}, configError(fmt.Sprintf("%s: %v", keySiteID, err))
	}
	if v := r.value(keyHostID); v != "" {
		if cfg.HostID, err = contract.ParseHostID(v); err == nil {
			_, err = site.Host(cfg.HostID)
		}
		if err != nil {
			return config{}, configError(fmt.Sprintf("%s: %v", keyHostID, err))
		}
	}
	if cfg.DataDir, err = r.required(keyDataDir); err != nil {
		return config{}, err
	}
	cfg.Runtime = r.value(keyRuntime)
	switch cfg.Runtime {
	case "":
		cfg.Runtime = defaultRuntime
	case "docker", "podman":
	default:
		return config{}, configError(fmt.Sprintf("%s: %q is not docker or podman", keyRuntime, cfg.Runtime))
	}

	cfg.NATSURL, cfg.NATSUsername, cfg.NATSPassword = r.value(keyNATSURL), r.value(keyNATSUsername), getenv(keyNATSPassword.env)
	cfg.NATSCAFile = r.value(keyNATSCAFile)
	if cfg.NATSInsecure, err = r.boolean(keyNATSInsecure); err != nil {
		return config{}, err
	}
	// Without the CA file, which run checks: loadConfig reads no file.
	withoutCA := cfg.nats()
	withoutCA.CAFile = ""
	if err := natsConfigError(withoutCA.Check()); err != nil {
		return config{}, err
	}

	if cfg.InventoryInterval, err = r.duration(keyInventoryInterval, defaultInventoryInterval, "60s"); err != nil {
		return config{}, err
	}
	if cfg.OTel.HTTPEndpoint, err = r.required(keyOTelHTTP); err != nil {
		return config{}, err
	}
	cfg.OTel.GRPCEndpoint = r.value(keyOTelGRPC)
	for _, e := range []struct {
		k key
		u string
	}{{keyOTelHTTP, cfg.OTel.HTTPEndpoint}, {keyOTelGRPC, cfg.OTel.GRPCEndpoint}} {
		if err := checkEndpoint(e.u); err != nil {
			return config{}, configError(fmt.Sprintf("%s: %v", e.k, err))
		}
	}

	cfg.RegistryAuthFile = r.value(keyRegistryAuthFile)
	if cfg.RegistryInsecure, err = r.boolean(keyRegistryInsecure); err != nil {
		return config{}, err
	}
	if cfg.Pull.MaxBytes, err = r.size(keyPullMaxBytes, defaultPullMaxBytes); err != nil {
		return config{}, err
	}
	if cfg.Pull.Timeout, err = r.duration(keyPullTimeout, defaultPullTimeout, "5m"); err != nil {
		return config{}, err
	}
	if cfg.Archive.MaxExtractedBytes, err = r.size(keyMaxExtractedBytes, defaultMaxExtractedBytes); err != nil {
		return config{}, err
	}
	entries, err := r.size(keyMaxEntries, defaultMaxEntries)
	if err != nil {
		return config{}, err
	}
	cfg.Archive.MaxEntries = int(entries)
	if cfg.StartTimeout, err = r.duration(keyStartTimeout, defaultStartTimeout, "5m"); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// reader reads the value of each key: its flag if it was given, its environment variable
// otherwise.
type reader struct {
	getenv    func(string) string
	flags     map[key]*string
	boolFlags map[key]*bool
	set       map[string]bool
}

func (r reader) value(k key) string {
	if r.set[k.flag] {
		return *r.flags[k]
	}
	return r.getenv(k.env)
}

func (r reader) required(k key) (string, error) {
	v := r.value(k)
	if v == "" {
		return "", configError(k.String() + " is not set")
	}
	return v, nil
}

// boolean returns the value of a key that is false unless it is set.
func (r reader) boolean(k key) (bool, error) {
	if r.set[k.flag] {
		return *r.boolFlags[k], nil
	}
	s := r.getenv(k.env)
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
func (r reader) duration(k key, def time.Duration, example string) (time.Duration, error) {
	s := r.value(k)
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, configError(fmt.Sprintf("%s: %q is not a duration greater than 0, such as %s", k, s, example))
	}
	return d, nil
}

// size returns the value of a key that is a whole number greater than 0, or def if it is not set.
func (r reader) size(k key, def int64) (int64, error) {
	s := r.value(k)
	if s == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, configError(fmt.Sprintf("%s: %q is not a whole number greater than 0, such as %d", k, s, def))
	}
	return n, nil
}

// checkEndpoint checks an OpenTelemetry endpoint: an http or https URL with a host, or nothing.
// It is passed to workloads as it is (SPEC §9.3), so it must not hold a user name or password.
func checkEndpoint(u string) error {
	if u == "" {
		return nil
	}
	p, err := url.Parse(u)
	switch {
	case err != nil:
		return errors.New("not a URL") // the parse error repeats u
	case p.Scheme != "http" && p.Scheme != "https":
		return errors.New("the URL must start with http:// or https://")
	case p.Host == "":
		return errors.New("the URL has no host")
	case p.User != nil:
		return errors.New("the URL must not hold a user name or password")
	}
	return nil
}

// nats returns how the EN reaches the site NATS server (SPEC §15.6). The caller adds the logger.
func (c config) nats() sitenats.Config {
	return sitenats.Config{
		URL: c.NATSURL, Username: c.NATSUsername, Password: c.NATSPassword, CAFile: c.NATSCAFile, Insecure: c.NATSInsecure,
	}
}

// natsConfigError returns the error of sitenats.Config.Check as a configError that names the EN's
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
		// The reason speaks of "the tier's nats_insecure setting"; name the EN's.
		reason += " (the setting is " + keyNATSInsecure.String() + ")"
	}
	return configError(fmt.Sprintf("%s: %s", k, reason))
}
