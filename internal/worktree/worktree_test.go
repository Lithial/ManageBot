package worktree_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/worktree"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// initRepo creates a temp git repo with one commit so worktree add has a base.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestManager_AddRemove(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
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
