// Package plan decides which commands the LO sends to one host: it compares the deployments
// resolved to the host with what the host last reported, and returns the Apply and Remove commands
// that converge them (SPEC §8.5 steps 1–5, §16.4).
//
// The planner is pure. It does no I/O and changes no state, so planning never changes actual state
// (SPEC §7.6). Choosing which hosts to reconcile (liveness), resolving targets, skipping retries
// that are not yet due, and sending the commands are the caller's job.
package plan

import (
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Actual is one deployment as its host last reported it (Host Actual State, SPEC §4.1.8).
type Actual struct {
	Digest contract.Digest
	// Components maps each reported component name to its state.
	Components map[string]contract.ComponentState
}

// Command is one command to send to the host. It is not the wire message: the caller adds the
// command ID and, for an Apply, the deployment.
type Command struct {
	Action       contract.Action
	DeploymentID uuid.UUID
	// Digest is the wanted digest for an Apply, and the digest the host reported for a Remove.
	Digest contract.Digest
}

// ForHost returns the commands that converge one host from have to want (SPEC §8.5 steps 3–5):
//
//   - Apply for each deployment in want that is absent from have, present at another digest, or
//     present at the same digest with any component failed;
//   - Remove for each deployment in have that is not in want;
//   - nothing for the rest.
//
// want maps each deployment resolved to the host to its desired digest; have is the host's actual
// state. The result lists the Applies, then the Removes, each sorted by deployment ID; it is empty
// when nothing needs to change. ForHost does not modify its arguments.
func ForHost(want map[uuid.UUID]contract.Digest, have map[uuid.UUID]Actual) []Command {
	return nil
}
