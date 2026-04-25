package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run applies all pending SQL migrations from the provided FS in alphabetical order.
// Applied migrations are tracked in the schema_migrations table so each file
// is executed exactly once, even across restarts.
func Run(ctx context.Context, db *pgxpool.Pool, files fs.FS) error {
	if _, err := db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.Glob(files, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)

	for _, name := range entries {
		var exists bool
		if err := db.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = $1)", name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}

		sql, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		if _, err := db.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}

		if _, err := db.Exec(ctx,
			"INSERT INTO schema_migrations (name) VALUES ($1)", name,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}

		slog.Info("migration applied", "name", strings.TrimSuffix(name, ".sql"))
	}
	return nil
}
