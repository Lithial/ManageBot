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
	"time"

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
		"--auto-advance-gates", "--tick-interval", "100ms",
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
	deadline := time.Now().Add(20 * time.Second)
	var got intake.GetRunResponse
	for time.Now().Before(deadline) {
		resp, err := httpc.Get("http://wrap/runs/" + submit.RunID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("GET status %d", resp.StatusCode)
		}
		_ = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if got.Phase == "failed" {
			break
		}
		if got.Phase == "done" {
			t.Fatalf("run reached done despite merger failure: %+v", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got.Phase != "failed" {
		t.Fatalf("phase = %q after wait, want failed", got.Phase)
	}
}
