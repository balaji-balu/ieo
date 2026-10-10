package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

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
	// Deployments lists what the host runs: each deployment once, with an ID and a valid digest,
	// and each of its components once, with a name and a state of SPEC §4.1.9. In a HostActual a
	// store returns, this list and every deployment's Components are empty, not nil, when they
	// hold nothing.
	Deployments []contract.InventoryDeployment
}

// HostState is everything a store holds for one host.
type HostState struct {
	Host Host
	// Actual is nil until the host's actual state has been put once.
	Actual *HostActual
}

// hostRecord is one host in the hosts bucket, as JSON (ADR 0015). Every key is always written, so
// decodeHosts treats a missing one as damage.
type hostRecord struct {
	Capabilities    *contract.DeviceCapabilities `json:"capabilities"`
	Labels          map[string]string            `json:"labels"`
	LastHeartbeatAt *time.Time                   `json:"lastHeartbeatAt"`
}

// actualRecord is one host's actual state in the actual bucket, as JSON (ADR 0015).
type actualRecord struct {
	ReportedAt  time.Time                      `json:"reportedAt"`
	Deployments []contract.InventoryDeployment `json:"deployments"`
}

// newHostRecord returns the record of a host of which only actual state is known.
func newHostRecord() []byte {
	return []byte(`{"capabilities":null,"labels":{},"lastHeartbeatAt":null}`)
}

// encodeActual returns the key and record of host's actual state a, or an error if either is not
// valid: decodeHosts refuses the same things, so nothing is written that cannot be read back.
func encodeActual(host contract.HostID, a HostActual) (key, value []byte, err error) {
	if _, err := contract.ParseHostID(host.String()); err != nil {
		return nil, nil, err
	}
	// Round drops the monotonic clock reading, which is not part of the instant.
	r := actualRecord{ReportedAt: a.ReportedAt.UTC().Round(0), Deployments: a.Deployments}
	if r.Deployments == nil {
		r.Deployments = []contract.InventoryDeployment{}
	}
	if err := checkActual(r); err != nil {
		return nil, nil, err
	}
	value, err = json.Marshal(r) // a deployment's nil Components is written as `[]`
	if err != nil {
		return nil, nil, err
	}
	return []byte(host), value, nil
}

// checkActual checks what HostActual requires of its fields.
func checkActual(r actualRecord) error {
	if r.ReportedAt.IsZero() {
		return errors.New("no report time")
	}
	if r.Deployments == nil {
		return errors.New("no list of deployments")
	}
	deployments := make(map[uuid.UUID]bool, len(r.Deployments))
	for _, d := range r.Deployments {
		if d.DeploymentID == uuid.Nil {
			return errors.New("a deployment has no ID")
		}
		if deployments[d.DeploymentID] {
			return fmt.Errorf("deployment %s is listed twice", d.DeploymentID)
		}
		deployments[d.DeploymentID] = true
		if _, err := contract.ParseDigest(d.Digest.String()); err != nil {
			return fmt.Errorf("deployment %s: %w", d.DeploymentID, err)
		}
		components := make(map[string]bool, len(d.Components))
		for _, c := range d.Components {
			switch {
			case c.Name == "":
				return fmt.Errorf("deployment %s: a component has no name", d.DeploymentID)
			case components[c.Name]:
				return fmt.Errorf("deployment %s: component %q is listed twice", d.DeploymentID, c.Name)
			case !knownState(c.State):
				return fmt.Errorf("deployment %s: component %q has unknown state %q", d.DeploymentID, c.Name, c.State)
			}
			components[c.Name] = true
		}
	}
	return nil
}

// knownState reports whether s is a component state of SPEC §4.1.9.
func knownState(s contract.ComponentState) bool {
	switch s {
	case contract.StatePending, contract.StateInstalling, contract.StateInstalled,
		contract.StateRemoving, contract.StateRemoved, contract.StateFailed:
		return true
	}
	return false
}

// records calls fn with the key and value of every record of one bucket, and stops at the first
// error, which it returns. bbolt's Bucket.ForEach is one.
type records func(fn func(k, v []byte) error) error

// decodeHosts returns the hosts held in the records of the hosts and actual buckets. It fails on
// the first record it cannot trust (see Bolt.LoadHosts). It keeps no key or value it is given.
func decodeHosts(hosts, actual records) (map[contract.HostID]HostState, error) {
	out := map[contract.HostID]HostState{}
	err := hosts(func(k, v []byte) error {
		id, err := contract.ParseHostID(string(k))
		if err != nil {
			return fmt.Errorf("hosts key %q: %w", k, err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(v, &keys); err != nil {
			return fmt.Errorf("host %s: %w", id, err)
		}
		for _, name := range []string{"capabilities", "labels", "lastHeartbeatAt"} {
			if _, ok := keys[name]; !ok {
				return fmt.Errorf("host %s: no %s", id, name)
			}
		}
		var r hostRecord
		if err := json.Unmarshal(v, &r); err != nil {
			return fmt.Errorf("host %s: %w", id, err)
		}
		if r.Labels == nil {
			return fmt.Errorf("host %s: no labels", id)
		}
		if r.Capabilities != nil && r.Capabilities.ID.Site == "" {
			return fmt.Errorf("host %s: capabilities without a device ID", id)
		}
		h := Host{Capabilities: r.Capabilities, Labels: r.Labels}
		if r.LastHeartbeatAt != nil {
			h.LastHeartbeatAt = r.LastHeartbeatAt.UTC()
		}
		out[id] = HostState{Host: h}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = actual(func(k, v []byte) error {
		id := contract.HostID(k)
		state, ok := out[id]
		if !ok { // ADR 0015: `actual` never holds a host that `hosts` does not
			return fmt.Errorf("actual state for host %q, which is not in hosts", k)
		}
		var r actualRecord
		if err := json.Unmarshal(v, &r); err != nil {
			return fmt.Errorf("actual state of host %s: %w", id, err)
		}
		if err := checkActual(r); err != nil {
			return fmt.Errorf("actual state of host %s: %w", id, err)
		}
		for _, d := range r.Deployments {
			if d.Components == nil {
				return fmt.Errorf("actual state of host %s: deployment %s: no list of components", id, d.DeploymentID)
			}
		}
		state.Actual = &HostActual{ReportedAt: r.ReportedAt.UTC(), Deployments: r.Deployments}
		out[id] = state
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
