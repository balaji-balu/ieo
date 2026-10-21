package catalog

import (
	"log/slog"

	"github.com/balaji-balu/ieo/internal/ociremote"
)

// NewRegistry returns the client the CO imports through; its Open is an OpenRepository. It
// reaches registries anonymously, sending no credential to any host (SPEC §5.3 `[IEO, interim]`),
// over HTTPS with the certificate verified against the system roots unless insecure
// (`co.registry.insecure`, SPEC §6.3). Redirects and the environment's proxy settings are
// followed as the EN follows them (ADR 0021). If insecure is set it logs a warning to log.
func NewRegistry(insecure bool, log *slog.Logger) *ociremote.Client {
	return ociremote.New(registryOptions(insecure, log))
}

func registryOptions(insecure bool, log *slog.Logger) ociremote.Options {
	return ociremote.Options{
		Insecure:    insecure,
		InsecureKey: "co.registry.insecure",
		Credential:  nil, // SPEC §5.3: anonymous until CO registry credentials are specified
		UserAgent:   "ieo-co",
		Log:         log,
	}
}
