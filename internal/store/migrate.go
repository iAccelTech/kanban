package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrate applies every embedded migration newer than PRAGMA user_version, each in
// its own transaction. Files are named NNNN_description.sql and numbered from 1
// with no gaps.
func migrate(ctx context.Context, db *sql.DB) error {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > len(names) {
		return fmt.Errorf("database is at schema version %d but this binary only knows %d; refusing to run an older binary on a newer database", current, len(names))
	}

	for i, name := range names {
		version := i + 1
		base := strings.TrimPrefix(name, "migrations/")
		if n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0]); err != nil || n != version {
			return fmt.Errorf("migration %s: expected number %04d", base, version)
		}
		if version <= current {
			continue
		}
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", base, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: %w", base, err)
		}
	}
	return nil
}
