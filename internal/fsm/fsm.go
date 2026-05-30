// Package fsm defines the run phase state machine. It is pure: no I/O,
// no DB. The orchestrator composes fsm with the store to persist transitions.
package fsm

import "fmt"

type Phase string

const (
	PhasePending   Phase = "pending"
	PhasePlanning  Phase = "planning"
	PhasePlanGate  Phase = "plan_gate"
	PhaseWorking   Phase = "working"
	PhaseMerging   Phase = "merging"
	PhaseMergeGate Phase = "merge_gate"
	PhaseDone      Phase = "done"
	PhaseFailed    Phase = "failed"
	PhaseKilled    Phase = "killed"
)

type Event string

const (
	EventPlanStart   Event = "plan_start"
	EventPlanDone    Event = "plan_done"
	EventPlanFailed  Event = "plan_failed"
	EventWorkStart   Event = "work_start"   // Phase 3
	EventWorkDone    Event = "work_done"    // Phase 3
	EventWorkFailed  Event = "work_failed"  // Phase 3
	EventMergeStart  Event = "merge_start"  // Phase 4
	EventMergeDone   Event = "merge_done"   // Phase 4
	EventMergeFailed Event = "merge_failed" // Phase 4
	EventGateApprove Event = "gate_approve" // Phase 5
	EventGateReject  Event = "gate_reject"  // Phase 5
	EventKill        Event = "kill"
)

var validPhases = map[Phase]struct{}{
	PhasePending: {}, PhasePlanning: {}, PhasePlanGate: {},
	PhaseWorking: {}, PhaseMerging: {}, PhaseMergeGate: {},
	PhaseDone: {}, PhaseFailed: {}, PhaseKilled: {},
}

// ParsePhase converts a stored string back to a Phase, returning an error
// if the value is not a known phase.
func ParsePhase(s string) (Phase, error) {
	p := Phase(s)
	if _, ok := validPhases[p]; !ok {
		return "", fmt.Errorf("unknown phase %q", s)
	}
	return p, nil
}

// terminal phases never transition out.
var terminalPhases = map[Phase]struct{}{
	PhaseDone:   {},
	PhaseFailed: {},
	PhaseKilled: {},
}

// transitions[from][event] = to
var transitions = map[Phase]map[Event]Phase{
	PhasePending: {
		EventPlanStart: PhasePlanning,
	},
	PhasePlanning: {
		EventPlanDone:   PhasePlanGate,
		EventPlanFailed: PhaseFailed,
	},
	PhasePlanGate: {
		// Phase 3 auto-advances this (no gate engine until Phase 5). Phase 5 will
		// gate the work_start event behind plan-gate approval.
		EventWorkStart: PhaseWorking,
	},
	PhaseWorking: {
		EventWorkDone:   PhaseMerging,
		EventWorkFailed: PhaseFailed,
	},
	// Phase 4 fills in merging/merge_gate. Listed here for the kill-from-any rule.
	PhaseMerging:   {},
	PhaseMergeGate: {},
}

// Advance returns the new phase after applying event to from. It returns
// an error if the transition is not defined. Kill is allowed from any
// non-terminal phase.
func Advance(from Phase, event Event) (Phase, error) {
	if _, terminal := terminalPhases[from]; terminal {
		return "", fmt.Errorf("cannot advance from terminal phase %q (event: %q)", from, event)
	}
	if event == EventKill {
		return PhaseKilled, nil
	}
	row, ok := transitions[from]
	if !ok {
		return "", fmt.Errorf("no transitions defined from %q", from)
	}
	to, ok := row[event]
	if !ok {
		return "", fmt.Errorf("invalid event %q from phase %q", event, from)
	}
	return to, nil
}
