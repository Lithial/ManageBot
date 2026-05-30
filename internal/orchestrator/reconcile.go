package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/store"
)

// midActivePhases are the transient phases whose work runs in a single
// subprocess (planner/merger). A run caught in one of these at startup cannot be
// safely resumed (the one in-flight subprocess is gone), so reconciliation fails
// it for inspection. `working` is handled separately — it fans out per-task and
// is resumed from partial progress, not failed (see Reconcile). Gate phases and
// pending are safe to resume and are left for the tick loop.
var midActivePhases = []fsm.Phase{fsm.PhasePlanning, fsm.PhaseMerging}

// Reconcile recovers daemon state after a restart: orphaned 'running' workers
// (no live process backs them) are marked failed with reason daemon_restart;
// runs caught in a single-subprocess phase (planning/merging) are failed
// (resuming mid-subprocess is unsafe); and runs caught mid-`working` are left at
// `working` to resume from partial progress (completed tasks stay done, orphaned
// workers were just failed as retryable). Runs parked at a gate or pending are
// left untouched for the tick loop. Worktrees and branches are preserved.
//
// Call once at startup, before the tick loop.
func (o *Orchestrator) Reconcile(ctx context.Context) error {
	orphans, err := o.cfg.Store.ListRunningWorkers(ctx)
	if err != nil {
		return fmt.Errorf("list running workers: %w", err)
	}
	for _, w := range orphans {
		if err := o.cfg.Store.FinishWorker(ctx, w.ID, string(statusFailed), -1); err != nil {
			log.Printf("orchestrator: reconcile finish worker %s: %v", w.ID, err)
		}
		payload, _ := json.Marshal(map[string]string{"task_id": w.TaskID, "reason": "daemon_restart"})
		if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
			RunID: w.RunID, WorkerID: w.ID, Kind: "worker_failed", PayloadJSON: string(payload),
		}); err != nil {
			log.Printf("orchestrator: reconcile event for worker %s: %v", w.ID, err)
		}
	}

	// Mid-`working` runs resume rather than fail: leave the phase at `working`
	// (the tick loop's resumeWorkers re-derives completed tasks from the event
	// log and re-dispatches only the unfinished ones) and record the recovery.
	working, err := o.cfg.Store.ListRunsByPhase(ctx, string(fsm.PhaseWorking))
	if err != nil {
		return fmt.Errorf("list working: %w", err)
	}
	for _, r := range working {
		payload, _ := json.Marshal(map[string]string{"from_phase": string(fsm.PhaseWorking), "reason": "resume"})
		if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
			RunID: r.ID, Kind: "daemon_recovered", PayloadJSON: string(payload),
		}); err != nil {
			log.Printf("orchestrator: reconcile event for run %s: %v", r.ID, err)
		}
		log.Printf("orchestrator: reconcile resuming run %s (mid-working) after daemon restart", r.ID)
	}

	for _, phase := range midActivePhases {
		runs, err := o.cfg.Store.ListRunsByPhase(ctx, string(phase))
		if err != nil {
			return fmt.Errorf("list %s: %w", phase, err)
		}
		for _, r := range runs {
			if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed)); err != nil {
				log.Printf("orchestrator: reconcile fail run %s: %v", r.ID, err)
				continue
			}
			payload, _ := json.Marshal(map[string]string{"from_phase": string(phase), "reason": "daemon_restart"})
			if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
				RunID: r.ID, Kind: "daemon_recovered", PayloadJSON: string(payload),
			}); err != nil {
				log.Printf("orchestrator: reconcile event for run %s: %v", r.ID, err)
			}
			log.Printf("orchestrator: reconcile failed run %s (was %s) after daemon restart", r.ID, phase)
		}
	}
	return nil
}
