package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func seedRun(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	pid, _ := s.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}"})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})
	return rid
}

func TestInsertAndGetPendingGate(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	rid := seedRun(t, s)

	gid, err := s.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", PayloadJSON: "{}"})
	if err != nil {
		t.Fatalf("InsertGate: %v", err)
	}
	if gid == "" {
		t.Fatal("InsertGate returned empty id")
	}

	g, err := s.PendingGateByRun(ctx, rid)
	if err != nil {
		t.Fatalf("PendingGateByRun: %v", err)
	}
	if g.ID != gid || g.Kind != "plan" || g.Status != "pending" {
		t.Errorf("pending gate = %+v", g)
	}
	if g.ResolvedAt != 0 || g.ResolvedBy != "" {
		t.Errorf("fresh gate should be unresolved: %+v", g)
	}
}

func TestPendingGateByRun_noneWhenAllResolved(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	rid := seedRun(t, s)
	gid, _ := s.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", PayloadJSON: "{}"})
	if err := s.ResolveGate(ctx, gid, "approved", "cli", ""); err != nil {
		t.Fatalf("ResolveGate: %v", err)
	}
	if _, err := s.PendingGateByRun(ctx, rid); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestResolveGate(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	rid := seedRun(t, s)
	gid, _ := s.InsertGate(ctx, store.Gate{RunID: rid, Kind: "merge", PayloadJSON: "{}"})

	if err := s.ResolveGate(ctx, gid, "rejected", "alice", ""); err != nil {
		t.Fatalf("ResolveGate: %v", err)
	}
	g, err := s.LatestGateByKind(ctx, rid, "merge")
	if err != nil {
		t.Fatalf("LatestGateByKind: %v", err)
	}
	if g.Status != "rejected" || g.ResolvedBy != "alice" || g.ResolvedAt == 0 {
		t.Errorf("resolved gate = %+v", g)
	}
}

func TestResolveGatePersistsAction(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	rid := seedRun(t, s)
	id, err := s.InsertGate(ctx, store.Gate{RunID: rid, Kind: "merge_conflict", PayloadJSON: "{}"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.ResolveGate(ctx, id, "approved", "tester", "drop_branch"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	g, err := s.LatestGateByKind(ctx, rid, "merge_conflict")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if g.Action != "drop_branch" {
		t.Errorf("Action=%q want drop_branch", g.Action)
	}
}

func TestResolveGate_alreadyResolvedConflicts(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	rid := seedRun(t, s)
	gid, _ := s.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", PayloadJSON: "{}"})

	if err := s.ResolveGate(ctx, gid, "approved", "first", ""); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	// A second resolution of the same gate must conflict, not silently overwrite.
	err := s.ResolveGate(ctx, gid, "rejected", "second", "")
	if !errors.Is(err, store.ErrGateNotPending) {
		t.Fatalf("second resolve err = %v, want ErrGateNotPending", err)
	}
	// The first decision stands.
	g, _ := s.LatestGateByKind(ctx, rid, "plan")
	if g.Status != "approved" || g.ResolvedBy != "first" {
		t.Errorf("gate = %+v, want first approval preserved", g)
	}
}

func TestResolveGate_unknown(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	if err := s.ResolveGate(ctx, "no-such-gate", "approved", "cli", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLatestGateByKind_returnsNewest(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	rid := seedRun(t, s)
	_, _ = s.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", Status: "rejected", PayloadJSON: "{}"})
	g2, _ := s.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", PayloadJSON: "{}"})

	got, err := s.LatestGateByKind(ctx, rid, "plan")
	if err != nil {
		t.Fatalf("LatestGateByKind: %v", err)
	}
	if got.ID != g2 {
		t.Errorf("latest gate id = %q, want %q", got.ID, g2)
	}
}

func TestLatestGateByKind_notFound(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	rid := seedRun(t, s)
	if _, err := s.LatestGateByKind(ctx, rid, "plan"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
