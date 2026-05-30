package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func TestInsertProjectAndRun(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

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

func TestInsertRun_persistsMaxWorkers(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	pid, _ := s.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/r", DefaultGatesJSON: "{}"})

	// A positive cap round-trips.
	rid, err := s.InsertRun(ctx, store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}", MaxWorkers: 2,
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	got, err := s.GetRun(ctx, rid)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxWorkers != 2 {
		t.Errorf("MaxWorkers = %d, want 2", got.MaxWorkers)
	}

	// An unset cap (0) persists as NULL and reads back as 0 (the "unset" sentinel).
	rid2, err := s.InsertRun(ctx, store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	got2, err := s.GetRun(ctx, rid2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.MaxWorkers != 0 {
		t.Errorf("unset MaxWorkers = %d, want 0", got2.MaxWorkers)
	}
}

func TestInsertProjectDuplicateNameFails(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	p := store.Project{Name: "demo", RepoPath: "/tmp/r", DefaultGatesJSON: "{}"}
	if _, err := s.InsertProject(ctx, p); err != nil {
		t.Fatalf("first InsertProject: %v", err)
	}
	if _, err := s.InsertProject(ctx, p); err == nil {
		t.Fatal("expected duplicate-name error, got nil")
	}
}

func TestUpdateRunPhase(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, err := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	rid, err := s.InsertRun(ctx, store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateRunPhase(ctx, rid, "planning"); err != nil {
		t.Fatalf("UpdateRunPhase: %v", err)
	}
	got, err := s.GetRun(ctx, rid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "planning" {
		t.Errorf("phase = %q, want %q", got.Phase, "planning")
	}
	if got.UpdatedAt < got.CreatedAt {
		t.Errorf("UpdatedAt=%d should be >= CreatedAt=%d", got.UpdatedAt, got.CreatedAt)
	}
}

func TestUpdateRunPhase_unknownRun(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	err := s.UpdateRunPhase(ctx, "no-such-id", "planning")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListRuns_newestFirst(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, _ := s.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}"})
	r1, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "a", GatesJSON: "{}"})
	r2, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "b", GatesJSON: "{}"})
	r3, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "c", GatesJSON: "{}"})

	got, err := s.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d, want 3", len(got))
	}
	// Newest first; rowid breaks same-second ties so insertion order is reversed.
	if got[0].ID != r3 || got[1].ID != r2 || got[2].ID != r1 {
		t.Errorf("order = [%s %s %s], want [%s %s %s]", got[0].ID, got[1].ID, got[2].ID, r3, r2, r1)
	}
}

func TestListRuns_empty(t *testing.T) {
	got, err := openTempStore(t).ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len=%d, want 0", len(got))
	}
}

func TestListRunsByPhase(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, _ := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
	})
	r1, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "a", GatesJSON: "{}"})
	r2, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "b", GatesJSON: "{}"})
	_ = s.UpdateRunPhase(ctx, r2, "planning")

	pending, err := s.ListRunsByPhase(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != r1 {
		t.Errorf("pending runs = %+v, want [%s]", pending, r1)
	}
}

func TestListRunsByPhase_empty(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	got, err := s.ListRunsByPhase(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil slice for empty result, got %v", got)
	}
}

func TestGetProjectByID(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, err := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
		VerificationCommand: "make test",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject(ctx, pid)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ID != pid || got.Name != "p" || got.RepoPath != "/tmp/repo" || got.VerificationCommand != "make test" {
		t.Errorf("project mismatch: got %+v", got)
	}
}

func TestGetProjectByID_notFound(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	if _, err := s.GetProject(ctx, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
