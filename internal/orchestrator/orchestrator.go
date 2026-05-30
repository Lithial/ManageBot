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

// MergerCmdFunc returns a freshly-configured *exec.Cmd for the merger
// subprocess. It is called once per run; the merge context (surviving branches,
// summaries, verification command) is passed in. Phase 4 wiring passes that
// context on stdin.
type MergerCmdFunc func(mergeContext string) *exec.Cmd

type Config struct {
	Store       *store.Store
	StateDir    string         // root for worktrees (e.g. ~/.wrap)
	PlannerCmd  PlannerCmdFunc // factory for the planner subprocess
	WorkerCmd   WorkerCmdFunc  // factory for worker subprocesses (Phase 3)
	MergerCmd   MergerCmdFunc  // factory for the merger subprocess (Phase 4)
	StepTimeout time.Duration  // per-step timeout for a planner/worker/merger subprocess

	// MaxWorkers caps simultaneous worker subprocesses per run (default 4).
	MaxWorkers int

	// RetryBudget is how many extra attempts a retryable worker failure (crash or
	// timeout) gets beyond the first. Zero means no retries.
	RetryBudget int
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

// Tick runs one orchestration pass, advancing each non-terminal run by one
// step: pending→plan via the planner, plan_gate and merge_gate via the gate
// engine, and merging via the merger. Errors on individual runs are logged but
// do not stop other runs in the same pass.
func (o *Orchestrator) Tick(ctx context.Context) error {
	// Ordered so a run created this tick can progress as far as its gates allow.
	if err := o.driveByPhase(ctx, fsm.PhasePending, o.drivePlanner); err != nil {
		return err
	}
	if err := o.driveByPhase(ctx, fsm.PhasePlanGate, o.drivePlanGate); err != nil {
		return err
	}
	// Merging is automatic work, not a gate; driveMerger self-guards when no
	// MergerCmd is configured (the run rests at merging).
	if err := o.driveByPhase(ctx, fsm.PhaseMerging, o.driveMerger); err != nil {
		return err
	}
	if err := o.driveByPhase(ctx, fsm.PhaseMergeGate, o.driveMergeGate); err != nil {
		return err
	}
	return nil
}

// driveByPhase applies `drive` to every run currently in `phase`. A per-run
// error is logged and does not abort the pass; only a list failure propagates.
func (o *Orchestrator) driveByPhase(ctx context.Context, phase fsm.Phase, drive func(context.Context, store.Run) error) error {
	runs, err := o.cfg.Store.ListRunsByPhase(ctx, string(phase))
	if err != nil {
		return fmt.Errorf("list %s: %w", phase, err)
	}
	for _, r := range runs {
		if err := drive(ctx, r); err != nil {
			log.Printf("orchestrator: run %s %s: %v", r.ID, phase, err)
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
