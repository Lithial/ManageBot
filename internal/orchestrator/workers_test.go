package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// autoGatesJSON resolves both the plan and merge gates automatically, so tests
// that focus on the worker/merger phases flow straight through without an
// approval step.
const autoGatesJSON = `{"plan":{"mode":"auto"},"merge":{"mode":"auto"}}`

// seedPlanGateRun creates a project + run with the given gates_json, persists a
// plan with the given tasks_json, and moves the run to plan_gate — the starting
// point for the worker phase.
func seedPlanGateRun(t *testing.T, st *store.Store, repo, tasksJSON, gatesJSON string) string {
	t.Helper()
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "proj", RepoPath: repo, DefaultGatesJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	rid, err := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: gatesJSON})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertPlan(ctx, store.Plan{RunID: rid, PlanMD: "# Plan", TasksJSON: tasksJSON}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRunPhase(ctx, rid, "plan_gate"); err != nil {
		t.Fatal(err)
	}
	return rid
}

func workerScript(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func newWorkerOrch(t *testing.T, st *store.Store, stateDir, fakeClaude, scriptPath string, maxWorkers int) *orchestrator.Orchestrator {
	t.Helper()
	return orchestrator.New(orchestrator.Config{
		Store:      st,
		StateDir:   stateDir,
		MaxWorkers: maxWorkers,
		WorkerCmd: func(taskDesc string) *exec.Cmd {
			c := exec.Command(fakeClaude)
			c.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
			return c
		},
		StepTimeout: 10 * time.Second,
	})
}

func TestTick_planGateToMerging_allWorkersDone(t *testing.T) {
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

	rid := seedPlanGateRun(t, st, repo,
		`[{"id":"t1","title":"first"},{"id":"t2","title":"second","depends_on":["t1"]}]`, autoGatesJSON)

	script := workerScript(t, stateDir, "worker.jsonl",
		`{"kind":"progress","msg":"working"}`,
		`{"kind":"done","summary":"done"}`,
		`{"kind":"exit","code":0}`,
	)
	o := newWorkerOrch(t, st, stateDir, fakeClaude, script, 4)

	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "merging" {
		t.Fatalf("phase = %q, want merging", got.Phase)
	}
	workers, err := st.ListWorkersByRun(context.Background(), rid)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(workers))
	}
	for _, w := range workers {
		if w.Status != "done" {
			t.Errorf("worker %s status = %q, want done", w.TaskID, w.Status)
		}
	}
}

// TestTick_perRunMaxWorkersSerializes proves the per-run cap (run.MaxWorkers),
// not just the daemon flag, reaches the scheduler: with max_workers=1 two
// independent tasks never run concurrently. Each worker records how many peers
// were in-flight while it held its marker; under a cap of 1 that count is always
// 1 (the scheduler won't start task B until task A's run func returns).
func TestTick_perRunMaxWorkersSerializes(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// concDir holds one marker file per in-flight worker; `observed` collects the
	// peer count each worker saw. Absolute so it survives the worker's cwd switch
	// into its worktree.
	concDir := t.TempDir()
	observed := filepath.Join(concDir, "observed")
	worker := workerScript(t, stateDir, "worker.sh",
		"#!/bin/sh",
		`me="`+concDir+`/$$"`,
		`touch "$me"`,
		`sleep 0.3`,
		`ls "`+concDir+`" | grep -v observed | wc -l >> "`+observed+`"`,
		`rm -f "$me"`,
		`echo '{"method":"report_done","params":{"summary":"ok"}}'`,
		`exit 0`,
	)

	// Two independent tasks (no deps) — both eligible at once, so an uncapped run
	// would race them.
	pid, _ := st.InsertProject(context.Background(), store.Project{Name: "proj", RepoPath: repo, DefaultGatesJSON: "{}"})
	rid, err := st.InsertRun(context.Background(), store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: autoGatesJSON, MaxWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertPlan(context.Background(), store.Plan{
		RunID: rid, PlanMD: "# Plan",
		TasksJSON: `[{"id":"t1","title":"first"},{"id":"t2","title":"second"}]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRunPhase(context.Background(), rid, "plan_gate"); err != nil {
		t.Fatal(err)
	}

	// Daemon-wide cap is generous (4); only the per-run cap of 1 should bind.
	o := orchestrator.New(orchestrator.Config{
		Store:      st,
		StateDir:   stateDir,
		MaxWorkers: 4,
		WorkerCmd: func(string) *exec.Cmd {
			return exec.Command("/bin/sh", worker)
		},
		StepTimeout: 10 * time.Second,
	})
	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	data, err := os.ReadFile(observed)
	if err != nil {
		t.Fatalf("read observed: %v", err)
	}
	counts := strings.Fields(string(data))
	if len(counts) != 2 {
		t.Fatalf("observed %d worker samples, want 2 (data=%q)", len(counts), data)
	}
	for _, c := range counts {
		if strings.TrimSpace(c) != "1" {
			t.Errorf("observed concurrency %q, want 1 (per-run cap not honored)", c)
		}
	}
}

func TestTick_workerPhaseRecordsWorkerDoneEvents(t *testing.T) {
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v", err)
	}
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`, autoGatesJSON)
	script := workerScript(t, stateDir, "worker.jsonl",
		`{"kind":"done","summary":"shipped t1"}`,
		`{"kind":"exit","code":0}`,
	)
	o := newWorkerOrch(t, st, stateDir, fakeClaude, script, 4)
	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	events, err := st.ListEventsByRun(context.Background(), rid)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		if e.Kind == "worker_done" {
			found = true
			if e.WorkerID == "" {
				t.Errorf("worker_done event has empty WorkerID")
			}
			if !strings.Contains(e.PayloadJSON, "shipped t1") {
				t.Errorf("worker_done payload = %q, want to contain summary", e.PayloadJSON)
			}
		}
	}
	if !found {
		t.Errorf("no worker_done event recorded; events = %+v", events)
	}
}

func TestTick_planGateToFailed_workerExitsNonZero(t *testing.T) {
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v", err)
	}
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`, autoGatesJSON)
	// Worker exits non-zero without report_done -> failed.
	script := workerScript(t, stateDir, "fail.jsonl", `{"kind":"exit","code":2}`)
	o := newWorkerOrch(t, st, stateDir, fakeClaude, script, 4)

	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "failed" {
		t.Errorf("phase = %q, want failed", got.Phase)
	}
}

func TestTick_planGate_holdsOnRequireApproval(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// require_approval plan gate: the run must hold at plan_gate with a pending
	// gate, even across multiple ticks, until a human resolves it.
	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`, `{"plan":{"mode":"require_approval"}}`)
	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})
	for i := 0; i < 2; i++ {
		if err := o.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "plan_gate" {
		t.Errorf("phase = %q, want plan_gate (run should hold at the gate)", got.Phase)
	}
	g, err := st.PendingGateByRun(context.Background(), rid)
	if err != nil {
		t.Fatalf("PendingGateByRun: %v", err)
	}
	if g.Kind != "plan" {
		t.Errorf("pending gate kind = %q, want plan", g.Kind)
	}
	// Idempotent: only one plan gate created across the two ticks.
	gs, _ := st.ListGatesByRun(context.Background(), rid)
	if len(gs) != 1 {
		t.Errorf("created %d gates across 2 ticks, want 1 (idempotent)", len(gs))
	}
}
