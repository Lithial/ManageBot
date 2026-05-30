package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestReconcile_failsMidWorkingRunAndOrphanWorkers(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: repo, DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "working"})
	wid, _ := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "b", WorktreePath: "/wt"})

	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})
	if err := o.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, _ := st.GetRun(ctx, rid)
	if got.Phase != "failed" {
		t.Errorf("run phase = %q, want failed (caught mid-working)", got.Phase)
	}
	ws, _ := st.ListWorkersByRun(ctx, rid)
	if len(ws) != 1 || ws[0].ID != wid || ws[0].Status != "failed" {
		t.Errorf("worker = %+v, want failed", ws)
	}
	if !hasEvent(t, st, rid, "daemon_recovered") {
		t.Error("expected daemon_recovered event")
	}
}

func TestReconcile_leavesGatedAndPendingRuns(t *testing.T) {
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/r", DefaultGatesJSON: "{}"})
	gated, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "a", GatesJSON: "{}", Phase: "plan_gate"})
	_, _ = st.InsertGate(ctx, store.Gate{RunID: gated, Kind: "plan", PayloadJSON: "{}"})
	pend, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "b", GatesJSON: "{}", Phase: "pending"})

	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})
	if err := o.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if g, _ := st.GetRun(ctx, gated); g.Phase != "plan_gate" {
		t.Errorf("gated run phase = %q, want plan_gate (should resume)", g.Phase)
	}
	if p, _ := st.GetRun(ctx, pend); p.Phase != "pending" {
		t.Errorf("pending run phase = %q, want pending (should resume)", p.Phase)
	}
}
