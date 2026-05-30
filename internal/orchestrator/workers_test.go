package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// seedPlanGateRun creates a project + run, persists a plan with the given
// tasks_json, and moves the run to plan_gate — the starting point for the
// worker phase.
func seedPlanGateRun(t *testing.T, st *store.Store, repo, tasksJSON string) string {
	t.Helper()
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "proj", RepoPath: repo, DefaultGatesJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	rid, err := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})
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
		Store:            st,
		StateDir:         stateDir,
		AutoAdvanceGates: true,
		MaxWorkers:       maxWorkers,
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
		`[{"id":"t1","title":"first"},{"id":"t2","title":"second","depends_on":["t1"]}]`)

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

	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`)
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

func TestTick_planGate_notAdvancedWhenAutoAdvanceOff(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`)
	// AutoAdvanceGates defaults to false; no WorkerCmd configured.
	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})
	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "plan_gate" {
		t.Errorf("phase = %q, want plan_gate (run should rest at the gate)", got.Phase)
	}
}
