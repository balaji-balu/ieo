package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"github.com/balaji-balu/ieo/internal/contract"
)

// StateFile is the name of the store file in the EN's data directory (SPEC §9.1).
const StateFile = "state.db"

// Applied is what the EN has applied of one deployment (SPEC §4.1.12): the digest of its last
// Apply and the Compose projects it brought up.
type Applied struct {
	Digest          contract.Digest
	ComposeProjects []string
}

// Deployment is one stored deployment: its Applied record and the last status of each of its
// components, keyed by component name.
type Deployment struct {
	Applied    Applied
	Components map[string]contract.ComponentStatus
}

// Bolt is the EN's durable store: an embedded bbolt file laid out as ADR 0016 says (SPEC §12).
// It is safe for concurrent use. Every write is one transaction, synced to disk when it commits.
type Bolt struct {
	db *bolt.DB
}

// Open opens <dataDir>/state.db, creating it if it does not exist, with mode 0600 (on Windows the
// file's access comes from its directory instead). It fails if another process has the file open,
// or if the file is not an EN store of a layout this code knows; it never deletes or overwrites
// such a file (ADR 0016).
//
// Open deletes the component states of every deployment with no Applied record: an Apply that
// stopped before it recorded any project, so none of its projects is up (ADR 0016).
func Open(dataDir string) (*Bolt, error) {
	return nil, errors.New("not implemented")
}

// Close closes the store file.
func (b *Bolt) Close() error { return b.db.Close() }

// Load returns every deployment with an Applied record, keyed by deployment ID. Component states
// of a deployment with no Applied record yet (an Apply in flight) are not returned, so a caller
// never sees a deployment without a digest. Load fails on any record it cannot decode, and never
// returns a partial result (ADR 0016).
func (b *Bolt) Load(_ context.Context) (map[uuid.UUID]Deployment, error) {
	return nil, errors.New("not implemented")
}

// PutApplied records a, replacing the deployment's previous Applied record (SPEC §8.9 steps 4.4
// and 6). Its component states are kept.
func (b *Bolt) PutApplied(_ context.Context, id uuid.UUID, a Applied) error {
	return errors.New("not implemented")
}

// PutComponentStatus records s as the status of component s.Name of the deployment, replacing
// the previous one (SPEC §4.1.12). The EN calls it before it publishes s (ADR 0016).
func (b *Bolt) PutComponentStatus(_ context.Context, id uuid.UUID, s contract.ComponentStatus) error {
	return errors.New("not implemented")
}

// DeleteDeployment deletes the deployment's Applied record and component states in one
// transaction (SPEC §8.9 Remove step 2). Deleting a deployment that is not stored succeeds.
func (b *Bolt) DeleteDeployment(_ context.Context, id uuid.UUID) error {
	return errors.New("not implemented")
}
