//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net"
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

func TestPlannerHappyPath(t *testing.T) {
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

	// Build a temp repo and planner script.
	repo := testutil.InitGitRepo(t)
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# spec\nbuild a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(t.TempDir(), "planner.jsonl")
	lines := []string{
		`{"kind":"progress","msg":"planning"}`,
		`{"kind":"plan","plan_md":"# Plan\nsteps","tasks_json":"[{\"id\":\"t1\",\"title\":\"do it\"}]"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(scriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Start wrapd with planner-cmd=fake-claude and FAKE_CLAUDE_SCRIPT in its planner env.
	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "wrap.sock")
	cmd := exec.Command(wrapdBin,
		"--state-dir", stateDir,
		"--socket", sock,
		"--planner-cmd", fakeClaude,
		"--planner-env", "FAKE_CLAUDE_SCRIPT="+scriptPath,
		"--tick-interval", "100ms",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapd: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	// Wait for socket.
	waitForSocket(t, sock, 3*time.Second)

	// Submit run via the wrap CLI.
	out, err := exec.Command(wrapBin, "run", "--socket", sock, "--repo", repo, specPath).CombinedOutput()
	if err != nil {
		t.Fatalf("wrap run: %v\noutput: %s", err, out)
	}
	var submit intake.SubmitRunResponse
	if err := json.Unmarshal(out, &submit); err != nil {
		t.Fatalf("decode submit response: %v\noutput: %s", err, out)
	}

	// Poll GET /runs/{id} until phase == plan_gate or timeout.
	httpc := socketHTTPClient(sock)
	deadline := time.Now().Add(15 * time.Second)
	var got intake.GetRunResponse
	for time.Now().Before(deadline) {
		resp, err := httpc.Get("http://wrap/runs/" + submit.RunID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		_ = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if got.Phase == "plan_gate" {
			break
		}
		if got.Phase == "failed" {
			t.Fatalf("run failed unexpectedly: %+v", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got.Phase != "plan_gate" {
		t.Fatalf("phase = %q after wait, want plan_gate (response: %+v)", got.Phase, got)
	}
	if !strings.Contains(got.PlanMD, "# Plan") {
		t.Errorf("PlanMD = %q, want to contain '# Plan'", got.PlanMD)
	}
	if !strings.Contains(got.TasksJSON, `"id":"t1"`) {
		t.Errorf("TasksJSON = %q, want to contain task t1", got.TasksJSON)
	}
}

func waitForSocket(t *testing.T, sock string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("unix", sock, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("socket %s never came up", sock)
}

func socketHTTPClient(sock string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
}
