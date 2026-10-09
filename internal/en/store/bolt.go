package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"

	"github.com/balaji-balu/ieo/internal/contract"
)

// StateFile is the name of the store file in the EN's data directory (SPEC §9.1).
const StateFile = "state.db"

// The layout of the store file (ADR 0016).
var (
	bucketMeta       = []byte("meta")
	bucketApplied    = []byte("applied")          // deployment ID → appliedRecord
	bucketComponents = []byte("component_states") // deployment ID → nested bucket: name → ComponentStatus
	keySchema        = []byte("schema")           // in meta: the layout version, 8 bytes big-endian
)

// schema is the layout version this code reads and writes.
const schema = 1

// lockTimeout is how long Open waits for another process to release the file.
const lockTimeout = time.Second

// appliedRecord is one deployment's applied entry, as JSON in the applied bucket (ADR 0016).
type appliedRecord struct {
	Digest          contract.Digest `json:"digest"`
	ComposeProjects []string        `json:"composeProjects"`
}

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
	path := filepath.Join(dataDir, StateFile)
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: lockTimeout})
	if err != nil {
		return nil, fmt.Errorf("open EN store %s: %w", path, err)
	}
	if err := db.Update(initLayout); err != nil {
		_ = db.Close() // the layout error is the one to report
		return nil, fmt.Errorf("open EN store %s: %w; to recover, bring down the host's IEO Compose projects, then remove the file (ADR 0016)", path, err)
	}
	return &Bolt{db: db}, nil
}

// initLayout creates the layout in an empty file, checks it in any other, and deletes component
// states that have no applied record. A refused file is left unchanged: the transaction rolls back.
func initLayout(tx *bolt.Tx) error {
	meta := tx.Bucket(bucketMeta)
	if meta == nil {
		empty := true
		_ = tx.ForEach(func([]byte, *bolt.Bucket) error { empty = false; return nil }) // fn never fails
		if !empty {
			return errors.New("not an EN store: it has buckets but no layout version")
		}
		return create(tx)
	}
	v, err := u64(meta.Get(keySchema))
	if err != nil {
		return fmt.Errorf("layout version: %w", err)
	}
	if v != schema {
		return fmt.Errorf("layout version %d, but this EN reads only version %d", v, schema)
	}
	applied, components := tx.Bucket(bucketApplied), tx.Bucket(bucketComponents)
	if applied == nil || components == nil {
		return errors.New("a bucket is missing")
	}
	var orphans [][]byte
	err = components.ForEachBucket(func(k []byte) error {
		if applied.Get(k) == nil {
			orphans = append(orphans, k)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range orphans {
		if err := components.DeleteBucket(k); err != nil {
			return fmt.Errorf("delete component states of %s: %w", k, err)
		}
	}
	return nil
}

// create writes the layout of an empty store.
func create(tx *bolt.Tx) error {
	meta, err := tx.CreateBucket(bucketMeta)
	if err != nil {
		return err
	}
	if _, err := tx.CreateBucket(bucketApplied); err != nil {
		return err
	}
	if _, err := tx.CreateBucket(bucketComponents); err != nil {
		return err
	}
	return meta.Put(keySchema, binary.BigEndian.AppendUint64(nil, schema))
}

// Close closes the store file.
func (b *Bolt) Close() error { return b.db.Close() }

// Load returns every deployment with an Applied record, keyed by deployment ID. Component states
// of a deployment with no Applied record yet (an Apply in flight) are not returned, so a caller
// never sees a deployment without a digest. Load fails on any record it cannot decode, and never
// returns a partial result (ADR 0016).
func (b *Bolt) Load(_ context.Context) (map[uuid.UUID]Deployment, error) {
	out := map[uuid.UUID]Deployment{}
	err := b.db.View(func(tx *bolt.Tx) error {
		applied, components := tx.Bucket(bucketApplied), tx.Bucket(bucketComponents)
		err := applied.ForEach(func(k, v []byte) error {
			id, err := deploymentID(k)
			if err != nil {
				return err
			}
			var r appliedRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("applied record of %s: %w", id, err)
			}
			if r.Digest == "" {
				return fmt.Errorf("applied record of %s: no digest", id)
			}
			if r.ComposeProjects == nil {
				r.ComposeProjects = []string{}
			}
			out[id] = Deployment{
				Applied:    Applied(r),
				Components: map[string]contract.ComponentStatus{},
			}
			return nil
		})
		if err != nil {
			return err
		}
		return components.ForEach(func(k, v []byte) error {
			id, err := deploymentID(k)
			if err != nil {
				return err
			}
			if v != nil {
				return fmt.Errorf("component states of %s: not a bucket", id)
			}
			d, ok := out[id]
			if !ok {
				return nil // an Apply in flight that has recorded no project yet
			}
			return components.Bucket(k).ForEach(func(name, v []byte) error {
				s, err := decodeStatus(name, v)
				if err != nil {
					return fmt.Errorf("component states of %s: %w", id, err)
				}
				d.Components[s.Name] = s
				return nil
			})
		})
	})
	if err != nil {
		return nil, fmt.Errorf("load EN store: %w", err)
	}
	return out, nil
}

// deploymentID decodes a bucket key, which is a deployment ID in its canonical lowercase form.
func deploymentID(k []byte) (uuid.UUID, error) {
	id, err := uuid.Parse(string(k))
	if err != nil || id.String() != string(k) {
		return uuid.UUID{}, fmt.Errorf("key %q is not a deployment ID", k)
	}
	return id, nil
}

// decodeStatus decodes the status stored under component name.
func decodeStatus(name, v []byte) (contract.ComponentStatus, error) {
	var s contract.ComponentStatus
	if err := json.Unmarshal(v, &s); err != nil {
		return s, fmt.Errorf("component %q: %w", name, err)
	}
	if s.Name != string(name) {
		return s, fmt.Errorf("component %q: record names %q", name, s.Name)
	}
	switch s.State {
	case contract.StatePending, contract.StateInstalling, contract.StateInstalled,
		contract.StateRemoving, contract.StateRemoved, contract.StateFailed:
		return s, nil
	default:
		return s, fmt.Errorf("component %q: unknown state %q", name, s.State)
	}
}

// PutApplied records a, replacing the deployment's previous Applied record (SPEC §8.9 steps 4.4
// and 6). Its component states are kept.
func (b *Bolt) PutApplied(_ context.Context, id uuid.UUID, a Applied) error {
	projects := a.ComposeProjects
	if projects == nil {
		projects = []string{} // ADR 0016: an array
	}
	v, err := json.Marshal(appliedRecord{Digest: a.Digest, ComposeProjects: projects})
	if err != nil {
		return fmt.Errorf("record applied %s: %w", id, err)
	}
	err = b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketApplied).Put([]byte(id.String()), v)
	})
	if err != nil {
		return fmt.Errorf("record applied %s: %w", id, err)
	}
	return nil
}

// PutComponentStatus records s as the status of component s.Name of the deployment, replacing
// the previous one (SPEC §4.1.12). The EN calls it before it publishes s (ADR 0016).
func (b *Bolt) PutComponentStatus(_ context.Context, id uuid.UUID, s contract.ComponentStatus) error {
	v, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("record status of %s component %q: %w", id, s.Name, err)
	}
	err = b.db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.Bucket(bucketComponents).CreateBucketIfNotExists([]byte(id.String()))
		if err != nil {
			return err
		}
		return bk.Put([]byte(s.Name), v)
	})
	if err != nil {
		return fmt.Errorf("record status of %s component %q: %w", id, s.Name, err)
	}
	return nil
}

// DeleteDeployment deletes the deployment's Applied record and component states in one
// transaction (SPEC §8.9 Remove step 2). Deleting a deployment that is not stored succeeds.
func (b *Bolt) DeleteDeployment(_ context.Context, id uuid.UUID) error {
	k := []byte(id.String())
	err := b.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketApplied).Delete(k); err != nil {
			return err
		}
		err := tx.Bucket(bucketComponents).DeleteBucket(k)
		if errors.Is(err, bolterrors.ErrBucketNotFound) {
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("delete deployment %s: %w", id, err)
	}
	return nil
}

// u64 decodes an 8-byte big-endian value.
func u64(b []byte) (uint64, error) {
	if len(b) != 8 {
		return 0, fmt.Errorf("%d bytes, want 8", len(b))
	}
	return binary.BigEndian.Uint64(b), nil
}
