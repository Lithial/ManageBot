package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func TestInsertProjectAndRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrap.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	p := store.Project{
		Name:             "demo",
		RepoPath:         "/tmp/demo-repo",
		DefaultGatesJSON: `{"plan":{"mode":"auto"}}`,
	}
	pid, err := s.InsertProject(ctx, p)
	if err != nil {
		t.Fatalf("InsertProject: %v", err)
	}
	if pid == "" {
		t.Fatal("InsertProject returned empty id")
	}

	r := store.Run{
		ProjectID:  pid,
		IntakeKind: "cli",
		IntakeRef:  "/tmp/demo-spec.md",
		SpecMD:     "# demo spec",
		GatesJSON:  `{"plan":{"mode":"auto"}}`,
		Phase:      "pending",
	}
	rid, err := s.InsertRun(ctx, r)
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if rid == "" {
		t.Fatal("InsertRun returned empty id")
	}

	// Verify the run row exists with the expected fields.
	got, err := s.GetRun(ctx, rid)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ProjectID != pid {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, pid)
	}
	if got.Phase != "pending" {
		t.Errorf("Phase = %q, want %q", got.Phase, "pending")
	}
}

func TestInsertProjectDuplicateNameFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrap.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	p := store.Project{Name: "demo", RepoPath: "/tmp/r", DefaultGatesJSON: "{}"}
	if _, err := s.InsertProject(ctx, p); err != nil {
		t.Fatalf("first InsertProject: %v", err)
	}
	if _, err := s.InsertProject(ctx, p); err == nil {
		t.Fatal("expected duplicate-name error, got nil")
	}
}
