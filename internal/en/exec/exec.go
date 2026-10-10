// Package exec runs one Apply or Remove command on the host, from start to finish (SPEC §8.9,
// §16.6): it pulls, extracts and starts each component, keeps the EN's record of what it applied,
// and reports every state change. Which command runs when, and what is acknowledged, is the
// caller's work.
package exec

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/compose"
	"github.com/balaji-balu/ieo/internal/en/override"
	"github.com/balaji-balu/ieo/internal/en/pull"
	"github.com/balaji-balu/ieo/internal/en/store"
	"github.com/balaji-balu/ieo/internal/platform"
)

// Config is the EN configuration the executor reads (SPEC §6.3).
type Config struct {
	// DataDir is en.data_dir. Deployments are extracted under <DataDir>/deployments (SPEC §9.1).
	DataDir string
	// Archive holds en.archive.max_entries and en.archive.max_extracted_bytes.
	Archive archive.Limits
	// StartTimeout is en.start_timeout: the wait for a component with `wait` and no `timeout`.
	StartTimeout time.Duration
	// OTel holds en.otel.http_endpoint and en.otel.grpc_endpoint.
	OTel override.OTel
}

// Puller pulls a component's Compose archive; *pull.Puller is one.
type Puller interface {
	ComposeArchive(ctx context.Context, repository, revision string) (*pull.Layer, error)
}

// Store is the EN's durable record of applied deployments; *store.Bolt is one.
type Store interface {
	Get(ctx context.Context, id uuid.UUID) (store.Deployment, bool, error)
	PutApplied(ctx context.Context, id uuid.UUID, a store.Applied) error
	PutComponentStatus(ctx context.Context, id uuid.UUID, s contract.ComponentStatus) error
	DeleteDeployment(ctx context.Context, id uuid.UUID) error
}

// Publisher sends a status event to the LO. An error means the event may not have arrived.
type Publisher interface {
	Publish(ctx context.Context, e contract.ComponentStatusEvent) error
}

// Executor runs Apply and Remove commands. Its methods are safe for concurrent use for different
// deployments; the caller runs at most one command per deployment at a time (SPEC §8.9).
type Executor struct {
	cfg    Config
	puller Puller
	runner compose.Runner
	store  Store
	pub    Publisher
	clock  platform.Clock
	log    *slog.Logger
}

// New returns an Executor. log carries the EN's site_id and host_id (SPEC §13.1).
func New(cfg Config, p Puller, r compose.Runner, s Store, pub Publisher, clock platform.Clock, log *slog.Logger) *Executor {
	return &Executor{cfg: cfg, puller: p, runner: r, store: s, pub: pub, clock: clock, log: log}
}

// Apply runs an Apply command the caller has validated (SPEC §8.9 step 2): cmd.Deployment is set
// and its components have distinct, non-empty slugs.
//
// If the deployment is applied at cmd.Digest with every component `installed`, Apply does
// nothing. Otherwise it takes the components in listed order: each is reported `installing`,
// pulled, extracted, given its override file and brought up, then reported `installed`. The first
// component that fails is reported `failed` with its error code (SPEC §10), and later ones are
// not started. Whatever happens, the digest and the projects brought up are recorded, each
// project before it is brought up.
//
// A component that fails is not an error of Apply. Apply returns an error only when the EN could
// not keep its record, or when ctx ended; it then reports nothing further. A status event that
// can't be published is logged, and Apply goes on: the state is recorded and inventory carries it.
func (e *Executor) Apply(ctx context.Context, cmd contract.Command) error {
	return errors.New("exec: Apply is not implemented")
}

// Remove runs a Remove command. If the deployment is not applied, it reports `removed` with
// cmd.Digest and does nothing else. Otherwise it reports `removing`, brings down every recorded
// project, deletes the deployment's directory and record, and reports `removed`; both events carry
// the applied digest, and neither names a component (SPEC §11.2).
//
// A project that can't be brought down, or a directory that can't be deleted, is not an error of
// Remove: it is logged, the record is kept and `removed` is not reported, so the LO sends Remove
// again. Remove returns an error only when the EN could not read or change its record, or when
// ctx ended.
func (e *Executor) Remove(ctx context.Context, cmd contract.Command) error {
	return errors.New("exec: Remove is not implemented")
}
