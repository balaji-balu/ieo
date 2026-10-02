package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/blob"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/deployment"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/device"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/publication"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent/site"
	"github.com/balaji-balu/ieo/internal/contract"
)

// siteLockSpace is the first key of every site's advisory lock; migrationLockSpace is
// migrate's. Two-key advisory locks never collide with one-key ones.
const (
	siteLockSpace      = 0x434f0001
	migrationLockSpace = 0x434f0000
)

// ChangeSite runs change on the state of site and writes what it returns, in one transaction that
// holds the site's advisory lock, so changes to one site are atomic and serialized (SPEC §12).
func (s *Store) ChangeSite(ctx context.Context, siteID contract.SiteID,
	change func(state deploy.SiteState, exists bool) (deploy.SiteChange, error)) error {
	return s.inSite(ctx, siteID, func(tx *ent.Tx) (deploy.SiteChange, error) {
		row, exists, err := siteRow(ctx, tx.Site, siteID)
		if err != nil {
			return deploy.SiteChange{}, err
		}
		state := deploy.SiteState{
			Site:        siteOf(row),
			Hosts:       map[contract.HostID]contract.DeviceCapabilitiesManifest{},
			Deployments: map[uuid.UUID]deploy.Deployment{},
			YAML:        map[uuid.UUID][]byte{},
			Manifest:    manifestOf(row),
		}
		hosts, err := tx.Device.Query().Where(device.SiteID(string(siteID)), device.HostNEQ("")).All(ctx)
		if err != nil {
			return deploy.SiteChange{}, fmt.Errorf("get hosts: %w", err)
		}
		for _, h := range hosts {
			var caps contract.DeviceCapabilitiesManifest
			if err := json.Unmarshal(h.Capabilities, &caps); err != nil {
				return deploy.SiteChange{}, fmt.Errorf("decode capabilities of %s/%s: %w", siteID, h.Host, err)
			}
			state.Hosts[contract.HostID(h.Host)] = caps
		}
		rows, err := tx.Deployment.Query().Where(deployment.SiteID(string(siteID))).All(ctx)
		if err != nil {
			return deploy.SiteChange{}, fmt.Errorf("get deployments: %w", err)
		}
		var digests []string
		for _, r := range rows {
			d, err := deploymentOf(r)
			if err != nil {
				return deploy.SiteChange{}, err
			}
			state.Deployments[d.ID] = d
			if !d.Deleted {
				digests = append(digests, r.Digest)
			}
		}
		content, err := blobs(ctx, tx, digests)
		if err != nil {
			return deploy.SiteChange{}, err
		}
		for id, d := range state.Deployments {
			if !d.Deleted {
				state.YAML[id] = content[string(d.Digest)]
			}
		}
		return change(state, exists)
	})
}

// ChangeDeployment is ChangeSite for a change that reads only deployment id of site.
func (s *Store) ChangeDeployment(ctx context.Context, siteID contract.SiteID, id uuid.UUID,
	change func(state deploy.DeploymentState, exists bool) (deploy.SiteChange, error)) error {
	return s.inSite(ctx, siteID, func(tx *ent.Tx) (deploy.SiteChange, error) {
		row, exists, err := siteRow(ctx, tx.Site, siteID)
		if err != nil {
			return deploy.SiteChange{}, err
		}
		state := deploy.DeploymentState{Site: siteOf(row), ManifestVersion: manifestOf(row).Version}
		r, err := tx.Deployment.Query().Where(deployment.ID(id), deployment.SiteID(string(siteID))).Only(ctx)
		switch {
		case ent.IsNotFound(err):
		case err != nil:
			return deploy.SiteChange{}, fmt.Errorf("get deployment %s: %w", id, err)
		default:
			if state.Deployment, err = deploymentOf(r); err != nil {
				return deploy.SiteChange{}, err
			}
			state.Found, state.Reported = true, r.Reported
		}
		return change(state, exists)
	})
}

// ChangeDevice is ChangeSite for a change that reads only device id of site.
func (s *Store) ChangeDevice(ctx context.Context, siteID contract.SiteID, id contract.DeviceID,
	change func(state deploy.DeviceState, exists bool) (deploy.SiteChange, error)) error {
	return s.inSite(ctx, siteID, func(tx *ent.Tx) (deploy.SiteChange, error) {
		row, exists, err := siteRow(ctx, tx.Site, siteID)
		if err != nil {
			return deploy.SiteChange{}, err
		}
		state := deploy.DeviceState{Site: siteOf(row)}
		hosts := []string{""}
		if id.Host != "" {
			hosts = append(hosts, string(id.Host))
		}
		rows, err := tx.Device.Query().
			Where(device.SiteID(string(siteID)), device.HostIn(hosts...)).
			Select(device.FieldHost).All(ctx)
		if err != nil {
			return deploy.SiteChange{}, fmt.Errorf("get device %s: %w", id, err)
		}
		for _, r := range rows {
			state.Gateway = state.Gateway || r.Host == ""
			state.Device = state.Device || id.Site == siteID && r.Host == string(id.Host)
		}
		return change(state, exists)
	})
}

// inSite runs read and writes the change it returns in one transaction holding site's advisory
// lock. An error from read is returned as it is, and nothing is written.
func (s *Store) inSite(ctx context.Context, siteID contract.SiteID, read func(tx *ent.Tx) (deploy.SiteChange, error)) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("change site %s: %w", siteID, err)
	}
	committed := false
	defer func() {
		if !committed { // also when read panics, so the site's lock is released
			_ = tx.Rollback() // the change failed already; there is nothing more to report
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1, hashtext($2))", siteLockSpace, string(siteID)); err != nil {
		return fmt.Errorf("lock site %s: %w", siteID, err)
	}
	c, err := read(tx)
	if err != nil {
		return err
	}
	if err := write(ctx, tx, siteID, c); err != nil {
		return fmt.Errorf("change site %s: %w", siteID, err)
	}
	committed = true // after a failed Commit, Rollback has nothing to undo
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change site %s: %w", siteID, err)
	}
	return nil
}

// write writes c to site. The trigger on co_sites refuses a lower manifest version (SPEC §12),
// which rolls the whole change back.
func write(ctx context.Context, tx *ent.Tx, siteID contract.SiteID, c deploy.SiteChange) error {
	if c.Site != nil {
		err := tx.Site.Create().SetID(string(siteID)).SetRetired(c.Site.Retired).
			OnConflictColumns(site.FieldID).UpdateRetired().Exec(ctx)
		if err != nil {
			return fmt.Errorf("put site: %w", err)
		}
	}
	if err := putBlobs(ctx, tx, c.Blobs); err != nil {
		return err
	}
	if c.Deployment != nil {
		if err := putDeployment(ctx, tx, siteID, *c.Deployment); err != nil {
			return err
		}
		if c.Manifest != nil && !c.Deployment.Deleted { // the new manifest holds its digest
			if err := publish(ctx, tx, siteID, false, c.Deployment.ID, c.Deployment.Digest); err != nil {
				return err
			}
		}
	}
	if c.Device != nil {
		if err := putDevice(ctx, tx, *c.Device); err != nil {
			return err
		}
	}
	if c.Status != nil {
		if err := putStatus(ctx, tx, *c.Status); err != nil {
			return err
		}
	}
	if c.Manifest != nil {
		return putManifest(ctx, tx, siteID, *c.Manifest)
	}
	return nil
}

// putBlobs stores each blob whose digest is not stored yet.
func putBlobs(ctx context.Context, tx *ent.Tx, blobs map[contract.Digest][]byte) error {
	if len(blobs) == 0 {
		return nil
	}
	// Sorted, so transactions inserting the same digests take their row locks in one order.
	var creates []*ent.BlobCreate
	for _, d := range slices.Sorted(maps.Keys(blobs)) {
		creates = append(creates, tx.Blob.Create().SetID(string(d)).SetContent(blobs[d]))
	}
	if err := tx.Blob.CreateBulk(creates...).OnConflictColumns(blob.FieldID).DoNothing().Exec(ctx); err != nil {
		return fmt.Errorf("put blobs: %w", err)
	}
	return nil
}

// putManifest replaces the site's manifest and publishes its bundle.
func putManifest(ctx context.Context, tx *ent.Tx, siteID contract.SiteID, m deploy.Manifest) error {
	n, err := tx.Site.Update().Where(site.ID(string(siteID))).
		SetManifestVersion(uint64(m.Version)).SetManifestBody(m.Body).
		SetManifestEtag(m.ETag).SetManifestBundle(string(m.Bundle)).Save(ctx)
	if err != nil {
		return fmt.Errorf("put manifest %d: %w", m.Version, err)
	}
	if n != 1 {
		return fmt.Errorf("put manifest %d: no site %s", m.Version, siteID)
	}
	if m.Bundle == "" {
		return nil
	}
	return publish(ctx, tx, siteID, true, uuid.Nil, m.Bundle)
}

// putDeployment writes d. It refuses a deployment stored at another site.
func putDeployment(ctx context.Context, tx *ent.Tx, siteID contract.SiteID, d deploy.Deployment) error {
	params, err := json.Marshal(d.Parameters)
	if err != nil {
		return fmt.Errorf("encode parameters of deployment %s: %w", d.ID, err)
	}
	set := func(m *ent.DeploymentMutation) {
		m.SetTarget(d.Target.String())
		m.SetAppID(d.AppID)
		m.SetAppVersion(d.AppVersion)
		m.SetProfile(d.Profile)
		m.SetName(d.Name)
		m.SetNamespace(d.Namespace)
		m.SetParameters(params)
		m.SetDigest(string(d.Digest))
		m.SetDigestVersion(uint64(d.DigestVersion))
		m.SetDeleted(d.Deleted)
		m.SetDeletedVersion(uint64(d.DeletedVersion))
		m.SetRemoved(d.Removed)
	}
	update := tx.Deployment.Update().Where(deployment.ID(d.ID), deployment.SiteID(string(siteID)))
	set(update.Mutation())
	n, err := update.Save(ctx)
	if err != nil {
		return fmt.Errorf("update deployment %s: %w", d.ID, err)
	}
	if n == 1 {
		return nil
	}
	create := tx.Deployment.Create().SetID(d.ID).SetSiteID(string(siteID))
	set(create.Mutation())
	if err := create.Exec(ctx); err != nil { // a deployment of another site violates the key
		return fmt.Errorf("create deployment %s: %w", d.ID, err)
	}
	return nil
}

func publish(ctx context.Context, tx *ent.Tx, siteID contract.SiteID, bundle bool, id uuid.UUID, digest contract.Digest) error {
	err := tx.Publication.Create().
		SetSiteID(string(siteID)).SetBundle(bundle).SetDeploymentID(id).SetDigest(string(digest)).
		OnConflictColumns(publication.FieldSiteID, publication.FieldBundle, publication.FieldDeploymentID, publication.FieldDigest).
		DoNothing().Exec(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) { // no row: published before
		return fmt.Errorf("publish %s: %w", digest, err)
	}
	return nil
}

func putDevice(ctx context.Context, tx *ent.Tx, c deploy.DeviceChange) error {
	if c.Capabilities == nil {
		_, err := tx.Device.Delete().
			Where(device.SiteID(string(c.ID.Site)), device.Host(string(c.ID.Host))).Exec(ctx)
		if err != nil {
			return fmt.Errorf("delete device %s: %w", c.ID, err)
		}
		return nil
	}
	if c.Capabilities.Properties.ID == (contract.DeviceID{}) {
		// It would not decode: a device ID is never empty (SPEC §4.2).
		return fmt.Errorf("put device %s: capabilities without properties.id", c.ID)
	}
	b, err := json.Marshal(c.Capabilities)
	if err != nil {
		return fmt.Errorf("encode capabilities of %s: %w", c.ID, err)
	}
	err = tx.Device.Create().SetSiteID(string(c.ID.Site)).SetHost(string(c.ID.Host)).SetCapabilities(b).
		OnConflictColumns(device.FieldSiteID, device.FieldHost).UpdateCapabilities().Exec(ctx)
	if err != nil {
		return fmt.Errorf("put device %s: %w", c.ID, err)
	}
	return nil
}

func putStatus(ctx context.Context, tx *ent.Tx, r deploy.StatusReport) error {
	id := r.Status.DeploymentID
	b, err := encodeStatus(r.Status)
	if err != nil {
		return err
	}
	if err := tx.Status.Create().SetDeploymentID(id).SetStatus(b).Exec(ctx); err != nil {
		return fmt.Errorf("add status of %s: %w", id, err)
	}
	u := tx.Deployment.UpdateOneID(id).SetReported(true)
	if r.Current {
		u.SetCurrentStatus(b)
	}
	if err := u.Exec(ctx); err != nil {
		return fmt.Errorf("set status of %s: %w", id, err)
	}
	return nil
}
