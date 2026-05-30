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

// seedMergingRun creates a project + run already in `merging`, with `survivors`
// done worker rows (each with a worker_done event) — the starting point for the
// merger phase.
func seedMergingRun(t *testing.T, st *store.Store, repo string, survivors int) string {
	t.Helper()
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "proj", RepoPath: repo, DefaultGatesJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	rid, err := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: autoGatesJSON})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < survivors; i++ {
		tid := "t" + string(rune('1'+i))
		wid, err := st.InsertWorker(ctx, store.Worker{
			RunID: rid, TaskID: tid, Branch: "wrap/" + rid + "/" + tid, WorktreePath: "/wt/" + tid,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.FinishWorker(ctx, wid, "done", 0); err != nil {
			t.Fatal(err)
		}
		if _, err := st.InsertEvent(ctx, store.Event{
			RunID: rid, WorkerID: wid, Kind: "worker_done", PayloadJSON: `{"task_id":"` + tid + `","summary":"did ` + tid + `"}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpdateRunPhase(ctx, rid, "merging"); err != nil {
		t.Fatal(err)
	}
	return rid
}

func newMergerOrch(t *testing.T, st *store.Store, stateDir, fakeClaude, scriptPath string) *orchestrator.Orchestrator {
	t.Helper()
	return orchestrator.New(orchestrator.Config{
		Store:    st,
		StateDir: stateDir,
		MergerCmd: func(_ string) *exec.Cmd {
			c := exec.Command(fakeClaude)
			c.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
			return c
		},
		StepTimeout: 10 * time.Second,
	})
}

func TestTick_mergingToDone(t *testing.T) {
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

	rid := seedMergingRun(t, st, repo, 2)
	script := workerScript(t, stateDir, "merger.jsonl",
		`{"kind":"progress","msg":"merging"}`,
		`{"kind":"done","summary":"merged 2 branches"}`,
		`{"kind":"exit","code":0}`,
	)
	o := newMergerOrch(t, st, stateDir, fakeClaude, script)

	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "done" {
		t.Fatalf("phase = %q, want done", got.Phase)
	}
	// merge_done event carries the summary; run_done event signals emission.
	mergeEv, err := st.LatestEventByKind(context.Background(), rid, "merge_done")
	if err != nil {
		t.Fatalf("LatestEventByKind(merge_done): %v", err)
	}
	if !strings.Contains(mergeEv.PayloadJSON, "merged 2 branches") {
		t.Errorf("merge_done payload = %q, want summary", mergeEv.PayloadJSON)
	}
	if _, err := st.LatestEventByKind(context.Background(), rid, "run_done"); err != nil {
		t.Errorf("no run_done event recorded: %v", err)
	}
	// The merge branch exists and is retained.
	if !branchExists(t, repo, "wrap/"+rid+"/merge") {
		t.Errorf("merge branch wrap/%s/merge not found", rid)
	}
}

func TestTick_mergerFails_runFailed(t *testing.T) {
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

	rid := seedMergingRun(t, st, repo, 1)
	script := workerScript(t, stateDir, "fail.jsonl", `{"kind":"exit","code":3}`)
	o := newMergerOrch(t, st, stateDir, fakeClaude, script)
	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "failed" {
		t.Errorf("phase = %q, want failed", got.Phase)
	}
}

func TestTick_merging_restsWhenNoMergerCmd(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rid := seedMergingRun(t, st, repo, 1)
	// No MergerCmd: the run must rest at merging regardless of gate policy.
	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})
	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "merging" {
		t.Errorf("phase = %q, want merging (rest when no merger configured)", got.Phase)
	}
}

func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branch)
	cmd.Dir = repo
	return cmd.Run() == nil
}
