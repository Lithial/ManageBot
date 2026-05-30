package store_test

import (
	"context"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func TestInsertAndListWorkers(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, _ := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
	})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})

	w1, err := s.InsertWorker(ctx, store.Worker{
		RunID: rid, TaskID: "t1", Branch: "wrap/r/t1", WorktreePath: "/tmp/wt/t1",
	})
	if err != nil {
		t.Fatalf("InsertWorker: %v", err)
	}
	if w1 == "" {
		t.Fatal("InsertWorker returned empty id")
	}
	if _, err := s.InsertWorker(ctx, store.Worker{
		RunID: rid, TaskID: "t2", Branch: "wrap/r/t2", WorktreePath: "/tmp/wt/t2",
	}); err != nil {
		t.Fatalf("InsertWorker t2: %v", err)
	}

	got, err := s.ListWorkersByRun(ctx, rid)
	if err != nil {
		t.Fatalf("ListWorkersByRun: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2: %+v", len(got), got)
	}
	// Freshly-inserted workers are running with a started_at and no exit code.
	for _, w := range got {
		if w.Status != "running" {
			t.Errorf("worker %s status = %q, want running", w.TaskID, w.Status)
		}
		if w.StartedAt == 0 {
			t.Errorf("worker %s StartedAt = 0, want set", w.TaskID)
		}
		if w.ExitCode != nil {
			t.Errorf("worker %s ExitCode = %v, want nil", w.TaskID, *w.ExitCode)
		}
	}
}

func TestFinishWorker(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, _ := s.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}"})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})
	wid, _ := s.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "b", WorktreePath: "/wt"})

	// Finish with exit code 0 — must be distinguishable from "no exit code".
	if err := s.FinishWorker(ctx, wid, "done", 0); err != nil {
		t.Fatalf("FinishWorker: %v", err)
	}
	got, err := s.ListWorkersByRun(ctx, rid)
	if err != nil {
		t.Fatal(err)
	}
	w := got[0]
	if w.Status != "done" {
		t.Errorf("status = %q, want done", w.Status)
	}
	if w.ExitCode == nil || *w.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", w.ExitCode)
	}
	if w.EndedAt == 0 {
		t.Errorf("EndedAt = 0, want set")
	}
}

func TestFinishWorker_unknown(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	if err := s.FinishWorker(ctx, "no-such-id", "done", 0); err == nil {
		t.Fatal("FinishWorker on unknown id: want error, got nil")
	}
}
