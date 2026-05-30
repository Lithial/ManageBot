package orchestrator

import (
	"context"
	"encoding/json"
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

	// Run-scoped context so `wrap kill` can cancel all of this run's workers.
	runCtx, cancel := context.WithCancel(ctx)
	o.kills.register(r.ID, cancel)
	defer func() { o.kills.deregister(r.ID); cancel() }()

	// `git worktree add` takes repo-wide ref/index locks; serialize the (quick)
	// plumbing so parallel workers don't collide. The subprocesses themselves
	// still run concurrently.
	var wtMu sync.Mutex
	run := func(ctx context.Context, t Task) taskStatus {
		return o.runWorker(ctx, r, proj, t, &wtMu)
	}
	results := schedule(runCtx, tasks, maxWorkers, run)

	// If the run was killed mid-work, leave the terminal `killed` phase alone.
	if o.isKilled(ctx, r.ID) {
		return nil
	}

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

// runWorker runs a task to a terminal status, retrying retryable failures
// (crash / timeout, never report_blocked or success) up to RetryBudget. Each
// attempt is its own workers row + worktree for forensics.
func (o *Orchestrator) runWorker(ctx context.Context, r store.Run, proj store.Project, t Task, wtMu *sync.Mutex) taskStatus {
	attempts := o.cfg.RetryBudget + 1 // budget extra tries beyond the first
	if attempts < 1 {
		attempts = 1
	}
	var status taskStatus
	for attempt := 0; attempt < attempts; attempt++ {
		var retryable bool
		status, retryable = o.runWorkerAttempt(ctx, r, proj, t, wtMu)
		if status == statusDone || !retryable {
			return status
		}
		if attempt < attempts-1 {
			o.recordWorkerEvent(ctx, r.ID, "", t, statusFailed, "", "")
			payload, _ := json.Marshal(map[string]any{"task_id": t.ID, "next_attempt": attempt + 1})
			_, _ = o.cfg.Store.InsertEvent(ctx, store.Event{RunID: r.ID, Kind: "worker_retry", PayloadJSON: string(payload)})
			log.Printf("orchestrator: run %s task %s failed (retryable); retrying (%d/%d)", r.ID, t.ID, attempt+1, attempts-1)
		}
	}
	return status
}

// runWorkerAttempt performs one spawn of a task and reports its terminal status
// plus whether the failure (if any) is worth retrying.
func (o *Orchestrator) runWorkerAttempt(ctx context.Context, r store.Run, proj store.Project, t Task, wtMu *sync.Mutex) (status taskStatus, retryable bool) {
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
		return statusFailed, false
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
		// A git failure will likely recur — not worth retrying.
		log.Printf("orchestrator: run %s task %s worktree add: %v", r.ID, t.ID, err)
		_ = o.cfg.Store.FinishWorker(ctx, wid, string(statusFailed), -1)
		o.recordWorkerEvent(ctx, r.ID, wid, t, statusFailed, "", "")
		return statusFailed, false
	}

	cmd := o.cfg.WorkerCmd(wid)
	cmd.Dir = wt.Path

	stepCtx := ctx
	if o.cfg.StepTimeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, o.cfg.StepTimeout)
		defer cancel()
	}
	out, runErr := supervisor.Run(stepCtx, supervisor.Request{
		Cmd:          cmd,
		StdinPayload: []byte(t.Title),
	})

	// A hit step deadline means the worker overran its budget and was killed.
	if stepCtx.Err() == context.DeadlineExceeded {
		log.Printf("orchestrator: run %s task %s timed out", r.ID, t.ID)
		_ = o.cfg.Store.FinishWorker(ctx, wid, string(statusFailed), out.ExitCode)
		o.recordWorkerTimeout(ctx, r.ID, wid, t)
		return statusFailed, true
	}
	if runErr != nil {
		log.Printf("orchestrator: run %s task %s supervise: %v", r.ID, t.ID, runErr)
		_ = o.cfg.Store.FinishWorker(ctx, wid, string(statusFailed), out.ExitCode)
		o.recordWorkerEvent(ctx, r.ID, wid, t, statusFailed, "", "")
		return statusFailed, true
	}
	if out.MalformedLines > 0 {
		log.Printf("orchestrator: run %s task %s emitted %d malformed lines (protocol bug)", r.ID, t.ID, out.MalformedLines)
	}

	status, summary, reason := o.workerOutcome(ctx, wid, out)
	if reason != "" {
		log.Printf("orchestrator: run %s task %s blocked: %s", r.ID, t.ID, reason)
	}
	if err := o.cfg.Store.FinishWorker(ctx, wid, string(status), out.ExitCode); err != nil {
		log.Printf("orchestrator: run %s task %s finish worker: %v", r.ID, t.ID, err)
	}
	o.recordWorkerEvent(ctx, r.ID, wid, t, status, summary, reason)
	switch {
	case status == statusDone:
		return statusDone, false
	case reason != "":
		return statusFailed, false // blocked: needs a human, do not retry
	default:
		return statusFailed, true // crash: retryable
	}
}

// recordWorkerTimeout records a worker_timeout event for a worker killed at its
// runtime ceiling.
func (o *Orchestrator) recordWorkerTimeout(ctx context.Context, runID, workerID string, t Task) {
	payload, _ := json.Marshal(map[string]string{"task_id": t.ID, "reason": "timeout"})
	if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
		RunID: runID, WorkerID: workerID, Kind: "worker_timeout", PayloadJSON: string(payload),
	}); err != nil {
		log.Printf("orchestrator: run %s task %s record worker_timeout: %v", runID, t.ID, err)
	}
}

// recordWorkerEvent appends a terminal-status event for a worker so the merger
// (and the emission log) can see what each branch produced. Best-effort: a
// failed event write is logged, not fatal.
func (o *Orchestrator) recordWorkerEvent(ctx context.Context, runID, workerID string, t Task, status taskStatus, summary, reason string) {
	var kind string
	payload := map[string]string{"task_id": t.ID}
	switch {
	case status == statusDone:
		kind = "worker_done"
		payload["summary"] = summary
	case reason != "":
		kind = "worker_blocked"
		payload["reason"] = reason
	default:
		kind = "worker_failed"
	}
	b, _ := json.Marshal(payload)
	if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
		RunID: runID, WorkerID: workerID, Kind: kind, PayloadJSON: string(b),
	}); err != nil {
		log.Printf("orchestrator: run %s task %s record %s event: %v", runID, t.ID, kind, err)
	}
}

// workerOutcome determines a worker's terminal status, preferring its
// MCP-reported outcome (recorded as worker_report_* events by the daemon) and
// falling back to the stdout NDJSON the test shim emits when no MCP report
// exists. `done` still requires exit 0 (the spec done-predicate).
func (o *Orchestrator) workerOutcome(ctx context.Context, wid string, out supervisor.Outcome) (status taskStatus, summary, reason string) {
	if ev, err := o.cfg.Store.LatestWorkerEventByKind(ctx, wid, "worker_report_blocked"); err == nil {
		var p struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(ev.PayloadJSON), &p)
		return statusFailed, "", p.Reason // blocked wins; not retryable
	}
	if ev, err := o.cfg.Store.LatestWorkerEventByKind(ctx, wid, "worker_report_done"); err == nil {
		var p struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal([]byte(ev.PayloadJSON), &p)
		if out.ExitCode == 0 {
			return statusDone, p.Summary, ""
		}
		return statusFailed, "", ""
	}
	return interpretWorkerOutcome(out)
}

// interpretWorkerOutcome maps a supervisor Outcome to a terminal status plus a
// done summary and a blocked reason, per the spec "done predicate": report_done
// AND exit 0 → done (with summary); report_blocked → failed (with reason);
// anything else → failed. If a worker both blocked and reported done, blocked
// wins — it asked for human judgment.
func interpretWorkerOutcome(out supervisor.Outcome) (status taskStatus, summary, reason string) {
	var sawDone, sawBlocked bool
	for _, m := range out.Messages {
		switch m.Method {
		case workerrpc.MethodReportDone:
			sawDone = true
			if d, err := workerrpc.AsDone(m); err == nil {
				summary = d.Summary
			}
		case workerrpc.MethodReportBlocked:
			sawBlocked = true
			if b, err := workerrpc.AsBlocked(m); err == nil {
				reason = b.Reason
			}
		}
	}
	if sawBlocked {
		return statusFailed, "", reason
	}
	if sawDone && out.ExitCode == 0 {
		return statusDone, summary, ""
	}
	return statusFailed, "", ""
}
