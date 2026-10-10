// Package exec runs one Apply or Remove command on the host, from start to finish (SPEC §8.9,
// §16.6): it pulls, extracts and starts each component, keeps the EN's record of what it applied,
// and reports every state change. Which command runs when, and what is acknowledged, is the
// caller's work.
package exec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
	if cmd.Deployment == nil {
		return fmt.Errorf("apply %s: the command has no deployment", cmd.DeploymentID)
	}
	if err := e.apply(ctx, cmd); err != nil {
		return fmt.Errorf("apply %s at %s: %w", cmd.DeploymentID, cmd.Digest, err)
	}
	return nil
}

// applyRun is one Apply in progress.
type applyRun struct {
	*Executor
	cmd      contract.Command
	log      *slog.Logger
	projects []string // the deployment's recorded Compose projects, in the order first recorded
}

func (e *Executor) apply(ctx context.Context, cmd contract.Command) error {
	components := cmd.Deployment.Spec.DeploymentProfile.Components
	current, _, err := e.store.Get(ctx, cmd.DeploymentID)
	if err != nil {
		return err
	}
	if current.Applied.Digest == cmd.Digest && allInstalled(current, components) {
		return nil // SPEC §8.9 step 1
	}
	// SPEC §8.9: components this Apply has not reached are `pending`, not what an earlier Apply
	// left.
	for _, c := range components {
		if contract.ComponentSlug(c.Name) == "" {
			return errors.New("a component has no name; the command was not validated")
		}
		if err := e.store.PutComponentStatus(ctx, cmd.DeploymentID, contract.ComponentStatus{Name: c.Name, State: contract.StatePending}); err != nil {
			return err
		}
	}
	// Projects recorded by an earlier Apply stay recorded, so a Remove brings them down too.
	r := &applyRun{Executor: e, cmd: cmd, log: e.logFor(cmd.DeploymentID, cmd.Digest), projects: current.Applied.ComposeProjects}
	var runErr error
	for _, c := range components {
		installed, err := r.component(ctx, c)
		runErr = err
		if err != nil || !installed {
			break // SPEC §8.9 step 4.6: later components are not started
		}
	}
	// SPEC §8.9 step 6: recorded whether the Apply succeeded or not.
	if err := r.record(ctx); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

func allInstalled(d store.Deployment, components []contract.Component) bool {
	for _, c := range components {
		if d.Components[c.Name].State != contract.StateInstalled {
			return false
		}
	}
	return true
}

// record writes the digest and projects of the Apply to the store.
func (r *applyRun) record(ctx context.Context) error {
	return r.store.PutApplied(ctx, r.cmd.DeploymentID, store.Applied{Digest: r.cmd.Digest, ComposeProjects: r.projects})
}

// component runs SPEC §8.9 step 4 for one component and reports whether it is installed. The
// error is one that ends the Apply without a report: the store failed, or ctx ended.
func (r *applyRun) component(ctx context.Context, c contract.Component) (installed bool, err error) {
	if err := r.report(ctx, c.Name, contract.StateInstalling, nil); err != nil {
		return false, err
	}
	code, cause, err := r.install(ctx, c)
	if err != nil {
		return false, err
	}
	if cause == nil {
		return true, r.report(ctx, c.Name, contract.StateInstalled, nil)
	}
	if err := ctx.Err(); err != nil {
		return false, err // the EN is stopping: the step was cut short, not failed (SPEC §8.9)
	}
	r.log.Warn("component failed", "component", c.Name, "code", code, "reason", cause.Error())
	return false, r.report(ctx, c.Name, contract.StateFailed, &contract.StatusError{
		Code: code, Source: c.Name, Message: truncate(cause.Error(), maxMessageBytes),
	})
}

// install pulls, extracts and brings up one component. A failure of the component is returned as
// its error code (SPEC §10) and cause; err is a failure of the EN to keep its record.
func (r *applyRun) install(ctx context.Context, c contract.Component) (code string, cause, err error) {
	options, cause := upOptions(c.Properties, r.cfg.StartTimeout)
	if cause != nil {
		return contract.CodeComposeFailed, cause, nil
	}
	layer, cause := r.puller.ComposeArchive(ctx, c.Properties.Repository, c.Properties.Revision)
	if cause != nil {
		if errors.Is(cause, pull.ErrDigestMismatch) {
			return contract.CodeDigestMismatch, cause, nil
		}
		return contract.CodePullFailed, cause, nil
	}
	dir := r.componentDir(r.cmd.DeploymentID, r.cmd.Digest, c.Name)
	top, cause := archive.Extract(ctx, layer, dir, r.cfg.Archive)
	if cerr := layer.Close(); cerr != nil {
		r.log.Warn("pulled layer not deleted", "component", c.Name, "reason", cerr.Error())
	}
	if cause != nil {
		return contract.CodeArchiveInvalid, cause, nil
	}
	project, cause := override.Write(ctx, r.runner, dir, top, *r.cmd.Deployment, c.Name, r.cfg.OTel)
	if cause != nil {
		return contract.CodeComposeFailed, cause, nil
	}
	// SPEC §8.9 step 4.4: recorded before it is brought up, so no project is up without a record.
	if !slices.Contains(r.projects, project.Name) {
		r.projects = append(slices.Clone(r.projects), project.Name)
	}
	if err := r.record(ctx); err != nil {
		return "", nil, err
	}
	if cause := r.runner.Up(ctx, project, options); cause != nil {
		if errors.Is(cause, compose.ErrStartTimeout) {
			return contract.CodeStartTimeout, cause, nil
		}
		return contract.CodeComposeFailed, cause, nil
	}
	return "", nil, nil
}

// upOptions returns how a component is brought up (SPEC §8.9 step 4.5): it is waited for unless
// `wait` is false, for its own `timeout` or, when it has none or zero, for en.start_timeout, so
// no wait is without end.
func upOptions(p contract.ComponentProperties, startTimeout time.Duration) (compose.UpOptions, error) {
	if p.Wait != nil && !*p.Wait {
		return compose.UpOptions{}, nil
	}
	timeout := startTimeout
	if p.Timeout != "" {
		own, err := contract.ParseComponentTimeout(p.Timeout)
		if err != nil {
			return compose.UpOptions{}, err
		}
		if own > 0 {
			timeout = own
		}
	}
	return compose.UpOptions{Wait: true, Timeout: timeout}, nil
}

// maxMessageBytes bounds the message of a status error. A Compose failure carries the output of
// the command, which can be tens of KiB (ADR 0017); the start of it says what failed.
const maxMessageBytes = 4096

// truncate returns s cut to at most limit bytes, at a character boundary.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// report records a component status, then publishes it (ADR 0016). The error is from the store; a
// status that can't be published is logged, since the record and inventory carry it.
func (r *applyRun) report(ctx context.Context, component string, state contract.ComponentState, statusErr *contract.StatusError) error {
	status := contract.ComponentStatus{Name: component, State: state, Error: statusErr}
	if err := r.store.PutComponentStatus(ctx, r.cmd.DeploymentID, status); err != nil {
		return err
	}
	r.publish(ctx, r.log, contract.ComponentStatusEvent{
		DeploymentID: r.cmd.DeploymentID, Digest: r.cmd.Digest, Component: component, State: state, Error: statusErr,
	})
	return nil
}

// publish stamps and sends a status event, and logs one that can't be sent.
func (e *Executor) publish(ctx context.Context, log *slog.Logger, event contract.ComponentStatusEvent) {
	event.At = e.clock.Now()
	if err := e.pub.Publish(ctx, event); err != nil {
		log.Warn("status event not published", "component", event.Component, "state", string(event.State), "reason", err.Error())
	}
}

// logFor returns the logger for lines about one deployment (SPEC §13.1).
func (e *Executor) logFor(id uuid.UUID, digest contract.Digest) *slog.Logger {
	return e.log.With("deployment_id", id.String(), "digest", digest.String())
}

// deploymentDir is the working directory of a deployment (SPEC §9.1).
func (e *Executor) deploymentDir(id uuid.UUID) string {
	return filepath.Join(e.cfg.DataDir, "deployments", id.String())
}

// componentDir is the directory of one component of a deployment at a digest (SPEC §9.1): the
// digest with `-` for `:`, and the component slug.
func (e *Executor) componentDir(id uuid.UUID, digest contract.Digest, component string) string {
	return filepath.Join(e.deploymentDir(id), strings.ReplaceAll(digest.String(), ":", "-"), contract.ComponentSlug(component))
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
	if err := e.remove(ctx, cmd); err != nil {
		return fmt.Errorf("remove %s: %w", cmd.DeploymentID, err)
	}
	return nil
}

func (e *Executor) remove(ctx context.Context, cmd contract.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := cmd.DeploymentID
	current, applied, err := e.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !applied {
		// SPEC §8.9 Remove step 1. Component states of an Apply that recorded nothing go too.
		if err := e.store.DeleteDeployment(ctx, id); err != nil {
			return err
		}
		e.publish(ctx, e.logFor(id, cmd.Digest), contract.ComponentStatusEvent{DeploymentID: id, Digest: cmd.Digest, State: contract.StateRemoved})
		return nil
	}
	// SPEC §8.9: the events of a Remove carry the applied digest and name no component.
	log := e.logFor(id, current.Applied.Digest)
	event := contract.ComponentStatusEvent{DeploymentID: id, Digest: current.Applied.Digest, State: contract.StateRemoving}
	e.publish(ctx, log, event)

	removed := true
	for _, project := range current.Applied.ComposeProjects {
		if err := e.runner.Down(ctx, project); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// The others are still brought down; the LO sends Remove again (SPEC §8.9).
			log.Error("project not brought down", "project", project, "reason", err.Error())
			removed = false
		}
	}
	if removed {
		if err := os.RemoveAll(e.deploymentDir(id)); err != nil {
			log.Error("working directory not deleted", "reason", err.Error())
			removed = false
		}
	}
	if !removed {
		return nil
	}
	if err := e.store.DeleteDeployment(ctx, id); err != nil {
		return err
	}
	event.State = contract.StateRemoved
	e.publish(ctx, log, event)
	return nil
}
