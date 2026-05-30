//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// startPlannerDaemon brings up wrapd with only a planner configured, so a
// submitted run parks at plan_gate (require_approval default).
func startPlannerDaemon(t *testing.T) (*testutil.Daemon, string) {
	t.Helper()
	wrapdBin, err := testutil.LocateBinary("wrapd")
	if err != nil {
		t.Fatalf("locate wrapd: %v", err)
	}
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Fatalf("locate fake-claude: %v", err)
	}
	repo := testutil.InitGitRepo(t)
	spec := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(spec, []byte("# spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "planner.jsonl")
	if err := os.WriteFile(script, []byte(strings.Join([]string{
		`{"kind":"plan","plan_md":"# Plan","tasks_json":"[{\"id\":\"t1\",\"title\":\"x\"}]"}`,
		`{"kind":"exit","code":0}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := testutil.StartTestDaemon(t, wrapdBin,
		"--planner-cmd", fakeClaude, "--planner-env", "FAKE_CLAUDE_SCRIPT="+script,
		"--tick-interval", "100ms",
	)
	wrapBin, err := testutil.LocateBinary("wrap")
	if err != nil {
		t.Fatalf("locate wrap: %v", err)
	}
	out, err := exec.Command(wrapBin, "run", "--socket", d.SocketPath, "--repo", repo, spec).CombinedOutput()
	if err != nil {
		t.Fatalf("wrap run: %v\n%s", err, out)
	}
	var submit intake.SubmitRunResponse
	if err := json.Unmarshal(out, &submit); err != nil {
		t.Fatalf("decode submit: %v", err)
	}
	return d, submit.RunID
}

func TestKillParkedRun(t *testing.T) {
	wrapBin, err := testutil.LocateBinary("wrap")
	if err != nil {
		t.Fatalf("locate wrap: %v", err)
	}
	d, runID := startPlannerDaemon(t)
	httpc := socketHTTPClient(d.SocketPath)

	waitForPhase(t, httpc, runID, "plan_gate")
	if out, err := exec.Command(wrapBin, "kill", "--socket", d.SocketPath, runID).CombinedOutput(); err != nil {
		t.Fatalf("wrap kill: %v\n%s", err, out)
	}
	waitForPhase(t, httpc, runID, "killed")
}

func TestConcurrentApprove_oneWins409(t *testing.T) {
	d, runID := startPlannerDaemon(t)
	httpc := socketHTTPClient(d.SocketPath)
	waitForPhase(t, httpc, runID, "plan_gate")

	// Fire two approvals concurrently; exactly one should win, the other 409.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := httpc.Post("http://wrap/runs/"+runID+"/approve", "application/json", strings.NewReader("{}"))
			if err != nil {
				codes[i] = -1
				return
			}
			codes[i] = resp.StatusCode
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Errorf("status codes = %v, want exactly one 200 and one 409", codes)
	}
}
