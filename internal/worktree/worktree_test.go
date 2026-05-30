package worktree_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/testutil"
	"github.com/Lithial/ManageBot/internal/worktree"
)

// requireGit skips the test if git is not on PATH. Used for tests that need
// git available but don't need an actual repo (e.g. invalid-repo error paths).
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func TestManager_AddRemove(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	m := worktree.NewManager(stateDir)
	ctx := context.Background()

	wt, err := m.Add(ctx, worktree.AddRequest{
		RepoPath: repo,
		Branch:   "wrap/run1/plan",
		BaseRef:  "main",
		Subpath:  "runs/run1/plan",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	wantPath := filepath.Join(stateDir, "runs/run1/plan")
	if wt.Path != wantPath {
		t.Errorf("Path = %q, want %q", wt.Path, wantPath)
	}
	if wt.Branch != "wrap/run1/plan" {
		t.Errorf("Branch = %q, want %q", wt.Branch, "wrap/run1/plan")
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "README")); err != nil {
		t.Errorf("README missing from worktree: %v", err)
	}

	if err := m.Remove(ctx, repo, wt.Path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path still exists after Remove: err=%v", err)
	}
}

func TestManager_PruneRun(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	m := worktree.NewManager(stateDir)
	ctx := context.Background()

	// Two live worker worktrees plus a planner worktree we remove up-front to
	// mimic production (the planner worktree is gone but its branch lingers).
	add := func(branch, subpath string) worktree.Worktree {
		wt, err := m.Add(ctx, worktree.AddRequest{RepoPath: repo, Branch: branch, BaseRef: "main", Subpath: subpath})
		if err != nil {
			t.Fatalf("Add %s: %v", branch, err)
		}
		return wt
	}
	w1 := add("wrap/run1/w1", "runs/run1/w1")
	w2 := add("wrap/run1/w2", "runs/run1/w2")
	planWt := add("wrap/run1/plan", "runs/run1/plan")
	if err := m.Remove(ctx, repo, planWt.Path); err != nil {
		t.Fatalf("pre-remove planner worktree: %v", err)
	}

	// Hand PruneRun the (now-missing) planner worktree path too, to prove it
	// tolerates it. Branches include the planner branch, which still exists.
	worktrees := []string{w1.Path, w2.Path, planWt.Path}
	branches := []string{"wrap/run1/w1", "wrap/run1/w2", "wrap/run1/plan"}
	removed, deleted, err := m.PruneRun(ctx, repo, worktrees, branches)
	if err != nil {
		t.Fatalf("PruneRun: %v", err)
	}
	if removed != 2 {
		t.Errorf("worktreesRemoved = %d, want 2 (planner worktree already gone)", removed)
	}
	if deleted != 3 {
		t.Errorf("branchesDeleted = %d, want 3", deleted)
	}
	for _, p := range []string{w1.Path, w2.Path} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("worktree %s still exists after prune: err=%v", p, err)
		}
	}
	out, err := exec.Command("git", "-C", repo, "branch", "--list", "wrap/run1/*").CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("wrap/run1/* branches remain after prune: %q", out)
	}

	// Idempotent: a second prune over the same inputs is a no-op, not an error.
	removed2, deleted2, err := m.PruneRun(ctx, repo, worktrees, branches)
	if err != nil {
		t.Fatalf("second PruneRun: %v", err)
	}
	if removed2 != 0 || deleted2 != 0 {
		t.Errorf("second prune removed=%d deleted=%d, want 0/0", removed2, deleted2)
	}
}

func TestManager_Add_invalidRepo(t *testing.T) {
	requireGit(t)
	m := worktree.NewManager(t.TempDir())
	_, err := m.Add(context.Background(), worktree.AddRequest{
		RepoPath: "/nonexistent/repo",
		Branch:   "wrap/x/plan",
		BaseRef:  "main",
		Subpath:  "x",
	})
	if err == nil {
		t.Fatal("Add against nonexistent repo: want error, got nil")
	}
}

func TestManager_Add_branchAlreadyExists(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	m := worktree.NewManager(t.TempDir())
	ctx := context.Background()

	if _, err := m.Add(ctx, worktree.AddRequest{
		RepoPath: repo,
		Branch:   "wrap/dup",
		BaseRef:  "main",
		Subpath:  "a",
	}); err != nil {
		t.Fatalf("first Add: %v", err)
	}

	if _, err := m.Add(ctx, worktree.AddRequest{
		RepoPath: repo,
		Branch:   "wrap/dup", // same branch
		BaseRef:  "main",
		Subpath:  "b", // different path so the failure is about the branch, not the directory
	}); err == nil {
		t.Fatal("second Add with duplicate branch: want error, got nil")
	}
}
