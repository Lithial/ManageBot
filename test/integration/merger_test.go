//go:build integration

package integration_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// TestMergerFailureFailsRun drives a run to the merging phase with a merger that
// exits non-zero (no report_done); the run must end in `failed`.
func TestMergerFailureFailsRun(t *testing.T) {
	wrapdBin, err := testutil.LocateBinary("wrapd")
	if err != nil {
		t.Fatalf("locate wrapd: %v", err)
	}
	wrapBin, err := testutil.LocateBinary("wrap")
	if err != nil {
		t.Fatalf("locate wrap: %v", err)
	}
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Fatalf("locate fake-claude: %v", err)
	}

	repo := testutil.InitGitRepo(t)
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	write := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	plannerScript := write("planner.jsonl",
		`{"kind":"plan","plan_md":"# Plan","tasks_json":"[{\"id\":\"t1\",\"title\":\"only\"}]"}`,
		`{"kind":"exit","code":0}`,
	)
	workerScript := write("worker.jsonl",
		`{"kind":"done","summary":"ok"}`,
		`{"kind":"exit","code":0}`,
	)
	mergerScript := write("merger.jsonl", `{"kind":"exit","code":4}`)

	d := testutil.StartTestDaemon(t, wrapdBin,
		"--planner-cmd", fakeClaude, "--planner-env", "FAKE_CLAUDE_SCRIPT="+plannerScript,
		"--worker-cmd", fakeClaude, "--worker-env", "FAKE_CLAUDE_SCRIPT="+workerScript,
		"--merger-cmd", fakeClaude, "--merger-env", "FAKE_CLAUDE_SCRIPT="+mergerScript,
		"--tick-interval", "100ms",
	)

	out, err := exec.Command(wrapBin, "run", "--socket", d.SocketPath, "--repo", repo, specPath).CombinedOutput()
	if err != nil {
		t.Fatalf("wrap run: %v\n%s", err, out)
	}
	var submit intake.SubmitRunResponse
	if err := json.Unmarshal(out, &submit); err != nil {
		t.Fatalf("decode submit: %v\n%s", err, out)
	}

	httpc := socketHTTPClient(d.SocketPath)
	// Approve the plan gate so the run proceeds into the worker/merge phases.
	waitForPhase(t, httpc, submit.RunID, "plan_gate")
	wrapResolve(t, wrapBin, d.SocketPath, "approve", submit.RunID)
	// The merger exits non-zero (merging → failed) before reaching the merge gate.
	waitForPhase(t, httpc, submit.RunID, "failed")
}

// TestPlanGateRejectFailsRun rejects the plan gate via the CLI; the run must end
// in failed without any worker ever running.
func TestPlanGateRejectFailsRun(t *testing.T) {
	wrapdBin, err := testutil.LocateBinary("wrapd")
	if err != nil {
		t.Fatalf("locate wrapd: %v", err)
	}
	wrapBin, err := testutil.LocateBinary("wrap")
	if err != nil {
		t.Fatalf("locate wrap: %v", err)
	}
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Fatalf("locate fake-claude: %v", err)
	}

	repo := testutil.InitGitRepo(t)
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plannerScript := filepath.Join(dir, "planner.jsonl")
	if err := os.WriteFile(plannerScript, []byte(strings.Join([]string{
		`{"kind":"plan","plan_md":"# Plan","tasks_json":"[{\"id\":\"t1\",\"title\":\"only\"}]"}`,
		`{"kind":"exit","code":0}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No worker/merger needed: the run is rejected at the plan gate.
	d := testutil.StartTestDaemon(t, wrapdBin,
		"--planner-cmd", fakeClaude, "--planner-env", "FAKE_CLAUDE_SCRIPT="+plannerScript,
		"--tick-interval", "100ms",
	)

	out, err := exec.Command(wrapBin, "run", "--socket", d.SocketPath, "--repo", repo, specPath).CombinedOutput()
	if err != nil {
		t.Fatalf("wrap run: %v\n%s", err, out)
	}
	var submit intake.SubmitRunResponse
	if err := json.Unmarshal(out, &submit); err != nil {
		t.Fatalf("decode submit: %v\n%s", err, out)
	}

	httpc := socketHTTPClient(d.SocketPath)
	waitForPhase(t, httpc, submit.RunID, "plan_gate")
	wrapResolve(t, wrapBin, d.SocketPath, "reject", submit.RunID)
	waitForPhase(t, httpc, submit.RunID, "failed")
}
