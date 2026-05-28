package fsm_test

import (
	"testing"

	"github.com/Lithial/ManageBot/internal/fsm"
)

func TestPhaseConstants(t *testing.T) {
	// Round-trip every defined phase through string form.
	phases := []fsm.Phase{
		fsm.PhasePending,
		fsm.PhasePlanning,
		fsm.PhasePlanGate,
		fsm.PhaseWorking,
		fsm.PhaseMerging,
		fsm.PhaseMergeGate,
		fsm.PhaseDone,
		fsm.PhaseFailed,
		fsm.PhaseKilled,
	}
	for _, p := range phases {
		got, err := fsm.ParsePhase(string(p))
		if err != nil {
			t.Errorf("ParsePhase(%q) error: %v", p, err)
			continue
		}
		if got != p {
			t.Errorf("ParsePhase(%q) = %q, want %q", p, got, p)
		}
	}
}

func TestParsePhaseUnknown(t *testing.T) {
	_, err := fsm.ParsePhase("not-a-phase")
	if err == nil {
		t.Fatal("ParsePhase(unknown): want error, got nil")
	}
}

func TestAdvanceTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    fsm.Phase
		event   fsm.Event
		want    fsm.Phase
		wantErr bool
	}{
		{"pending->planning on plan_start", fsm.PhasePending, fsm.EventPlanStart, fsm.PhasePlanning, false},
		{"planning->plan_gate on plan_done", fsm.PhasePlanning, fsm.EventPlanDone, fsm.PhasePlanGate, false},
		{"planning->failed on plan_failed", fsm.PhasePlanning, fsm.EventPlanFailed, fsm.PhaseFailed, false},
		{"kill from any non-terminal", fsm.PhasePlanning, fsm.EventKill, fsm.PhaseKilled, false},
		{"kill from pending", fsm.PhasePending, fsm.EventKill, fsm.PhaseKilled, false},
		{"invalid: done->planning", fsm.PhaseDone, fsm.EventPlanStart, "", true},
		{"invalid: planning->done", fsm.PhasePlanning, fsm.EventPlanDone, fsm.PhasePlanGate, false}, // sanity: done is two hops away
		{"invalid: pending->plan_gate", fsm.PhasePending, fsm.EventPlanDone, "", true},
		{"invalid: kill from done", fsm.PhaseDone, fsm.EventKill, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fsm.Advance(tt.from, tt.event)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Advance(%q, %q): want error, got %q", tt.from, tt.event, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Advance(%q, %q) error: %v", tt.from, tt.event, err)
			}
			if got != tt.want {
				t.Errorf("Advance(%q, %q) = %q, want %q", tt.from, tt.event, got, tt.want)
			}
		})
	}
}
