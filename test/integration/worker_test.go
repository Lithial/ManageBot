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

// TestWorkerPhaseHappyPath exercises the full Phase 3 path end-to-end through
// the real wrapd binary: pending → planning → plan_gate → working → merging.
// The planner and workers are both fake-claude, each driven by its own script
// via a distinct FAKE_CLAUDE_SCRIPT in --planner-env / --worker-env.
func TestWorkerPhaseHappyPath(t *testing.T) {
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

	d := testutil.StartTestDaemon(t, wrapdBin,
		"--planner-cmd", fakeClaude,
		"--planner-env", "FAKE_CLAUDE_SCRIPT="+plannerScript,
		"--worker-cmd", fakeClaude,
		"--worker-env", "FAKE_CLAUDE_SCRIPT="+workerScript,
		"--auto-advance-gates",
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
	deadline := time.Now().Add(20 * time.Second)
	var got intake.GetRunResponse
	for time.Now().Before(deadline) {
		resp, err := httpc.Get("http://wrap/runs/" + submit.RunID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("GET /runs/%s: status %d, body: %s", submit.RunID, resp.StatusCode, body)
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			resp.Body.Close()
			t.Fatalf("decode get-run response: %v", err)
		}
		resp.Body.Close()
		if got.Phase == "merging" {
			break
		}
		if got.Phase == "failed" {
			t.Fatalf("run failed unexpectedly: %+v", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got.Phase != "merging" {
		t.Fatalf("phase = %q after wait, want merging (response: %+v)", got.Phase, got)
	}
}
