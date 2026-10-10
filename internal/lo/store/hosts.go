package store

import (
	"time"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Host is what the LO knows about one host apart from what runs on it (SPEC §4.1.2, §4.1.11;
// `hosts[h]` in ADR 0015).
type Host struct {
	// Capabilities is the host's last reported capabilities; nil means none has been received.
	Capabilities *contract.DeviceCapabilities
	// Labels are the host's last reported labels; never nil in a Host a store returns.
	Labels map[string]string
	// LastHeartbeatAt is when the host's last heartbeat was sent; zero means none has been
	// received.
	LastHeartbeatAt time.Time
}

// HostActual is one host's actual state as it last reported it (SPEC §4.1.8; `actual[h]` in ADR
// 0015).
type HostActual struct {
	// ReportedAt is the `at` of the report the state comes from. It must not be zero.
	ReportedAt time.Time
	// Deployments lists what the host runs: each deployment once, with a valid digest, and each of
	// its components once, with a name and a state of SPEC §4.1.9.
	Deployments []contract.InventoryDeployment
}

// HostState is everything a store holds for one host.
type HostState struct {
	Host Host
	// Actual is nil until the host's actual state has been put once.
	Actual *HostActual
}
