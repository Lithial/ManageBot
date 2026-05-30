package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/supervisor"
	"github.com/Lithial/ManageBot/internal/workerrpc"
	"github.com/Lithial/ManageBot/internal/worktree"
)

// logPlannerStderrTail emits a bounded tail of the planner's stderr for
// diagnostic context on failure paths. Large stderr buffers are clipped
// at maxStderrTail bytes to keep logs digestible.
func logPlannerStderrTail(runID string, out supervisor.Outcome) {
	if len(out.Stderr) == 0 {
		return
	}
	const maxStderrTail = 2048
	tail := out.Stderr
	if len(tail) > maxStderrTail {
		tail = tail[len(tail)-maxStderrTail:]
	}
	log.Printf("orchestrator: run %s planner stderr (tail): %s", runID, tail)
}

// plannerPlan reads the reported plan, preferring the MCP worker_report_plan
// event and falling back to the stdout NDJSON the test shim emits.
func (o *Orchestrator) plannerPlan(ctx context.Context, plannerWID string, out supervisor.Outcome) (planMD, tasksJSON string, ok bool) {
	if ev, err := o.cfg.Store.LatestWorkerEventByKind(ctx, plannerWID, "worker_report_plan"); err == nil {
		var p struct {
			PlanMD    string `json:"plan_md"`
			TasksJSON string `json:"tasks_json"`
		}
		if json.Unmarshal([]byte(ev.PayloadJSON), &p) == nil && (p.PlanMD != "" || p.TasksJSON != "") {
			return p.PlanMD, p.TasksJSON, true
		}
	}
	// Fallback: last valid report_plan on stdout (revisions supersede).
	var planMsg *workerrpc.PlanParams
	for _, m := range out.Messages {
		if m.Method == workerrpc.MethodReportPlan {
			if p, perr := workerrpc.AsPlan(m); perr == nil {
				planMsg = &p
			}
		}
	}
	if planMsg != nil {
		return planMsg.PlanMD, planMsg.TasksJSON, true
	}
	return "", "", false
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

	// Give the planner a worker row so it can report its plan over MCP, scoped
	// by this id.
	plannerWID, err := o.cfg.Store.InsertWorker(ctx, store.Worker{
		RunID: r.ID, TaskID: taskIDPlanner, Branch: wt.Branch, WorktreePath: wt.Path,
	})
	if err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("insert planner worker: %w", err)
	}

	// Spawn the planner.
	cmd := o.cfg.PlannerCmd(plannerWID)
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
		logPlannerStderrTail(r.ID, out)
		_ = o.cfg.Store.FinishWorker(ctx, plannerWID, string(fsm.PhaseFailed), out.ExitCode)
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("supervise planner: %w", err)
	}

	// Always surface protocol/wait diagnostics; failure paths log stderr too.
	if out.MalformedLines > 0 {
		log.Printf("orchestrator: run %s planner emitted %d malformed lines (protocol bug)", r.ID, out.MalformedLines)
	}
	if out.WaitErr != nil {
		log.Printf("orchestrator: run %s planner wait error: %v", r.ID, out.WaitErr)
	}

	// Prefer the MCP-reported plan (worker_report_plan event); fall back to the
	// stdout NDJSON the shim emits.
	planMD, tasksJSON, hasPlan := o.plannerPlan(ctx, plannerWID, out)
	plannerStatus := statusFailed
	if hasPlan && out.ExitCode == 0 {
		plannerStatus = statusDone
	}
	_ = o.cfg.Store.FinishWorker(ctx, plannerWID, string(plannerStatus), out.ExitCode)

	if out.ExitCode != 0 || !hasPlan {
		logPlannerStderrTail(r.ID, out)
		nextPhase, _ := fsm.Advance(fsm.PhasePlanning, fsm.EventPlanFailed)
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(nextPhase))
		return fmt.Errorf("planner failed: exit=%d hasPlan=%v", out.ExitCode, hasPlan)
	}

	if _, err := o.cfg.Store.InsertPlan(ctx, store.Plan{
		RunID:     r.ID,
		PlanMD:    planMD,
		TasksJSON: tasksJSON,
	}); err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("insert plan: %w", err)
	}
	// Open the plan gate before advancing, so phase==plan_gate always has both
	// the plan and the gate present.
	if err := o.openGate(ctx, r, "plan"); err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("open plan gate: %w", err)
	}
	nextPhase, _ := fsm.Advance(fsm.PhasePlanning, fsm.EventPlanDone)
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(nextPhase)); err != nil {
		return fmt.Errorf("update run phase plan_gate: %w", err)
	}
	return nil
}
