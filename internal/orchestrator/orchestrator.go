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

type Config struct {
	Store       *store.Store
	StateDir    string         // root for worktrees (e.g. ~/.wrap)
	PlannerCmd  PlannerCmdFunc // factory for the planner subprocess
	StepTimeout time.Duration  // per-step timeout for planner subprocess
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
