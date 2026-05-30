package gates_test

import (
	"testing"

	"github.com/Lithial/ManageBot/internal/gates"
)

func TestParseAndMode(t *testing.T) {
	p, err := gates.Parse(`{"plan":{"mode":"require_approval"},"worker_done":{"mode":"auto"},"merge":{"mode":"auto"},"custom":[]}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := p.Mode("plan"); got != gates.ModeRequireApproval {
		t.Errorf("Mode(plan) = %q, want require_approval", got)
	}
	if got := p.Mode("merge"); got != gates.ModeAuto {
		t.Errorf("Mode(merge) = %q, want auto", got)
	}
	if got := p.Mode("worker_done"); got != gates.ModeAuto {
		t.Errorf("Mode(worker_done) = %q, want auto", got)
	}
}

func TestMode_defaultsToRequireApproval(t *testing.T) {
	// An empty policy (and any unspecified kind) defaults to require_approval —
	// never auto-approve the unspecified.
	p, err := gates.Parse(`{}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := p.Mode("plan"); got != gates.ModeRequireApproval {
		t.Errorf("Mode(plan) on empty policy = %q, want require_approval", got)
	}
	if got := p.Mode("anything"); got != gates.ModeRequireApproval {
		t.Errorf("Mode(anything) = %q, want require_approval", got)
	}
}

func TestParse_badJSON(t *testing.T) {
	if _, err := gates.Parse(`not json`); err == nil {
		t.Fatal("Parse(bad): want error, got nil")
	}
}

func TestParse_unknownModeRejected(t *testing.T) {
	if _, err := gates.Parse(`{"plan":{"mode":"sometimes"}}`); err == nil {
		t.Fatal("Parse(unknown mode): want error, got nil")
	}
}

func TestParse_emptyStringIsEmptyPolicy(t *testing.T) {
	// An empty gates_json string is treated as an empty policy (all require_approval),
	// not an error — robustness against partially-populated rows.
	p, err := gates.Parse("")
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if got := p.Mode("plan"); got != gates.ModeRequireApproval {
		t.Errorf("Mode(plan) = %q, want require_approval", got)
	}
}
