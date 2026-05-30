package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/store"
)

// PruneRun is the coordinator behind `wrap prune`: it removes a terminal run's
// retained worktrees and deletes its wrap/<run>/* branches, then records a
// `pruned` event. The API's POST /runs/{id}/prune delegates here so the git
// plumbing stays serialized with worker worktree-adds (the shared wtMu) and out
// of the thin HTTP layer.
//
// It returns store.ErrNotFound for an unknown run and store.ErrRunNotTerminal
// when the run has not reached a terminal phase (the spec's "no data loss until
// the run has stopped" rule). The returned counts are the worktrees removed and
// branches deleted.
func (o *Orchestrator) PruneRun(ctx context.Context, runID string) (worktreesRemoved, branchesDeleted int, err error) {
	run, err := o.cfg.Store.GetRun(ctx, runID)
	if err != nil {
		return 0, 0, err // store.ErrNotFound for an unknown run
	}
	if !fsm.IsTerminal(fsm.Phase(run.Phase)) {
		return 0, 0, fmt.Errorf("prune run %s (phase %s): %w", runID, run.Phase, store.ErrRunNotTerminal)
	}
	proj, err := o.cfg.Store.GetProject(ctx, run.ProjectID)
	if err != nil {
		return 0, 0, fmt.Errorf("get project: %w", err)
	}
	workers, err := o.cfg.Store.ListWorkersByRun(ctx, runID)
	if err != nil {
		return 0, 0, fmt.Errorf("list workers: %w", err)
	}

	// Every worker row (including the planner/merger rows) contributes its
	// retained worktree path and branch. PruneRun tolerates an already-removed
	// worktree (e.g. the planner's) and an already-deleted branch.
	var worktrees, branches []string
	for _, w := range workers {
		if w.WorktreePath != "" {
			worktrees = append(worktrees, w.WorktreePath)
		}
		if w.Branch != "" {
			branches = append(branches, w.Branch)
		}
	}

	o.wtMu.Lock()
	wtCount, brCount, pruneErr := o.wt.PruneRun(ctx, proj.RepoPath, worktrees, branches)
	o.wtMu.Unlock()
	if pruneErr != nil {
		// Return the counts so the caller sees partial progress; the prune is
		// idempotent, so retrying is safe.
		return wtCount, brCount, fmt.Errorf("prune run %s: %w", runID, pruneErr)
	}

	payload, _ := json.Marshal(map[string]int{
		"worktrees_removed": wtCount,
		"branches_deleted":  brCount,
	})
	if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
		RunID: runID, Kind: "pruned", PayloadJSON: string(payload),
	}); err != nil {
		log.Printf("orchestrator: run %s record pruned event: %v", runID, err)
	}
	return wtCount, brCount, nil
}
