package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func TestInsertAndGetPlan(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, _ := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
	})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})

	pl := store.Plan{
		RunID:     rid,
		PlanMD:    "# Plan",
		TasksJSON: `[{"id":"t1","title":"do thing"}]`,
	}
	pid2, err := s.InsertPlan(ctx, pl)
	if err != nil {
		t.Fatalf("InsertPlan: %v", err)
	}
	if pid2 == "" {
		t.Fatal("InsertPlan returned empty id")
	}

	got, err := s.GetPlanByRun(ctx, rid)
	if err != nil {
		t.Fatalf("GetPlanByRun: %v", err)
	}
	if got.PlanMD != pl.PlanMD || got.TasksJSON != pl.TasksJSON || got.RunID != rid {
		t.Errorf("plan mismatch: got %+v want %+v", got, pl)
	}
	if got.ApprovedAt != 0 {
		t.Errorf("ApprovedAt = %d, want 0 (unapproved)", got.ApprovedAt)
	}
}

func TestGetPlanByRun_notFound(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	_, err := s.GetPlanByRun(ctx, "no-such-run")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
