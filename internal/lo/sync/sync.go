// Package sync keeps an LO's desired state in step with its CO (SPEC §8.2, §16.3): each tick
// polls the site's State Manifest, refuses a rollback, fetches the YAML the LO does not hold,
// verifies every artifact against its digest, and replaces desired state atomically.
//
// The package name shadows the standard library's sync; callers import it as losync.
package sync

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/platform"
)

// State is the part of the LO runtime state the sync tick reads and writes (SPEC §4.1.11).
type State struct {
	// Version is accepted_manifest_version; 0 means no manifest has been accepted, since
	// manifest versions start at 1.
	Version contract.ManifestVersion
	// ETag is the ETag of the accepted manifest's response; empty means none.
	ETag string
	// Desired maps each deployment ID to what should run.
	Desired map[uuid.UUID]Desired
}

// Desired is one deployment of desired state.
type Desired struct {
	Digest contract.Digest
	// YAML is the deployment's bytes, verified against Digest.
	YAML []byte
	// AdoptedVersion is the manifest version in which Digest first appeared for this deployment
	// (adopted_manifest_version).
	AdoptedVersion contract.ManifestVersion
}

// Store keeps the sync state. Implementations copy what they take and return, so callers never
// share maps or slices with them.
type Store interface {
	// Load returns the stored state; a store that was never written returns the zero State.
	Load(ctx context.Context) (State, error)
	// ReplaceDesired replaces the whole desired state atomically (SPEC §8.2 step 6).
	ReplaceDesired(ctx context.Context, desired map[uuid.UUID]Desired) error
	// CommitVersion records the accepted manifest's version and ETag (SPEC §8.2 step 8).
	CommitVersion(ctx context.Context, v contract.ManifestVersion, etag string) error
}

// Config configures a Syncer.
type Config struct {
	// SiteID is the LO's site, logged on every line (SPEC §13.1).
	SiteID contract.SiteID
	// COURL is the base URL of the CO's Margo API, such as `https://co.example`.
	COURL string
	// Token is the site's bearer token (SPEC §15.6). It is sent only in the Authorization header
	// and never logged.
	Token string
	// PollInterval is the time between the end of one sync attempt and the start of the next.
	PollInterval time.Duration
	// Transport carries the requests; nil means http.DefaultTransport.
	Transport http.RoundTripper
	// Clock times the poll interval; nil means platform.SystemClock.
	Clock platform.Clock
	// Log receives one line per sync attempt; nil means slog.Default.
	Log *slog.Logger
	// Reconcile is called after desired state is replaced and before the new version is committed
	// (SPEC §8.2 steps 6–8); nil means nothing to do (until roadmap slice D).
	Reconcile func(ctx context.Context)
}

// Syncer runs the LO sync loop for one site. Its methods must not be called concurrently.
type Syncer struct {
	store     Store
	client    *client
	interval  time.Duration
	clock     platform.Clock
	log       *slog.Logger
	reconcile func(ctx context.Context)
}

// New returns a Syncer that keeps store in step with the CO cfg names.
func New(cfg Config, store Store) *Syncer {
	s := &Syncer{
		store:     store,
		client:    newClient(cfg.COURL, cfg.Token, cfg.Transport),
		interval:  cfg.PollInterval,
		clock:     cfg.Clock,
		log:       cfg.Log,
		reconcile: cfg.Reconcile,
	}
	if s.clock == nil {
		s.clock = platform.SystemClock()
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With(slog.String("site_id", string(cfg.SiteID))) // SPEC §13.1
	if s.reconcile == nil {
		s.reconcile = func(context.Context) {}
	}
	return s
}

// Tick runs one sync attempt (SPEC §16.3), logs its outcome on one line, and returns the outcome.
// The error is non-nil only when the store fails; the attempt then has no outcome, and the next
// attempt starts over.
func (s *Syncer) Tick(ctx context.Context) (Outcome, error) {
	r, err := s.attempt(ctx)
	if err != nil {
		err = fmt.Errorf("sync attempt: store: %w", err)
		s.log.LogAttrs(ctx, slog.LevelError, "sync attempt failed", slog.String("reason", err.Error()))
		return "", err
	}
	attrs := append([]slog.Attr{
		slog.String("outcome", string(r.outcome)),
		slog.Uint64("manifest_version", uint64(r.version)),
	}, r.attrs...)
	s.log.LogAttrs(ctx, r.level, r.msg, attrs...)
	return r.outcome, nil
}

// Run ticks at once and then PollInterval after each attempt ends, until ctx ends; it then returns
// ctx's error. Every outcome waits one interval: backoff arrives with roadmap slice K.
func (s *Syncer) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		// Tick logs every attempt, a store failure included, and the next one starts over.
		_, _ = s.Tick(ctx)
		t := s.clock.NewTimer(s.interval)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C():
		}
	}
	return ctx.Err()
}

// result is how an attempt ended, and its log line. version is the manifest version the line
// reports: the received manifest's once it has been decoded, the stored one before.
type result struct {
	outcome Outcome
	level   slog.Level
	msg     string
	version contract.ManifestVersion
	attrs   []slog.Attr
}

func unreachable(v contract.ManifestVersion, msg string, err *requestError, attrs ...slog.Attr) result {
	return result{outcome: Unreachable, level: slog.LevelWarn, msg: msg, version: v, attrs: append(attrs, err.attrs()...)}
}

// mismatch is a security event, logged at warning level with the offending values (SPEC §13.1).
func mismatch(v contract.ManifestVersion, msg string, attrs ...slog.Attr) result {
	return result{outcome: AbortedDigestMismatch, level: slog.LevelWarn, msg: msg, version: v, attrs: attrs}
}

// attempt runs SPEC §8.2 steps 1–8. The error is a store failure.
func (s *Syncer) attempt(ctx context.Context) (result, error) {
	st, err := s.store.Load(ctx)
	if err != nil {
		return result{}, fmt.Errorf("load state: %w", err)
	}
	resp, rerr := s.client.manifest(ctx, st.ETag)
	if rerr != nil {
		return unreachable(st.Version, "manifest request failed", rerr), nil
	}
	if resp.notModified {
		return result{outcome: NotModified, level: slog.LevelInfo, msg: "manifest not modified", version: st.Version}, nil
	}
	// SPEC §8.2 step 3: validate, then refuse a rollback.
	m, err := contract.DecodeStateManifest(resp.body)
	if err != nil {
		return result{
			outcome: Unreachable, level: slog.LevelError, msg: "invalid manifest", version: st.Version,
			attrs: []slog.Attr{slog.String("reason", err.Error())},
		}, nil
	}
	if m.ManifestVersion <= st.Version {
		return result{
			outcome: RejectedRollback, level: slog.LevelWarn, msg: "manifest rollback rejected", version: m.ManifestVersion,
			attrs: []slog.Attr{slog.Uint64("stored_manifest_version", uint64(st.Version))},
		}, nil
	}
	yamls, how, failure := s.fetch(ctx, st, m)
	if failure != nil {
		return *failure, nil
	}

	// SPEC §8.2 steps 6–8: replace desired state, start reconciling, then commit the version.
	desired := make(map[uuid.UUID]Desired, len(m.Deployments))
	for _, d := range m.Deployments {
		adopted := m.ManifestVersion
		if prev, ok := st.Desired[d.DeploymentID]; ok && prev.Digest == d.Digest {
			adopted = prev.AdoptedVersion
		}
		desired[d.DeploymentID] = Desired{Digest: d.Digest, YAML: yamls[d.DeploymentID], AdoptedVersion: adopted}
	}
	if err := s.store.ReplaceDesired(ctx, desired); err != nil {
		return result{}, fmt.Errorf("replace desired state: %w", err)
	}
	s.reconcile(ctx)
	if err := s.store.CommitVersion(ctx, m.ManifestVersion, resp.etag); err != nil {
		return result{}, fmt.Errorf("commit manifest version %d: %w", m.ManifestVersion, err)
	}
	return result{
		outcome: Accepted, level: slog.LevelInfo, msg: "manifest accepted", version: m.ManifestVersion,
		attrs: []slog.Attr{slog.Int("deployments", len(m.Deployments)), slog.String("fetched", how)},
	}, nil
}

// fetch returns the verified YAML of every deployment of m, taking what st holds and fetching the
// rest (SPEC §8.2 steps 4–5), and how it fetched: "bundle", "deployments" or "none". A non-nil
// result ends the attempt with desired state unchanged.
//
// Content comes from the configured CO at the paths SPEC §11.1 defines, never from the
// manifest's URLs, so the token goes nowhere else.
func (s *Syncer) fetch(ctx context.Context, st State, m contract.StateManifest) (map[uuid.UUID][]byte, string, *result) {
	held := make(map[contract.Digest][]byte, len(st.Desired))
	for _, d := range st.Desired {
		held[d.Digest] = d.YAML
	}
	notHeld := 0
	for _, d := range m.Deployments {
		if _, ok := held[d.Digest]; !ok {
			notHeld++
		}
	}
	// SPEC §8.2 step 4 [IEO]: the bundle when more than half are not held; a first sync holds none.
	if 2*notHeld > len(m.Deployments) {
		yamls, failure := s.fetchBundle(ctx, m)
		return yamls, "bundle", failure
	}
	how := "none"
	yamls := make(map[uuid.UUID][]byte, len(m.Deployments))
	for _, d := range m.Deployments {
		if y, ok := held[d.Digest]; ok {
			yamls[d.DeploymentID] = y
			continue
		}
		how = "deployments"
		about := []slog.Attr{slog.String("deployment_id", d.DeploymentID.String()), slog.String("digest", string(d.Digest))}
		b, err := s.client.content(ctx, contract.DeploymentURL(d.DeploymentID, d.Digest))
		if err != nil {
			r := unreachable(m.ManifestVersion, "deployment fetch failed", err, about...)
			return nil, how, &r
		}
		if got := contract.DigestOf(b); got != d.Digest { // SPEC §8.2 step 5, §15.3
			r := mismatch(m.ManifestVersion, "deployment digest mismatch", append(about, slog.String("computed_digest", string(got)))...)
			return nil, how, &r
		}
		yamls[d.DeploymentID] = b
	}
	return yamls, how, nil
}

// fetchBundle returns the YAML of every deployment of m from its bundle. It verifies the bundle's
// digest before unpacking it, then that the bundle holds exactly one entry per deployment, each
// matching that deployment's digest (SPEC §8.2 step 5, §15.3, ADR 0012).
func (s *Syncer) fetchBundle(ctx context.Context, m contract.StateManifest) (map[uuid.UUID][]byte, *result) {
	fail := func(r result) (map[uuid.UUID][]byte, *result) { return nil, &r }
	// Not nil: some deployment is not held, and a valid manifest with deployments has a bundle.
	b := m.Bundle
	about := slog.String("bundle_digest", string(b.Digest))
	body, err := s.client.content(ctx, contract.BundleURL(b.Digest))
	if err != nil {
		return fail(unreachable(m.ManifestVersion, "bundle fetch failed", err, about))
	}
	if got := contract.DigestOf(body); got != b.Digest {
		return fail(mismatch(m.ManifestVersion, "bundle digest mismatch", about, slog.String("computed_digest", string(got))))
	}
	files, derr := contract.DecodeBundle(body)
	if derr != nil {
		return fail(mismatch(m.ManifestVersion, "bundle does not match the manifest", about, slog.String("reason", derr.Error())))
	}
	refs := make(map[uuid.UUID]contract.DeploymentRef, len(m.Deployments))
	for _, d := range m.Deployments {
		refs[d.DeploymentID] = d
	}
	yamls := make(map[uuid.UUID][]byte, len(files))
	for _, f := range files {
		entry := slog.String("bundle_entry", contract.BundleEntryName(f.DeploymentID))
		d, ok := refs[f.DeploymentID]
		if !ok {
			return fail(mismatch(m.ManifestVersion, "bundle entry not in the manifest", about, entry))
		}
		if got := contract.DigestOf(f.YAML); got != d.Digest {
			return fail(mismatch(m.ManifestVersion, "bundle entry digest mismatch", about, entry,
				slog.String("deployment_id", d.DeploymentID.String()), slog.String("digest", string(d.Digest)),
				slog.String("computed_digest", string(got))))
		}
		yamls[f.DeploymentID] = f.YAML
	}
	for _, d := range m.Deployments {
		if _, ok := yamls[d.DeploymentID]; !ok {
			return fail(mismatch(m.ManifestVersion, "bundle entry missing", about,
				slog.String("bundle_entry", contract.BundleEntryName(d.DeploymentID)),
				slog.String("deployment_id", d.DeploymentID.String()), slog.String("digest", string(d.Digest))))
		}
	}
	return yamls, nil
}
