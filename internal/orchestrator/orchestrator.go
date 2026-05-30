// Package orchestrator drives runs through the phase state machine by
// composing the FSM, store, worktree manager, and supervisor. Phase 2
// implements only the planner phase: pending → planning → plan_gate.
package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"time"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/worktree"
)

// PlannerCmdFunc returns a freshly-configured *exec.Cmd for the planner
// subprocess. It is called once per run; the spec markdown is passed in
// so callers can wire it into the command (e.g. via env or args) if they
// don't want it on stdin. Phase 2 production wiring passes spec on stdin
// via supervisor.Request.StdinPayload, so this func only configures the
// program path, env, and args.
type PlannerCmdFunc func(specMD string) *exec.Cmd

// WorkerCmdFunc returns a freshly-configured *exec.Cmd for one worker
// subprocess. It is called once per task; the task description is passed in so
// callers can wire it into the command if they don't want it on stdin. Phase 3
// production wiring passes the task description on stdin.
type WorkerCmdFunc func(taskDescription string) *exec.Cmd

type Config struct {
	Store       *store.Store
	StateDir    string         // root for worktrees (e.g. ~/.wrap)
	PlannerCmd  PlannerCmdFunc // factory for the planner subprocess
	WorkerCmd   WorkerCmdFunc  // factory for worker subprocesses (Phase 3)
	StepTimeout time.Duration  // per-step timeout for a planner/worker subprocess

	// MaxWorkers caps simultaneous worker subprocesses per run (default 4).
	MaxWorkers int

	// AutoAdvanceGates drives plan_gate runs straight into the working phase
	// without human approval. Phase 3 scaffold: there is no gate engine yet
	// (Phase 5), so this crude boolean stands in for gates.plan.mode == "auto".
	// Default false keeps runs resting at plan_gate, matching Phase 2.
	AutoAdvanceGates bool
}

type Orchestrator struct {
	cfg Config
	wt  *worktree.Manager
}

func New(cfg Config) *Orchestrator {
	return &Orchestrator{
		cfg: cfg,
		wt:  worktree.NewManager(cfg.StateDir),
	}
}

// Tick runs one orchestration pass: advance every pending run by one
// planner phase. Errors on individual runs are logged but do not stop
// other runs in the same pass.
func (o *Orchestrator) Tick(ctx context.Context) error {
	pending, err := o.cfg.Store.ListRunsByPhase(ctx, string(fsm.PhasePending))
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	for _, r := range pending {
		if err := o.drivePlanner(ctx, r); err != nil {
			log.Printf("orchestrator: run %s planner: %v", r.ID, err)
		}
	}

	// plan_gate → working → (merging | failed). Phase 3 auto-advances the plan
	// gate only when configured to; Phase 5 replaces this with the gate engine.
	if o.cfg.AutoAdvanceGates {
		gated, err := o.cfg.Store.ListRunsByPhase(ctx, string(fsm.PhasePlanGate))
		if err != nil {
			return fmt.Errorf("list plan_gate: %w", err)
		}
		for _, r := range gated {
			if err := o.driveWorkers(ctx, r); err != nil {
				log.Printf("orchestrator: run %s workers: %v", r.ID, err)
			}
		}
	}
	return nil
}

// Run loops Tick on interval until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context, interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		if err := o.Tick(ctx); err != nil {
			log.Printf("orchestrator: tick: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}
