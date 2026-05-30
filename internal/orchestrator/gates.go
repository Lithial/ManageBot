package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/gates"
	"github.com/Lithial/ManageBot/internal/store"
)

// Gate resolutions returned by ensureGate.
const (
	gateApproved = "approved"
	gatePending  = "pending"
	gateRejected = "rejected"
)

// drivePlanGate evaluates the plan gate for a run at plan_gate. When approved
// (auto or human) it runs the worker phase; when rejected it fails the run;
// when pending it holds.
func (o *Orchestrator) drivePlanGate(ctx context.Context, r store.Run) error {
	resolution, err := o.evaluateGate(ctx, r, "plan")
	if err != nil {
		return err
	}
	switch resolution {
	case gateApproved:
		return o.driveWorkers(ctx, r)
	case gateRejected:
		return o.rejectGatePhase(ctx, r.ID, fsm.PhasePlanGate)
	default:
		return nil // pending: hold
	}
}

// driveMergeGate evaluates the merge gate for a run at merge_gate. When approved
// it finishes the run (→ done + run_done event); when rejected it fails the run;
// when pending it holds.
func (o *Orchestrator) driveMergeGate(ctx context.Context, r store.Run) error {
	resolution, err := o.evaluateGate(ctx, r, "merge")
	if err != nil {
		return err
	}
	switch resolution {
	case gateApproved:
		return o.finishMergeGate(ctx, r)
	case gateRejected:
		return o.rejectGatePhase(ctx, r.ID, fsm.PhaseMergeGate)
	default:
		return nil // pending: hold
	}
}

// openGate creates the gate for a phase a run is about to ENTER, so that by the
// time the run's phase reads plan_gate/merge_gate the gate already exists (no
// observable window where the phase has advanced but the gate is missing —
// mirroring the plan-before-phase invariant). Idempotent via ensureGate.
func (o *Orchestrator) openGate(ctx context.Context, r store.Run, kind string) error {
	pol, err := gates.Parse(r.GatesJSON)
	if err != nil {
		return fmt.Errorf("parse gates_json: %w", err)
	}
	if _, err := o.ensureGate(ctx, r.ID, kind, pol); err != nil {
		return err
	}
	return nil
}

// evaluateGate parses the run's gate policy and ensures a gate of `kind` exists,
// returning its resolution (approved | pending | rejected). A malformed
// gates_json is unrecoverable, so the run is failed.
func (o *Orchestrator) evaluateGate(ctx context.Context, r store.Run, kind string) (string, error) {
	pol, err := gates.Parse(r.GatesJSON)
	if err != nil {
		log.Printf("orchestrator: run %s malformed gates_json, failing: %v", r.ID, err)
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return "", fmt.Errorf("parse gates_json: %w", err)
	}
	return o.ensureGate(ctx, r.ID, kind, pol)
}

// ensureGate looks up the latest gate of `kind` for a run and, if none exists,
// creates one per policy: auto → an auto-approved gate; require_approval → a
// pending gate plus a gate_requested event. It returns the gate's resolution.
func (o *Orchestrator) ensureGate(ctx context.Context, runID, kind string, pol gates.Policy) (string, error) {
	g, err := o.cfg.Store.LatestGateByKind(ctx, runID, kind)
	if errors.Is(err, store.ErrNotFound) {
		if pol.Mode(kind) == gates.ModeAuto {
			if _, e := o.cfg.Store.InsertGate(ctx, store.Gate{
				RunID: runID, Kind: kind, Status: "auto-approved", ResolvedBy: "auto", PayloadJSON: "{}",
			}); e != nil {
				return "", fmt.Errorf("insert auto gate: %w", e)
			}
			return gateApproved, nil
		}
		if _, e := o.cfg.Store.InsertGate(ctx, store.Gate{
			RunID: runID, Kind: kind, Status: "pending", PayloadJSON: "{}",
		}); e != nil {
			return "", fmt.Errorf("insert pending gate: %w", e)
		}
		o.recordGateEvent(ctx, runID, "gate_requested", kind)
		return gatePending, nil
	}
	if err != nil {
		return "", fmt.Errorf("latest gate: %w", err)
	}
	switch g.Status {
	case "approved", "auto-approved":
		return gateApproved, nil
	case "rejected":
		return gateRejected, nil
	default:
		return gatePending, nil
	}
}

// rejectGatePhase advances a gated phase to failed on gate rejection.
func (o *Orchestrator) rejectGatePhase(ctx context.Context, runID string, from fsm.Phase) error {
	next, err := fsm.Advance(from, fsm.EventGateReject)
	if err != nil {
		return fmt.Errorf("fsm gate_reject from %s: %w", from, err)
	}
	if err := o.cfg.Store.UpdateRunPhase(ctx, runID, string(next)); err != nil {
		return fmt.Errorf("update run phase failed: %w", err)
	}
	return nil
}

// finishMergeGate advances a run merge_gate → done and records a run_done event
// — the "basic emission" signal.
func (o *Orchestrator) finishMergeGate(ctx context.Context, r store.Run) error {
	next, err := fsm.Advance(fsm.PhaseMergeGate, fsm.EventGateApprove)
	if err != nil {
		return fmt.Errorf("fsm gate_approve: %w", err)
	}
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(next)); err != nil {
		return fmt.Errorf("update run phase done: %w", err)
	}
	payload, _ := json.Marshal(map[string]string{"merge_branch": fmt.Sprintf("wrap/%s/merge", r.ID)})
	if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
		RunID: r.ID, Kind: "run_done", PayloadJSON: string(payload),
	}); err != nil {
		log.Printf("orchestrator: run %s record run_done: %v", r.ID, err)
	}
	return nil
}

func (o *Orchestrator) recordGateEvent(ctx context.Context, runID, eventKind, gateKind string) {
	payload, _ := json.Marshal(map[string]string{"kind": gateKind})
	if _, err := o.cfg.Store.InsertEvent(ctx, store.Event{
		RunID: runID, Kind: eventKind, PayloadJSON: string(payload),
	}); err != nil {
		log.Printf("orchestrator: run %s record %s: %v", runID, eventKind, err)
	}
}
