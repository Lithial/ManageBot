package orchestrator_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// TestReconcileThenTick_resumesWorkingRunSkippingCompletedTask drives the full
// crash-resume path: a run is caught mid-`working` with task t1 already done
// (worker_done event) and task t2 orphaned (a running worker). After
// Reconcile + one Tick, t1 is NOT re-dispatched, t2 is re-run to completion,
// and the run proceeds to merging — partial progress is resumed, not discarded.
func TestReconcileThenTick_resumesWorkingRunSkippingCompletedTask(t *testing.T) {
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v (run `make fake-claude`)", err)
	}
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: repo, DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: autoGatesJSON, Phase: "working"})
	_, _ = st.InsertPlan(ctx, store.Plan{RunID: rid, PlanMD: "# Plan",
		TasksJSON: `[{"id":"t1","title":"first"},{"id":"t2","title":"second"}]`})

	// t1 completed before the crash: a done worker row + worker_done event.
	t1wid, _ := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "wrap/r/t1", WorktreePath: "/wt1"})
	_ = st.FinishWorker(ctx, t1wid, "done", 0)
	donePayload, _ := json.Marshal(map[string]string{"task_id": "t1", "summary": "did t1"})
	_, _ = st.InsertEvent(ctx, store.Event{RunID: rid, WorkerID: t1wid, Kind: "worker_done", PayloadJSON: string(donePayload)})

	// t2 was in flight: an orphaned running worker the crash left behind.
	t2orphan, _ := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t2", Branch: "wrap/r/t2", WorktreePath: "/wt2"})

	script := workerScript(t, stateDir, "worker.jsonl",
		`{"kind":"done","summary":"did t2"}`,
		`{"kind":"exit","code":0}`,
	)
	o := newWorkerOrch(t, st, stateDir, fakeClaude, script, 4)

	if err := o.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := o.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, _ := st.GetRun(ctx, rid)
	if got.Phase != "merging" {
		t.Fatalf("phase = %q, want merging (resumed run finished its remaining task)", got.Phase)
	}

	ws, _ := st.ListWorkersByRun(ctx, rid)
	var t1Rows, t2Rows int
	var orphanStatus string
	for _, w := range ws {
		switch w.TaskID {
		case "t1":
			t1Rows++
		case "t2":
			t2Rows++
		}
		if w.ID == t2orphan {
			orphanStatus = w.Status
		}
	}
	if t1Rows != 1 {
		t.Errorf("t1 worker rows = %d, want 1 (a completed task must not be re-dispatched)", t1Rows)
	}
	if t2Rows != 2 {
		t.Errorf("t2 worker rows = %d, want 2 (failed orphan + one resumed attempt)", t2Rows)
	}
	if orphanStatus != "failed" {
		t.Errorf("orphan worker status = %q, want failed", orphanStatus)
	}
}

func TestReconcile_resumesMidWorkingRunAndFailsOrphanWorkers(t *testing.T) {
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

	// The run is left at `working` for the tick loop to resume from partial
	// progress — not failed.
	got, _ := st.GetRun(ctx, rid)
	if got.Phase != "working" {
		t.Errorf("run phase = %q, want working (resumed, not failed)", got.Phase)
	}
	// The crash-orphaned worker is failed as retryable.
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
