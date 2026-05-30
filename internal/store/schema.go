package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed schema.sql
var schemaSQL string

// additiveMigrations are idempotent ALTER TABLE statements applied after
// schema.sql. Each must tolerate re-application (existing DBs already have the
// column); isDuplicateColumnErr filters SQLite's "duplicate column name". Append
// only — never rewrite or reorder (the guard relies on additive semantics).
var additiveMigrations = []string{
	`ALTER TABLE gates ADD COLUMN action TEXT`,
	`ALTER TABLE runs ADD COLUMN max_workers INTEGER`,
	`ALTER TABLE runs ADD COLUMN worker_idle_timeout_ms INTEGER`,
	`ALTER TABLE workers ADD COLUMN last_progress_at INTEGER`,
}

func isDuplicateColumnErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}

func applySchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range strings.Split(schemaSQL, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	for _, stmt := range additiveMigrations {
		if _, err := tx.ExecContext(ctx, stmt); err != nil && !isDuplicateColumnErr(err) {
			return fmt.Errorf("migration %q: %w", firstLine(stmt), err)
		}
	}
	return tx.Commit()
}

// firstLine returns the first line of s, for error messages where the full
// statement would be noisy.
func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
