package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// planRequireMergeAuto holds the plan gate for approval but auto-resolves the
// merge gate, so a resolved plan gate lets the run flow into the worker phase.
const planRequireMergeAuto = `{"plan":{"mode":"require_approval"},"merge":{"mode":"auto"}}`

func TestTick_planGateApproved_advancesToWorking(t *testing.T) {
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v", err)
	}
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`, planRequireMergeAuto)
	script := workerScript(t, stateDir, "worker.jsonl",
		`{"kind":"done","summary":"ok"}`,
		`{"kind":"exit","code":0}`,
	)
	o := newWorkerOrch(t, st, stateDir, fakeClaude, script, 4)

	// Tick 1: gate is created pending; run holds.
	if err := o.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetRun(context.Background(), rid); got.Phase != "plan_gate" {
		t.Fatalf("after tick 1, phase = %q, want plan_gate", got.Phase)
	}

	// Human approves the pending gate.
	g, err := st.PendingGateByRun(context.Background(), rid)
	if err != nil {
		t.Fatalf("PendingGateByRun: %v", err)
	}
	if err := st.ResolveGate(context.Background(), g.ID, "approved", "cli"); err != nil {
		t.Fatal(err)
	}

	// Tick 2: approval observed; run advances through the worker phase. With no
	// MergerCmd it rests at merging.
	if err := o.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "merging" {
		t.Errorf("after approval, phase = %q, want merging", got.Phase)
	}
}

func TestTick_planGateRejected_failsRun(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rid := seedPlanGateRun(t, st, repo, `[{"id":"t1","title":"only"}]`, `{"plan":{"mode":"require_approval"}}`)
	o := orchestrator.New(orchestrator.Config{Store: st, StateDir: stateDir})

	if err := o.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	g, err := st.PendingGateByRun(context.Background(), rid)
	if err != nil {
		t.Fatalf("PendingGateByRun: %v", err)
	}
	if err := st.ResolveGate(context.Background(), g.ID, "rejected", "cli"); err != nil {
		t.Fatal(err)
	}
	if err := o.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "failed" {
		t.Errorf("after rejection, phase = %q, want failed", got.Phase)
	}
}
