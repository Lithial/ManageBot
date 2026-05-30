//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// TestPruneRejectsNonTerminalRun: a run paused at the plan gate (non-terminal)
// cannot be pruned — POST /runs/{id}/prune returns 409.
func TestPruneRejectsNonTerminalRun(t *testing.T) {
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

	resp, err := httpc.Post("http://wrap/runs/"+submit.RunID+"/prune", "application/json", nil)
	if err != nil {
		t.Fatalf("post prune: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("prune of plan_gate run: status %d, want 409", resp.StatusCode)
	}
}

// TestPruneTerminalRun drives a run to `failed` (the merger exits non-zero),
// then `wrap prune` removes its retained worktrees + branches.
func TestPruneTerminalRun(t *testing.T) {
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
	waitForPhase(t, httpc, submit.RunID, "plan_gate")
	wrapResolve(t, wrapBin, d.SocketPath, "approve", submit.RunID)
	waitForPhase(t, httpc, submit.RunID, "failed")

	// Sanity: the run left wrap/<run>/* branches behind before prune.
	branches := func() string {
		b, err := exec.Command("git", "-C", repo, "branch", "--list", "wrap/"+submit.RunID+"/*").CombinedOutput()
		if err != nil {
			t.Fatalf("git branch --list: %v\n%s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if branches() == "" {
		t.Fatal("expected wrap/<run>/* branches to exist before prune")
	}

	pruneOut, err := exec.Command(wrapBin, "prune", "--socket", d.SocketPath, submit.RunID).CombinedOutput()
	if err != nil {
		t.Fatalf("wrap prune: %v\n%s", err, pruneOut)
	}
	var pruneResp intake.PruneRunResponse
	if err := json.Unmarshal(pruneOut, &pruneResp); err != nil {
		t.Fatalf("decode prune resp: %v\n%s", err, pruneOut)
	}
	if pruneResp.BranchesDeleted == 0 {
		t.Errorf("BranchesDeleted = 0, want > 0 (resp: %+v)", pruneResp)
	}
	if remaining := branches(); remaining != "" {
		t.Errorf("wrap/<run>/* branches remain after prune: %q", remaining)
	}
}
