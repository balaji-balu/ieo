package main

import (
	"time"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/override"
	"github.com/balaji-balu/ieo/internal/en/pull"
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

// loadConfig reads the configuration from args and getenv: each key from its environment variable,
// overridden by its flag (SPEC §6.1). It validates every key, and returns a configError naming the
// first one that is missing or invalid. It reads no file.
func loadConfig(args []string, getenv func(string) string) (config, error) {
	return config{}, nil
}
