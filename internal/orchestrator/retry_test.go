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

func countWorkersForTask(t *testing.T, st *store.Store, runID, taskID string) int {
	t.Helper()
	ws, err := st.ListWorkersByRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range ws {
		if w.TaskID == taskID {
			n++
		}
	}
	return n
}

func hasEvent(t *testing.T, st *store.Store, runID, kind string) bool {
	t.Helper()
	evs, err := st.ListEventsByRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func newRetryOrch(t *testing.T, st *store.Store, stateDir, fakeClaude, scriptPath string, retryBudget int, stepTimeout time.Duration) *orchestrator.Orchestrator {
	t.Helper()
	return orchestrator.New(orchestrator.Config{
		Store:       st,
		StateDir:    stateDir,
		MaxWorkers:  4,
		RetryBudget: retryBudget,
		WorkerCmd: func(string) *exec.Cmd {
			c := exec.Command(fakeClaude)
			c.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
			return c
		},
		StepTimeout: stepTimeout,
	})
}

func TestTick_workerRetriesOnCrash(t *testing.T) {
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
	// Always crashes; with budget 1 we expect 2 attempts (2 worker rows).
	script := workerScript(t, stateDir, "crash.jsonl", `{"kind":"exit","code":1}`)
	o := newRetryOrch(t, st, stateDir, fakeClaude, script, 1, 10*time.Second)

	if err := o.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "failed" {
		t.Errorf("phase = %q, want failed", got.Phase)
	}
	if n := countWorkersForTask(t, st, rid, "t1"); n != 2 {
		t.Errorf("worker rows for t1 = %d, want 2 (original + 1 retry)", n)
	}
	if !hasEvent(t, st, rid, "worker_retry") {
		t.Error("expected a worker_retry event")
	}
}

func TestTick_noRetryWhenBudgetZero(t *testing.T) {
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v", err)
	}
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, _ := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	t.Cleanup(func() { _ = st.Close() })

	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`, autoGatesJSON)
	script := workerScript(t, stateDir, "crash.jsonl", `{"kind":"exit","code":1}`)
	o := newRetryOrch(t, st, stateDir, fakeClaude, script, 0, 10*time.Second)
	if err := o.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := countWorkersForTask(t, st, rid, "t1"); n != 1 {
		t.Errorf("worker rows = %d, want 1 (no retry)", n)
	}
}

func TestTick_workerTimeoutMarksTimeout(t *testing.T) {
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v", err)
	}
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, _ := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	t.Cleanup(func() { _ = st.Close() })

	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`, autoGatesJSON)
	// Sleeps well past the 200ms step timeout → killed → timeout.
	script := workerScript(t, stateDir, "hang.jsonl", `{"kind":"sleep_ms","ms":5000}`, `{"kind":"exit","code":0}`)
	o := newRetryOrch(t, st, stateDir, fakeClaude, script, 0, 200*time.Millisecond)

	if err := o.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "failed" {
		t.Errorf("phase = %q, want failed", got.Phase)
	}
	if !hasEvent(t, st, rid, "worker_timeout") {
		t.Error("expected a worker_timeout event")
	}
}
