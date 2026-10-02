// Package storetest runs CO tests against every store backend: Memory always, and Postgres when
// IEO_TEST_DATABASE_URL names a database the test may create schemas in (roadmap B4).
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/api"
	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store"
	"github.com/balaji-balu/ieo/internal/co/store/postgres"
	"github.com/balaji-balu/ieo/internal/contract"
)

const (
	// DatabaseURLEnv names a Postgres database, e.g.
	// postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable. Each test gets a schema
	// of its own there, dropped when the test ends.
	DatabaseURLEnv = "IEO_TEST_DATABASE_URL"
	// RequirePostgresEnv names a variable that, when set, fails the Postgres subtests instead of
	// skipping them when DatabaseURLEnv is unset. CI sets it.
	RequirePostgresEnv = "IEO_REQUIRE_POSTGRES"
)

// Store is every operation the CO packages need from a store backend.
type Store interface {
	catalog.Store
	deploy.Store
	api.Store
	auth.Store
	// Deployment returns a deployment, and whether it exists.
	Deployment(ctx context.Context, id uuid.UUID) (deploy.Deployment, bool, error)
	// Blob returns the content stored under digest, and whether there is any.
	Blob(ctx context.Context, digest contract.Digest) ([]byte, bool, error)
	// PutDevice replaces the latest capabilities reported for a device.
	PutDevice(ctx context.Context, id contract.DeviceID, caps contract.DeviceCapabilitiesManifest) error
	// CurrentStatus returns the current status of a deployment, and whether it has one.
	CurrentStatus(ctx context.Context, id uuid.UUID) (contract.DeploymentStatus, bool, error)
	// StatusHistory returns every status reported for a deployment, oldest first.
	StatusHistory(ctx context.Context, id uuid.UUID) ([]contract.DeploymentStatus, error)
}

var (
	_ Store = (*store.Memory)(nil)
	_ Store = (*postgres.Store)(nil)
)

// New returns an empty store of one backend, which lives until t ends.
type New func(t *testing.T) Store

// Each runs test once per backend, as the subtests "memory" and "postgres". newStore returns an
// empty store of that backend each time it is called.
func Each(t *testing.T, test func(t *testing.T, newStore New)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		test(t, func(*testing.T) Store { return store.NewMemory() })
	})
	t.Run("postgres", func(t *testing.T) {
		test(t, func(t *testing.T) Store { return Postgres(t) })
	})
}

// Postgres returns an empty Postgres store in a schema of its own, dropped when t ends. It skips t
// when DatabaseURLEnv is unset, or fails it if RequirePostgresEnv is set.
func Postgres(t *testing.T) *postgres.Store {
	t.Helper()
	s, _ := PostgresURL(t)
	return s
}

// PostgresURL is Postgres, and also returns the database URL that opens the store's schema.
func PostgresURL(t *testing.T) (*postgres.Store, string) {
	t.Helper()
	base := os.Getenv(DatabaseURLEnv)
	if base == "" {
		if os.Getenv(RequirePostgresEnv) != "" {
			t.Fatalf("%s is set but %s is not", RequirePostgresEnv, DatabaseURLEnv)
		}
		t.Skipf("%s not set", DatabaseURLEnv)
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Fatalf("open %s: %v", DatabaseURLEnv, err)
	}
	t.Cleanup(func() { _ = admin.Close() }) // nothing to do about a failed close in a test
	var b [8]byte
	_, _ = rand.Read(b[:]) // never fails (crypto/rand, Go 1.24)
	schema := "test_" + hex.EncodeToString(b[:])
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	dsn := WithSearchPath(t, base, schema)
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s, dsn
}

// WithSearchPath returns the database URL dsn with its search_path set to schema.
func WithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse %s: %v", DatabaseURLEnv, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
