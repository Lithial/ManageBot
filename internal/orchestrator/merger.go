package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/supervisor"
	"github.com/Lithial/ManageBot/internal/worktree"
)

// driveMerger advances a single run merging → (merge_gate | failed). It gathers
// the surviving worker branches (status done) and their summaries, spawns one
// merger subprocess in a wrap/<run>/merge worktree, and on report_done + exit 0
// records a merge_done event and advances to merge_gate; otherwise the run fails.
//
// If no MergerCmd is configured the run rests at merging (returns nil) — merging
// is automatic work, but without a merger binary there is nothing to drive.
//
// The merge worktree/branch is RETAINED (the output artifact), like worker
// worktrees.
func (o *Orchestrator) driveMerger(ctx context.Context, r store.Run) error {
	if o.cfg.MergerCmd == nil {
		return nil
	}

	proj, err := o.cfg.Store.GetProject(ctx, r.ProjectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	survivors, err := o.survivingBranches(ctx, r.ID)
	if err != nil {
		return err
	}
	if len(survivors) == 0 {
		// FSM should not have reached merging with zero done workers, but guard.
		log.Printf("orchestrator: run %s merging with no surviving workers", r.ID)
		return o.failMerge(ctx, r.ID)
	}

	branch := fmt.Sprintf("wrap/%s/merge", r.ID)
	subpath := filepath.Join("runs", r.ID, "merge")
	wt, err := o.wt.Add(ctx, worktree.AddRequest{
		RepoPath: proj.RepoPath,
		Branch:   branch,
		BaseRef:  "HEAD",
		Subpath:  subpath,
	})
	if err != nil {
		log.Printf("orchestrator: run %s merge worktree add: %v", r.ID, err)
		return o.failMerge(ctx, r.ID)
	}

	// Give the merger a worker row so it can report done over MCP, scoped by id.
	mergerWID, err := o.cfg.Store.InsertWorker(ctx, store.Worker{
		RunID: r.ID, TaskID: taskIDMerger, Branch: branch, WorktreePath: wt.Path,
	})
	if err != nil {
		log.Printf("orchestrator: run %s insert merger worker: %v", r.ID, err)
		return o.failMerge(ctx, r.ID)
	}

	mergeContext := buildMergeContext(survivors, proj.VerificationCommand)
	cmd := o.cfg.MergerCmd(mergerWID)
	cmd.Dir = wt.Path

	// Run-scoped context so `wrap kill` can stop the merger promptly.
	runCtx, cancelRun := context.WithCancel(ctx)
	o.kills.register(r.ID, cancelRun)
	defer func() { o.kills.deregister(r.ID); cancelRun() }()

	stepCtx := runCtx
	if o.cfg.StepTimeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(runCtx, o.cfg.StepTimeout)
		defer cancel()
	}
	out, err := supervisor.Run(stepCtx, supervisor.Request{
		Cmd:          cmd,
		StdinPayload: []byte(mergeContext),
	})
	if o.isKilled(ctx, r.ID) {
		return nil // killed mid-merge; leave the terminal phase alone
	}
	if err != nil {
		log.Printf("orchestrator: run %s supervise merger: %v", r.ID, err)
		_ = o.cfg.Store.FinishWorker(ctx, mergerWID, string(statusFailed), out.ExitCode)
		return o.failMerge(ctx, r.ID)
	}
	if out.MalformedLines > 0 {
		log.Printf("orchestrator: run %s merger emitted %d malformed lines (protocol bug)", r.ID, out.MalformedLines)
	}

	// The merger reports done the same way a worker does (report_done + exit 0).
	status, summary, _ := o.workerOutcome(ctx, mergerWID, out)
	_ = o.cfg.Store.FinishWorker(ctx, mergerWID, string(status), out.ExitCode)
	if status != statusDone {
		logPlannerStderrTail(r.ID, out)
		return o.failMerge(ctx, r.ID)
	}

	payload, _ := json.Marshal(map[string]string{"branch": branch, "summary": summary})
	if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
		RunID: r.ID, Kind: "merge_done", PayloadJSON: string(payload),
	}); err != nil {
		log.Printf("orchestrator: run %s record merge_done: %v", r.ID, err)
	}

	// Open the merge gate before advancing, so phase==merge_gate always has the
	// gate present (no observable window).
	if err := o.openGate(ctx, r, "merge"); err != nil {
		log.Printf("orchestrator: run %s open merge gate: %v", r.ID, err)
		return o.failMerge(ctx, r.ID)
	}
	next, err := fsm.Advance(fsm.PhaseMerging, fsm.EventMergeDone)
	if err != nil {
		return fmt.Errorf("fsm merge_done: %w", err)
	}
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(next)); err != nil {
		return fmt.Errorf("update run phase merge_gate: %w", err)
	}
	return nil
}

// mergeInput pairs a surviving worker's branch with its reported summary.
type mergeInput struct {
	Branch  string
	TaskID  string
	Summary string
}

// survivingBranches returns the branches of workers that reached `done`, paired
// with the summaries they reported (from worker_done events).
func (o *Orchestrator) survivingBranches(ctx context.Context, runID string) ([]mergeInput, error) {
	workers, err := o.cfg.Store.ListWorkersByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	summaries := make(map[string]string) // worker_id → summary
	events, err := o.cfg.Store.ListEventsByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	for _, e := range events {
		if e.Kind != "worker_done" || e.WorkerID == "" {
			continue
		}
		var p struct {
			Summary string `json:"summary"`
		}
		if json.Unmarshal([]byte(e.PayloadJSON), &p) == nil {
			summaries[e.WorkerID] = p.Summary
		}
	}
	var out []mergeInput
	for _, w := range workers {
		// Only real task-workers are merge inputs — not the planner/merger rows.
		if w.Status != string(statusDone) || w.TaskID == taskIDPlanner || w.TaskID == taskIDMerger {
			continue
		}
		out = append(out, mergeInput{Branch: w.Branch, TaskID: w.TaskID, Summary: summaries[w.ID]})
	}
	return out, nil
}

// buildMergeContext renders the merger's stdin payload: the branches to merge,
// their summaries, and the verification command to run after merging.
func buildMergeContext(inputs []mergeInput, verificationCommand string) string {
	var b strings.Builder
	b.WriteString("Merge the following worker branches into the current branch.\n\n")
	for _, in := range inputs {
		fmt.Fprintf(&b, "- branch %s (task %s): %s\n", in.Branch, in.TaskID, in.Summary)
	}
	if verificationCommand != "" {
		fmt.Fprintf(&b, "\nAfter merging, run the verification command and only report done if it passes:\n  %s\n", verificationCommand)
	}
	return b.String()
}

// failMerge transitions a run merging → failed.
func (o *Orchestrator) failMerge(ctx context.Context, runID string) error {
	next, err := fsm.Advance(fsm.PhaseMerging, fsm.EventMergeFailed)
	if err != nil {
		return fmt.Errorf("fsm merge_failed: %w", err)
	}
	if err := o.cfg.Store.UpdateRunPhase(ctx, runID, string(next)); err != nil {
		return fmt.Errorf("update run phase failed: %w", err)
	}
	return nil
}
