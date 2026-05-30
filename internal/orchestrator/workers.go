package orchestrator

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/ids"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/supervisor"
	"github.com/Lithial/ManageBot/internal/workerrpc"
	"github.com/Lithial/ManageBot/internal/worktree"
)

const defaultMaxWorkers = 4

// driveWorkers advances a single run plan_gate → working → (merging | failed).
// It reads the persisted plan, schedules a worker subprocess per task under the
// DAG and the concurrency cap, persists each worker's outcome, and advances the
// FSM: merging if at least one worker is done, else failed.
//
// Worker worktrees and branches are RETAINED on every path — they are the
// merger's inputs (Phase 4), and the spec forbids deleting failed worktrees
// until `wrap prune`. (Contrast drivePlanner, which removes its worktree.)
func (o *Orchestrator) driveWorkers(ctx context.Context, r store.Run) error {
	if o.cfg.WorkerCmd == nil {
		return fmt.Errorf("AutoAdvanceGates set but WorkerCmd is nil")
	}

	proj, err := o.cfg.Store.GetProject(ctx, r.ProjectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	plan, err := o.cfg.Store.GetPlanByRun(ctx, r.ID)
	if err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("get plan: %w", err)
	}
	tasks, err := parseTasks(plan.TasksJSON)
	if err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("parse tasks: %w", err)
	}

	// Advance plan_gate → working.
	next, err := fsm.Advance(fsm.PhasePlanGate, fsm.EventWorkStart)
	if err != nil {
		return fmt.Errorf("fsm work_start: %w", err)
	}
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(next)); err != nil {
		return fmt.Errorf("update run phase working: %w", err)
	}

	maxWorkers := o.cfg.MaxWorkers
	if maxWorkers < 1 {
		maxWorkers = defaultMaxWorkers
	}

	// `git worktree add` takes repo-wide ref/index locks; serialize the (quick)
	// plumbing so parallel workers don't collide. The subprocesses themselves
	// still run concurrently.
	var wtMu sync.Mutex
	run := func(ctx context.Context, t Task) taskStatus {
		return o.runWorker(ctx, r, proj, t, &wtMu)
	}
	results := schedule(ctx, tasks, maxWorkers, run)

	anyDone := false
	for _, st := range results {
		if st == statusDone {
			anyDone = true
			break
		}
	}

	event := fsm.EventWorkFailed
	if anyDone {
		event = fsm.EventWorkDone
	}
	nextPhase, err := fsm.Advance(fsm.PhaseWorking, event)
	if err != nil {
		return fmt.Errorf("fsm %s: %w", event, err)
	}
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(nextPhase)); err != nil {
		return fmt.Errorf("update run phase %s: %w", nextPhase, err)
	}
	return nil
}

// runWorker creates an isolated worktree for one task, spawns the worker
// subprocess, interprets its outcome into a terminal status, and persists a
// workers row. It returns the task's terminal status for the scheduler.
func (o *Orchestrator) runWorker(ctx context.Context, r store.Run, proj store.Project, t Task, wtMu *sync.Mutex) taskStatus {
	wid := ids.New()
	branch := fmt.Sprintf("wrap/%s/%s", r.ID, wid)
	subpath := filepath.Join("runs", r.ID, wid)
	// Mirror worktree.Manager's path join so the row records the intended path
	// even if the git add below fails.
	wtPath := filepath.Join(o.cfg.StateDir, subpath)

	if _, err := o.cfg.Store.InsertWorker(ctx, store.Worker{
		ID: wid, RunID: r.ID, TaskID: t.ID, Branch: branch, WorktreePath: wtPath,
	}); err != nil {
		log.Printf("orchestrator: run %s task %s insert worker: %v", r.ID, t.ID, err)
		return statusFailed
	}

	wtMu.Lock()
	wt, err := o.wt.Add(ctx, worktree.AddRequest{
		RepoPath: proj.RepoPath,
		Branch:   branch,
		BaseRef:  "HEAD",
		Subpath:  subpath,
	})
	wtMu.Unlock()
	if err != nil {
		log.Printf("orchestrator: run %s task %s worktree add: %v", r.ID, t.ID, err)
		_ = o.cfg.Store.FinishWorker(ctx, wid, string(statusFailed), -1)
		return statusFailed
	}

	cmd := o.cfg.WorkerCmd(t.Title)
	cmd.Dir = wt.Path

	stepCtx := ctx
	if o.cfg.StepTimeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, o.cfg.StepTimeout)
		defer cancel()
	}
	out, err := supervisor.Run(stepCtx, supervisor.Request{
		Cmd:          cmd,
		StdinPayload: []byte(t.Title),
	})
	if err != nil {
		log.Printf("orchestrator: run %s task %s supervise: %v", r.ID, t.ID, err)
		_ = o.cfg.Store.FinishWorker(ctx, wid, string(statusFailed), out.ExitCode)
		return statusFailed
	}
	if out.MalformedLines > 0 {
		log.Printf("orchestrator: run %s task %s emitted %d malformed lines (protocol bug)", r.ID, t.ID, out.MalformedLines)
	}

	status, reason := interpretWorkerOutcome(out)
	if reason != "" {
		log.Printf("orchestrator: run %s task %s blocked: %s", r.ID, t.ID, reason)
	}
	if err := o.cfg.Store.FinishWorker(ctx, wid, string(status), out.ExitCode); err != nil {
		log.Printf("orchestrator: run %s task %s finish worker: %v", r.ID, t.ID, err)
	}
	return status
}

// interpretWorkerOutcome maps a supervisor Outcome to a terminal status per the
// spec "done predicate": report_done AND exit 0 → done; report_blocked → failed
// (with the reason); anything else → failed. If a worker both blocked and
// reported done, blocked wins — it asked for human judgment.
func interpretWorkerOutcome(out supervisor.Outcome) (taskStatus, string) {
	var sawDone, sawBlocked bool
	var blockedReason string
	for _, m := range out.Messages {
		switch m.Method {
		case workerrpc.MethodReportDone:
			sawDone = true
		case workerrpc.MethodReportBlocked:
			sawBlocked = true
			if b, err := workerrpc.AsBlocked(m); err == nil {
				blockedReason = b.Reason
			}
		}
	}
	if sawBlocked {
		return statusFailed, blockedReason
	}
	if sawDone && out.ExitCode == 0 {
		return statusDone, ""
	}
	return statusFailed, ""
}
