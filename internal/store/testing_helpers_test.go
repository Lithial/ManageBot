package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

// openTempStore returns a store backed by a fresh temp DB, with cleanup
// registered via t.Cleanup. Used by all store tests that need to mutate.
func openTempStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "wrap.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
