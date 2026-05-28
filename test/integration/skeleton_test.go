//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestSkeletonEndToEnd(t *testing.T) {
	wrapdBin, err := testutil.LocateBinary("wrapd")
	if err != nil {
		t.Fatalf("locate wrapd: %v (did you run `make wrapd`?)", err)
	}
	wrapBin, err := testutil.LocateBinary("wrap")
	if err != nil {
		t.Fatalf("locate wrap: %v (did you run `make wrap`?)", err)
	}

	d := testutil.StartTestDaemon(t, wrapdBin)

	// Create a fake "repo" directory with a spec file.
	repo := t.TempDir()
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# integration spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Invoke the wrap CLI against the test daemon.
	cmd := exec.Command(wrapBin, "run", "--socket", d.SocketPath, "--repo", repo, specPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrap run: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), `"run_id":`) {
		t.Fatalf("output missing run_id: %s", out)
	}
	if !strings.Contains(string(out), `"phase": "pending"`) {
		t.Fatalf("output missing pending phase: %s", out)
	}

	// Open the daemon's DB directly and verify the row exists.
	dbPath := filepath.Join(d.StateDir, "wrap.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs WHERE phase = 'pending'`,
	).Scan(&count); err != nil {
		t.Fatalf("query runs: %v", err)
	}
	if count != 1 {
		t.Errorf("runs count = %d, want 1", count)
	}

	var projectCount int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM projects WHERE name = ?`, filepath.Base(repo),
	).Scan(&projectCount); err != nil {
		t.Fatalf("query projects: %v", err)
	}
	if projectCount != 1 {
		t.Errorf("projects count = %d, want 1", projectCount)
	}
}
