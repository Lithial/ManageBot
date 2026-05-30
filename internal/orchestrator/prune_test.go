package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
	"github.com/Lithial/ManageBot/internal/worktree"
)

// TestPruneRun_notTerminal refuses to prune a run that is still in flight.
func TestPruneRun_notTerminal(t *testing.T) {
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "working"})

	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})
	_, _, err = o.PruneRun(ctx, rid)
	if !errors.Is(err, store.ErrRunNotTerminal) {
		t.Fatalf("PruneRun on working run: err = %v, want ErrRunNotTerminal", err)
	}
}

// TestPruneRun_unknownRun returns ErrNotFound.
func TestPruneRun_unknownRun(t *testing.T) {
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})
	if _, _, err := o.PruneRun(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PruneRun on unknown run: err = %v, want ErrNotFound", err)
	}
}

// TestPruneRun_happyPath removes a terminal run's retained worktrees + branches
// and records a `pruned` event.
func TestPruneRun_happyPath(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: repo, DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "failed"})

	// Seed two real worktrees and matching worker rows.
	m := worktree.NewManager(stateDir)
	for _, tid := range []string{"t1", "t2"} {
		branch := "wrap/" + rid + "/" + tid
		wt, err := m.Add(ctx, worktree.AddRequest{RepoPath: repo, Branch: branch, BaseRef: "main", Subpath: filepath.Join("runs", rid, tid)})
		if err != nil {
			t.Fatalf("seed worktree %s: %v", tid, err)
		}
		if _, err := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: tid, Branch: branch, WorktreePath: wt.Path}); err != nil {
			t.Fatal(err)
		}
	}

	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})
	wtCount, brCount, err := o.PruneRun(ctx, rid)
	if err != nil {
		t.Fatalf("PruneRun: %v", err)
	}
	if wtCount != 2 || brCount != 2 {
		t.Errorf("counts = %d worktrees / %d branches, want 2/2", wtCount, brCount)
	}

	out, err := exec.Command("git", "-C", repo, "branch", "--list", "wrap/"+rid+"/*").CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branches remain after prune: %q", out)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "runs", rid, "t1")); !os.IsNotExist(err) {
		t.Errorf("worktree t1 still exists after prune: err=%v", err)
	}

	if _, err := st.LatestEventByKind(ctx, rid, "pruned"); err != nil {
		t.Errorf("expected a pruned event: %v", err)
	}
}
