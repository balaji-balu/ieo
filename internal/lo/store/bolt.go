package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"github.com/balaji-balu/ieo/internal/contract"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

var _ losync.Store = (*Bolt)(nil)

// The layout of the store file (ADR 0014). Later slices add buckets beside these.
var (
	bucketMeta    = []byte("meta")
	bucketSync    = []byte("sync")
	bucketDesired = []byte("desired")
	keySchema     = []byte("schema")  // in meta: the layout version, 8 bytes big-endian
	keyVersion    = []byte("version") // in sync: accepted_manifest_version, 8 bytes big-endian
	keyETag       = []byte("etag")    // in sync: the accepted manifest's ETag
)

// schema is the layout version this code reads and writes.
const schema = 1

// lockTimeout is how long OpenBolt waits for another process to release the file.
const lockTimeout = time.Second

// record is one deployment of desired state, as JSON in the desired bucket (ADR 0014).
type record struct {
	Digest                 contract.Digest          `json:"digest"`
	AdoptedManifestVersion contract.ManifestVersion `json:"adoptedManifestVersion"`
	YAML                   []byte                   `json:"yaml"`
}

// Bolt is the LO's durable store: an embedded bbolt file laid out as ADR 0014 says (SPEC §12).
// It is safe for concurrent use. Every write is one transaction, synced to disk when it commits.
type Bolt struct {
	db *bolt.DB
}

// OpenBolt opens the store file at path, creating it if it does not exist, with mode 0600 (on
// Windows the file's access comes from its directory instead). It fails if another process has the file open, or if the file is not an LO store of a
// layout this code knows.
func OpenBolt(path string) (*Bolt, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: lockTimeout})
	if err != nil {
		return nil, fmt.Errorf("open LO store %s: %w", path, err)
	}
	if err := db.Update(initLayout); err != nil {
		_ = db.Close() // the layout error is the one to report
		return nil, fmt.Errorf("open LO store %s: %w", path, err)
	}
	return &Bolt{db: db}, nil
}

// initLayout creates the layout in an empty file, and checks it in any other.
func initLayout(tx *bolt.Tx) error {
	meta := tx.Bucket(bucketMeta)
	if meta == nil {
		empty := true
		_ = tx.ForEach(func([]byte, *bolt.Bucket) error { empty = false; return nil }) // fn never fails
		if !empty {
			return errors.New("not an LO store: it has buckets but no layout version (ADR 0014)")
		}
		return create(tx)
	}
	v, err := u64(meta.Get(keySchema))
	if err != nil {
		return fmt.Errorf("layout version: %w", err)
	}
	if v != schema {
		return fmt.Errorf("layout version %d, but this LO reads only version %d (ADR 0014)", v, schema)
	}
	return nil
}

// create writes the layout of an empty store. The version and ETag are written too, so that a
// missing key is damage rather than a store that has accepted nothing.
func create(tx *bolt.Tx) error {
	meta, err := tx.CreateBucket(bucketMeta)
	if err != nil {
		return err
	}
	syncBucket, err := tx.CreateBucket(bucketSync)
	if err != nil {
		return err
	}
	if _, err := tx.CreateBucket(bucketDesired); err != nil {
		return err
	}
	return errors.Join(
		meta.Put(keySchema, binary.BigEndian.AppendUint64(nil, schema)),
		syncBucket.Put(keyVersion, binary.BigEndian.AppendUint64(nil, 0)),
		syncBucket.Put(keyETag, []byte{}),
	)
}

// Close closes the store file.
func (b *Bolt) Close() error { return b.db.Close() }

// Load returns the stored state. It fails on any record it cannot trust, including a YAML that
// does not match its digest (SPEC §15.3), and never returns an empty state in its place: that
// would reset the accepted version and turn rollback protection off (SPEC §12).
func (b *Bolt) Load(_ context.Context) (losync.State, error) {
	var st losync.State
	err := b.db.View(func(tx *bolt.Tx) error {
		syncBucket, desired := tx.Bucket(bucketSync), tx.Bucket(bucketDesired)
		if syncBucket == nil || desired == nil {
			return errors.New("a bucket is missing")
		}
		v, err := u64(syncBucket.Get(keyVersion))
		if err != nil {
			return fmt.Errorf("accepted manifest version: %w", err)
		}
		etag := syncBucket.Get(keyETag)
		if etag == nil {
			return errors.New("ETag is missing")
		}
		st = losync.State{Version: contract.ManifestVersion(v), ETag: string(etag), Desired: map[uuid.UUID]losync.Desired{}}
		return desired.ForEach(func(k, v []byte) error {
			id, d, err := decodeRecord(k, v)
			if err != nil {
				return err
			}
			st.Desired[id] = d
			return nil
		})
	})
	if err != nil {
		return losync.State{}, fmt.Errorf("load LO store: %w", err)
	}
	return st, nil
}

// decodeRecord decodes the desired record of key k, whose bytes v are only valid during the
// transaction: the YAML is copied out by json.Unmarshal.
func decodeRecord(k, v []byte) (uuid.UUID, losync.Desired, error) {
	id, err := uuid.Parse(string(k))
	if err != nil || id.String() != string(k) {
		return uuid.UUID{}, losync.Desired{}, fmt.Errorf("desired key %q is not a deployment ID", k)
	}
	var r record
	if err := json.Unmarshal(v, &r); err != nil {
		return uuid.UUID{}, losync.Desired{}, fmt.Errorf("deployment %s: %w", id, err)
	}
	if got := contract.DigestOf(r.YAML); got != r.Digest {
		return uuid.UUID{}, losync.Desired{}, fmt.Errorf("deployment %s: YAML has digest %s, record says %s", id, got, r.Digest)
	}
	return id, losync.Desired{Digest: r.Digest, YAML: r.YAML, AdoptedVersion: r.AdoptedManifestVersion}, nil
}

// ReplaceDesired replaces desired state in one transaction (SPEC §8.2 step 6, §12).
func (b *Bolt) ReplaceDesired(_ context.Context, desired map[uuid.UUID]losync.Desired) error {
	err := b.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketDesired); err != nil {
			return err
		}
		bucket, err := tx.CreateBucket(bucketDesired)
		if err != nil {
			return err
		}
		for id, d := range desired {
			v, err := json.Marshal(record{Digest: d.Digest, AdoptedManifestVersion: d.AdoptedVersion, YAML: d.YAML})
			if err != nil {
				return fmt.Errorf("deployment %s: %w", id, err)
			}
			if err := bucket.Put([]byte(id.String()), v); err != nil {
				return fmt.Errorf("deployment %s: %w", id, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replace desired state: %w", err)
	}
	return nil
}

// CommitVersion records the accepted manifest's version and ETag in one transaction (SPEC §8.2
// step 8, §12).
func (b *Bolt) CommitVersion(_ context.Context, v contract.ManifestVersion, etag string) error {
	err := b.db.Update(func(tx *bolt.Tx) error {
		syncBucket := tx.Bucket(bucketSync)
		if syncBucket == nil {
			return errors.New("the sync bucket is missing")
		}
		return errors.Join(
			syncBucket.Put(keyVersion, binary.BigEndian.AppendUint64(nil, uint64(v))),
			syncBucket.Put(keyETag, []byte(etag)),
		)
	})
	if err != nil {
		return fmt.Errorf("commit manifest version %d: %w", v, err)
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
