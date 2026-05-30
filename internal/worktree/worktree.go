// Package worktree wraps `git worktree` operations for the daemon.
// All paths are absolute; callers are responsible for choosing branch
// names and subpaths.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Manager creates worktrees rooted under a state directory (typically
// $WRAP_STATE_DIR, e.g. ~/.wrap). One Manager per daemon.
type Manager struct {
	stateDir string
}

func NewManager(stateDir string) *Manager {
	return &Manager{stateDir: stateDir}
}

// AddRequest names the worktree to create. RepoPath is the source repo;
// Branch is the new branch name (e.g. wrap/<run>/plan); BaseRef is the
// ref the new branch starts from; Subpath is appended to the manager's
// state dir to form the worktree's filesystem path.
type AddRequest struct {
	RepoPath string
	Branch   string
	BaseRef  string
	Subpath  string
}

type Worktree struct {
	Path   string
	Branch string
}

// Add runs `git worktree add -b <branch> <path> <baseRef>` and returns the
// resulting worktree. The parent directory of the worktree path is created
// as needed.
func (m *Manager) Add(ctx context.Context, req AddRequest) (Worktree, error) {
	if req.RepoPath == "" || req.Branch == "" || req.BaseRef == "" || req.Subpath == "" {
		return Worktree{}, fmt.Errorf("worktree.Add: RepoPath, Branch, BaseRef, Subpath are required")
	}
	wtPath := filepath.Join(m.stateDir, req.Subpath)
	// Create only the parent — `git worktree add` creates wtPath itself.
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o700); err != nil {
		return Worktree{}, fmt.Errorf("mkdir parent: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", req.Branch, wtPath, req.BaseRef)
	cmd.Dir = req.RepoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	return Worktree{Path: wtPath, Branch: req.Branch}, nil
}

// Remove runs `git worktree remove --force <path>` against the given repo.
// --force is required for Phase 2 because the planner writes untracked output
// files. WARNING: --force also discards uncommitted *tracked* changes. Phase 3+
// callers must ensure tracked changes are committed or intentionally abandoned
// before calling Remove, or replace --force with a softer fallback path that
// distinguishes dirty-tracked-changes from untracked-output cleanups.
// Branches are NOT deleted here — Phase 6 `wrap prune` handles branch cleanup.
func (m *Manager) Remove(ctx context.Context, repoPath, wtPath string) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", wtPath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, out)
	}
	return nil
}
