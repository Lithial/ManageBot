package orchestrator

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/supervisor"
	"github.com/Lithial/ManageBot/internal/workerrpc"
	"github.com/Lithial/ManageBot/internal/worktree"
)

// logPlannerOutput emits diagnostic lines for a failed planner outcome.
// stderr can be large; we log a bounded tail.
func logPlannerOutput(runID string, out supervisor.Outcome) {
	if len(out.Stderr) > 0 {
		tail := out.Stderr
		const max = 2048
		if len(tail) > max {
			tail = tail[len(tail)-max:]
		}
		log.Printf("orchestrator: run %s planner stderr (tail): %s", runID, tail)
	}
	if out.MalformedLines > 0 {
		log.Printf("orchestrator: run %s planner emitted %d malformed lines (protocol bug)", runID, out.MalformedLines)
	}
	if out.WaitErr != nil {
		log.Printf("orchestrator: run %s planner wait error: %v", runID, out.WaitErr)
	}
}

// drivePlanner advances a single run pending → planning → (plan_gate | failed).
// It is best-effort idempotent: if the worktree already exists from a prior
// failed attempt, this returns an error rather than retrying (Phase 8 will
// add retry budgeting).
func (o *Orchestrator) drivePlanner(ctx context.Context, r store.Run) error {
	proj, err := o.cfg.Store.GetProject(ctx, r.ProjectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	// Advance pending → planning.
	next, err := fsm.Advance(fsm.PhasePending, fsm.EventPlanStart)
	if err != nil {
		return fmt.Errorf("fsm: %w", err)
	}
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(next)); err != nil {
		return fmt.Errorf("update run phase planning: %w", err)
	}

	// Create the planner worktree.
	wt, err := o.wt.Add(ctx, worktree.AddRequest{
		RepoPath: proj.RepoPath,
		Branch:   fmt.Sprintf("wrap/%s/plan", r.ID),
		BaseRef:  "HEAD",
		Subpath:  filepath.Join("runs", r.ID, "plan"),
	})
	if err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("worktree add: %w", err)
	}
	defer func() {
		_ = o.wt.Remove(context.Background(), proj.RepoPath, wt.Path)
	}()

	// Spawn the planner.
	cmd := o.cfg.PlannerCmd(r.SpecMD)
	cmd.Dir = wt.Path

	stepCtx := ctx
	if o.cfg.StepTimeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, o.cfg.StepTimeout)
		defer cancel()
	}
	out, err := supervisor.Run(stepCtx, supervisor.Request{
		Cmd:          cmd,
		StdinPayload: []byte(r.SpecMD),
	})
	if err != nil {
		logPlannerOutput(r.ID, out)
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("supervise planner: %w", err)
	}

	// Find the plan message. Take the last valid one; if the planner emits
	// multiple report_plan messages, treat each as a revision superseding
	// the previous. Malformed plan messages are silently skipped so a
	// trailing valid one still wins.
	var planMsg *workerrpc.PlanParams
	for _, m := range out.Messages {
		if m.Method == workerrpc.MethodReportPlan {
			p, perr := workerrpc.AsPlan(m)
			if perr != nil {
				continue
			}
			planMsg = &p
		}
	}

	// Always surface protocol/wait diagnostics; failure paths log stderr too.
	if out.MalformedLines > 0 {
		log.Printf("orchestrator: run %s planner emitted %d malformed lines (protocol bug)", r.ID, out.MalformedLines)
	}
	if out.WaitErr != nil {
		log.Printf("orchestrator: run %s planner wait error: %v", r.ID, out.WaitErr)
	}

	if out.ExitCode != 0 || planMsg == nil {
		if len(out.Stderr) > 0 {
			tail := out.Stderr
			const max = 2048
			if len(tail) > max {
				tail = tail[len(tail)-max:]
			}
			log.Printf("orchestrator: run %s planner stderr (tail): %s", r.ID, tail)
		}
		nextPhase, _ := fsm.Advance(fsm.PhasePlanning, fsm.EventPlanFailed)
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(nextPhase))
		return fmt.Errorf("planner failed: exit=%d hasPlan=%v", out.ExitCode, planMsg != nil)
	}

	if _, err := o.cfg.Store.InsertPlan(ctx, store.Plan{
		RunID:     r.ID,
		PlanMD:    planMsg.PlanMD,
		TasksJSON: planMsg.TasksJSON,
	}); err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("insert plan: %w", err)
	}
	nextPhase, _ := fsm.Advance(fsm.PhasePlanning, fsm.EventPlanDone)
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(nextPhase)); err != nil {
		return fmt.Errorf("update run phase plan_gate: %w", err)
	}
	return nil
}
