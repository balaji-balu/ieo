package postgres_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect/sql/schema"

	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/postgres"
	"github.com/balaji-balu/ieo/internal/co/store/storetest"
	"github.com/balaji-balu/ieo/internal/contract"
)

// The migrations create exactly the tables the ent schema describes.
func TestMigrationsMatchSchema(t *testing.T) {
	s := storetest.Postgres(t)
	var diff bytes.Buffer
	if err := s.Client().Schema.WriteTo(context.Background(), &diff, schema.WithDropColumn(true), schema.WithDropIndex(true)); err != nil {
		t.Fatalf("diff: %v", err)
	}
	for _, line := range strings.Split(diff.String(), "\n") {
		if l := strings.TrimSpace(line); l != "" && l != "BEGIN;" && l != "COMMIT;" {
			t.Errorf("migrations differ from ent/schema; the schema needs:\n%s", diff.String())
			break
		}
	}
}

// Opening a migrated database applies nothing again, also when several COs open it at once.
func TestOpenAppliesEachMigrationOnce(t *testing.T) {
	s, dsn := storetest.PostgresURL(t)
	ctx := context.Background()
	if _, err := deploy.New(s).AddSite(ctx, "site-1"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			again, err := postgres.Open(ctx, dsn)
			if err != nil {
				t.Errorf("open again: %v", err)
				return
			}
			if err := again.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, ok, err := s.Site(ctx, "site-1"); !ok || err != nil {
		t.Errorf("site lost after reopening: %v %v", ok, err)
	}
	var n int
	if err := db(t, dsn).QueryRowContext(ctx, "SELECT count(*) FROM co_schema_migrations").Scan(&n); err != nil || n != 1 {
		t.Errorf("co_schema_migrations has %d rows (%v), want 1", n, err)
	}
}

// SPEC §12: the database itself refuses to lower a site's manifestVersion, whoever writes.
func TestDatabaseRefusesLowerManifestVersion(t *testing.T) {
	s, dsn := storetest.PostgresURL(t)
	ctx := context.Background()
	if _, err := deploy.New(s).AddSite(ctx, "site-1"); err != nil {
		t.Fatal(err)
	}
	conn := db(t, dsn)
	if _, err := conn.ExecContext(ctx, "UPDATE co_sites SET manifest_version = 0 WHERE id = 'site-1'"); err == nil {
		t.Error("lowering manifest_version succeeded")
	}
	if _, err := conn.ExecContext(ctx, "UPDATE co_sites SET manifest_version = 5 WHERE id = 'site-1'"); err != nil {
		t.Errorf("raising manifest_version: %v", err)
	}
}

// SPEC §15.4: Open's errors never repeat the database URL, which may hold a password.
func TestOpenErrorsHideDatabaseURL(t *testing.T) {
	const password = "s3cret-pw"
	for name, dsn := range map[string]string{
		"malformed URL":       "postgres://user:" + password + "@host:port-is-not-a-number/db",
		"unreachable host":    "postgres://user:" + password + "@127.0.0.1:1/db?sslmode=disable&connect_timeout=1",
		"key=value form":      "host=127.0.0.1 port=1 user=user password=" + password + " sslmode=disable connect_timeout=1",
		"malformed key=value": "host=127.0.0.1 user=user password" + password,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := postgres.Open(context.Background(), dsn)
			if err == nil {
				_ = s.Close() // the test fails either way
				t.Fatal("open succeeded")
			}
			if strings.Contains(err.Error(), password) {
				t.Errorf("error repeats the password: %v", err)
			}
		})
	}
}

func db(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// SPEC §18.3: "Verify CO database restore keeps `manifestVersion` increasing (§12)." A restore
// brings back an older manifest_version, as if the database were restored from a backup; raising
// the versions puts every site above anything it published before.
func TestSpec_18_3_RestoreThenRaiseVersionsKeepsManifestVersionIncreasing(t *testing.T) {
	s, dsn := storetest.PostgresURL(t)
	ctx := context.Background()
	svc := deploy.New(s)
	if _, err := svc.AddSite(ctx, "site-1"); err != nil {
		t.Fatal(err)
	}
	for v := contract.ManifestVersion(2); v <= 4; v++ { // publish up to version 4
		if err := s.ChangeSite(ctx, "site-1", func(deploy.SiteState, bool) (deploy.SiteChange, error) {
			return deploy.SiteChange{Manifest: &deploy.Manifest{Version: v, Body: []byte{byte(v)}, ETag: `"e"`}}, nil
		}); err != nil {
			t.Fatalf("publish %d: %v", v, err)
		}
	}
	// The restore: the backup was taken at version 2. A restore loads rows without the trigger.
	conn := db(t, dsn)
	for _, stmt := range []string{
		"ALTER TABLE co_sites DISABLE TRIGGER co_sites_manifest_version_never_decreases",
		"UPDATE co_sites SET manifest_version = 2 WHERE id = 'site-1'",
		"ALTER TABLE co_sites ENABLE TRIGGER co_sites_manifest_version_never_decreases",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if _, err := svc.RaiseManifestVersions(ctx, 10); err != nil {
		t.Fatalf("raise: %v", err)
	}
	m, ok, err := s.Manifest(ctx, "site-1")
	if err != nil || !ok {
		t.Fatalf("manifest: %v %v", ok, err)
	}
	var body contract.StateManifest
	if err := json.Unmarshal(m.Body, &body); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if m.Version != 12 || body.ManifestVersion != 12 {
		t.Errorf("after restore to 2 and raise by 10: version %d, body %d; want 12", m.Version, body.ManifestVersion)
	}
	if m.Version <= 4 {
		t.Errorf("version %d is not above the last published version 4", m.Version)
	}
}
