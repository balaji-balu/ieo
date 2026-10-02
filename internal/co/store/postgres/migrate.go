package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// migrations are the store's schema changes, applied in name order. A migration, once released,
// is never edited: add the next one.
//
//go:embed migrations/*.sql
var migrations embed.FS

// migrate applies, in one transaction, every migration the database lacks. A lock keeps two COs
// starting together from applying the same one.
func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// After a Commit, Rollback is a no-op; before it, the migration failed and is undone.
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1, 0)", migrationLockSpace); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS co_schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slices.Sort(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var applied bool
		if err := tx.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM co_schema_migrations WHERE version = $1)", version).Scan(&applied); err != nil {
			return fmt.Errorf("migrate %s: %w", version, err)
		}
		if applied {
			continue
		}
		b, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("migrate %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO co_schema_migrations (version) VALUES ($1)", version); err != nil {
			return fmt.Errorf("migrate %s: %w", version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
