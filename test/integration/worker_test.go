//go:build integration

package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// TestGateApprovalFlow exercises the full path end-to-end through the real wrapd
// binary with the default require_approval gates: pending → planning → plan_gate
// → (approve) → working → merging → merge_gate → (approve) → done. Planner,
// workers, and merger are all fake-claude; the two gates are resolved via the
// `wrap approve` CLI.
func TestGateApprovalFlow(t *testing.T) {
	wrapdBin, err := testutil.LocateBinary("wrapd")
	if err != nil {
		t.Fatalf("locate wrapd: %v (did you run `make wrapd`?)", err)
	}
	wrapBin, err := testutil.LocateBinary("wrap")
	if err != nil {
		t.Fatalf("locate wrap: %v (did you run `make wrap`?)", err)
	}
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Fatalf("locate fake-claude: %v (did you run `make fake-claude`?)", err)
	}

	repo := testutil.InitGitRepo(t)
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# spec\nbuild a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptDir := t.TempDir()
	plannerScript := filepath.Join(scriptDir, "planner.jsonl")
	plannerLines := []string{
		`{"kind":"progress","msg":"planning"}`,
		`{"kind":"plan","plan_md":"# Plan\nsteps","tasks_json":"[{\"id\":\"t1\",\"title\":\"first\"},{\"id\":\"t2\",\"title\":\"second\",\"depends_on\":[\"t1\"]}]"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(plannerScript, []byte(strings.Join(plannerLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerScript := filepath.Join(scriptDir, "worker.jsonl")
	workerLines := []string{
		`{"kind":"progress","msg":"working"}`,
		`{"kind":"done","summary":"did the task"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(workerScript, []byte(strings.Join(workerLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mergerScript := filepath.Join(scriptDir, "merger.jsonl")
	mergerLines := []string{
		`{"kind":"progress","msg":"merging"}`,
		`{"kind":"done","summary":"merged all worker branches"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(mergerScript, []byte(strings.Join(mergerLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := testutil.StartTestDaemon(t, wrapdBin,
		"--planner-cmd", fakeClaude,
		"--planner-env", "FAKE_CLAUDE_SCRIPT="+plannerScript,
		"--worker-cmd", fakeClaude,
		"--worker-env", "FAKE_CLAUDE_SCRIPT="+workerScript,
		"--merger-cmd", fakeClaude,
		"--merger-env", "FAKE_CLAUDE_SCRIPT="+mergerScript,
		"--tick-interval", "100ms",
	)

	out, err := exec.Command(wrapBin, "run", "--socket", d.SocketPath, "--repo", repo, specPath).CombinedOutput()
	if err != nil {
		t.Fatalf("wrap run: %v\noutput: %s", err, out)
	}
	var submit intake.SubmitRunResponse
	if err := json.Unmarshal(out, &submit); err != nil {
		t.Fatalf("decode submit response: %v\noutput: %s", err, out)
	}

	httpc := socketHTTPClient(d.SocketPath)

	// Default project gates are require_approval: the run holds at the plan gate.
	got := waitForPhase(t, httpc, submit.RunID, "plan_gate")
	if got.PendingGateKind != "plan" {
		t.Errorf("PendingGateKind = %q, want plan", got.PendingGateKind)
	}
	wrapResolve(t, wrapBin, d.SocketPath, "approve", submit.RunID)

	// Approval lets it run through to the merge gate, where it holds again.
	got = waitForPhase(t, httpc, submit.RunID, "merge_gate")
	if got.PendingGateKind != "merge" {
		t.Errorf("PendingGateKind = %q, want merge", got.PendingGateKind)
	}
	wrapResolve(t, wrapBin, d.SocketPath, "approve", submit.RunID)

	got = waitForPhase(t, httpc, submit.RunID, "done")
	if !strings.Contains(got.MergeSummary, "merged all worker branches") {
		t.Errorf("MergeSummary = %q, want the merger's summary", got.MergeSummary)
	}
	if !strings.Contains(got.MergeBranch, "/merge") {
		t.Errorf("MergeBranch = %q, want a wrap/<run>/merge branch", got.MergeBranch)
	}
}

// waitForPhase polls GET /runs/{id} until the run reaches `want`, failing the
// test if it reaches an unwanted terminal phase or times out.
func waitForPhase(t *testing.T, httpc *http.Client, runID, want string) intake.GetRunResponse {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var got intake.GetRunResponse
	for time.Now().Before(deadline) {
		resp, err := httpc.Get("http://wrap/runs/" + runID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("GET /runs/%s: status %d, body: %s", runID, resp.StatusCode, body)
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			resp.Body.Close()
			t.Fatalf("decode get-run response: %v", err)
		}
		resp.Body.Close()
		if got.Phase == want {
			return got
		}
		if (got.Phase == "failed" || got.Phase == "done") && got.Phase != want {
			t.Fatalf("run reached %q while waiting for %q: %+v", got.Phase, want, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("phase = %q after wait, want %q (response: %+v)", got.Phase, want, got)
	return got
}

// wrapResolve runs `wrap approve|reject <run-id>` against the daemon socket.
func wrapResolve(t *testing.T, wrapBin, socket, action, runID string) {
	t.Helper()
	out, err := exec.Command(wrapBin, action, "--socket", socket, runID).CombinedOutput()
	if err != nil {
		t.Fatalf("wrap %s: %v\noutput: %s", action, err, out)
	}
}
