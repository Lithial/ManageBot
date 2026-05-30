// Package worktree wraps `git worktree` operations for the daemon.
// All paths are absolute; callers are responsible for choosing branch
// names and subpaths.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// PruneRun is the destructive cleanup behind `wrap prune`: it removes each of
// the given worktrees and deletes each of the given branches from repoPath. It
// is deliberately narrow — it only ever runs `git worktree remove --force` and
// `git branch -D` on the exact paths/branches it is handed (the caller derives
// these from one run's worker rows), never `rm -rf` and never a wildcard.
//
// It is idempotent so a partial prune can be retried safely: worktree paths that
// no longer exist on disk are skipped (e.g. the planner worktree, already
// removed after planning), and branches that are already gone are tolerated.
// Worktrees are removed before branches because a branch checked out in a
// worktree cannot be deleted. The returned counts are the worktrees actually
// removed and the branches actually deleted; a non-nil error joins every hard
// failure encountered (all removals are still attempted).
func (m *Manager) PruneRun(ctx context.Context, repoPath string, worktrees, branches []string) (worktreesRemoved, branchesDeleted int, err error) {
	var errs []error
	for _, wt := range worktrees {
		if wt == "" {
			continue
		}
		// Tolerate an already-removed worktree (idempotent re-prune): if the path
		// is gone there is nothing to remove.
		if _, statErr := os.Stat(wt); os.IsNotExist(statErr) {
			continue
		}
		cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", wt)
		cmd.Dir = repoPath
		if out, rmErr := cmd.CombinedOutput(); rmErr != nil {
			errs = append(errs, fmt.Errorf("git worktree remove %s: %w\n%s", wt, rmErr, out))
			continue
		}
		worktreesRemoved++
	}
	// Clear any stale worktree admin entries (e.g. a worktree whose directory was
	// removed out-of-band) so the branch deletes below don't trip on "used by
	// worktree".
	prune := exec.CommandContext(ctx, "git", "worktree", "prune")
	prune.Dir = repoPath
	if out, pErr := prune.CombinedOutput(); pErr != nil {
		errs = append(errs, fmt.Errorf("git worktree prune: %w\n%s", pErr, out))
	}
	for _, br := range branches {
		if br == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, "git", "branch", "-D", br)
		cmd.Dir = repoPath
		out, brErr := cmd.CombinedOutput()
		if brErr != nil {
			// An already-deleted branch is fine (idempotent re-prune); anything else
			// is a real failure.
			if strings.Contains(string(out), "not found") {
				continue
			}
			errs = append(errs, fmt.Errorf("git branch -D %s: %w\n%s", br, brErr, out))
			continue
		}
		branchesDeleted++
	}
	return worktreesRemoved, branchesDeleted, errors.Join(errs...)
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
