package store

import (
	"context"
	"path/filepath"
	"testing"
)

// columnExists reports whether table has a column of the given name.
func columnExists(t *testing.T, s *Store, table, col string) bool {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == col {
			return true
		}
	}
	return false
}

func TestOpenAppliesAdditiveMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wrap.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := map[string][]string{
		"gates":   {"action"},
		"runs":    {"max_workers", "worker_idle_timeout_ms"},
		"workers": {"last_progress_at"},
	}
	for table, cols := range want {
		for _, c := range cols {
			if !columnExists(t, s, table, c) {
				t.Errorf("expected column %s.%s to exist", table, c)
			}
		}
	}
	_ = s.Close()

	// Idempotency: re-opening the same DB must not error (duplicate-column guard).
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s2.Close()
}
