package worktree_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
