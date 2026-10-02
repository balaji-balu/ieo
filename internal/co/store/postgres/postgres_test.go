package postgres_test

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect/sql/schema"

	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/postgres"
	"github.com/balaji-balu/ieo/internal/co/store/storetest"
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
	if err := deploy.New(s).AddSite(ctx, "site-1"); err != nil {
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
	if err := deploy.New(s).AddSite(ctx, "site-1"); err != nil {
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
