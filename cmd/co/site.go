package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/postgres"
	"github.com/balaji-balu/ieo/internal/contract"
)

// siteAdd adds site id and prints its token on stdout, the only place it is ever shown (SPEC
// §15.6). It refuses a site that exists, so it never replaces a token an LO already holds.
func siteAdd(ctx context.Context, s *postgres.Store, id contract.SiteID, stdout io.Writer, log *slog.Logger) error {
	created, err := deploy.New(s).AddSite(ctx, id)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("site %s exists; to replace its token, run: co site rotate-token %s", id, id)
	}
	log.Info("site added", "site_id", id)
	return issue(ctx, s, id, stdout, log)
}

// siteRotateToken prints a new token for site id, which replaces its old one (SPEC §15.6). It also
// gives a token to a site added without one, as after a crash between adding it and issuing one.
// A retired site gets none: the CO refuses its LO whatever the token.
func siteRotateToken(ctx context.Context, s *postgres.Store, id contract.SiteID, stdout io.Writer, log *slog.Logger) error {
	site, ok, err := s.Site(ctx, id)
	switch {
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("no site %s; to add it, run: co site add %s", id, id)
	case site.Retired:
		return fmt.Errorf("site %s is retired", id)
	}
	return issue(ctx, s, id, stdout, log)
}

// issue issues a token for site id and prints it on stdout, one line. The token is never logged
// (SPEC §15.4).
func issue(ctx context.Context, s *postgres.Store, id contract.SiteID, stdout io.Writer, log *slog.Logger) error {
	token, err := auth.New(s).Issue(ctx, id)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, token); err != nil {
		return fmt.Errorf("print the token of site %s: %w; it is issued but lost, so run: co site rotate-token %s", id, err, id)
	}
	log.Info("site token issued", "site_id", id)
	return nil
}

// sitesRaiseVersions raises every site's manifestVersion by by, after a database restore (SPEC
// §12), and logs each site it raised.
func sitesRaiseVersions(ctx context.Context, s *postgres.Store, by contract.ManifestVersion, log *slog.Logger) error {
	raised, err := deploy.New(s).RaiseManifestVersions(ctx, by)
	for _, r := range raised {
		log.Info("manifest version raised", "site_id", r.Site, "previous_manifest_version", uint64(r.From),
			"manifest_version", uint64(r.To))
	}
	if err != nil {
		return err
	}
	log.Info("manifest versions raised", "sites", len(raised), "by", uint64(by))
	return nil
}
