//go:build integration && e2e

// Package-level e2e smoke: one real `claude` run through the whole pipeline
// (planner → worker → merger over real MCP via the wrap-mcp bridge). Opt-in:
// build-tagged `e2e` and skipped unless `claude` is on PATH. It costs real API
// usage, so it is never part of `make test`. Run with `make test-e2e`.
package integration_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// this file is <root>/test/integration/e2e_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func TestE2ERealClaude(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH; skipping real-claude e2e smoke")
	}
	wrapdBin, err := testutil.LocateBinary("wrapd")
	if err != nil {
		t.Fatalf("locate wrapd: %v (run `make build`)", err)
	}
	wrapMcpBin, err := testutil.LocateBinary("wrap-mcp")
	if err != nil {
		t.Fatalf("locate wrap-mcp: %v (run `make build`)", err)
	}
	promptDir := filepath.Join(repoRootDir(t), "prompts")

	// A trivial, verifiable task in a fresh repo.
	repo := testutil.InitGitRepo(t)
	const want = "hello from wrap"
	spec := "Create a file named GREETING.txt at the repository root whose entire contents are exactly:\n" + want + "\nCommit it.\n"
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real claude for all three roles, via the wrap-mcp bridge + role prompts.
	d := testutil.StartTestDaemon(t, wrapdBin,
		"--planner-cmd", "claude", "--worker-cmd", "claude", "--merger-cmd", "claude",
		"--wrap-mcp-cmd", wrapMcpBin, "--prompt-dir", promptDir,
		"--max-workers", "2", "--tick-interval", "500ms", "--step-timeout", "8m",
	)

	// Submit directly with auto gates so the smoke runs unattended.
	httpc := socketHTTPClient(d.SocketPath)
	body, _ := json.Marshal(intake.SubmitRunRequest{
		ProjectName: "e2e", RepoPath: repo, IntakeKind: "cli", SpecMD: spec,
		GatesJSON: `{"plan":{"mode":"auto"},"worker_done":{"mode":"auto"},"merge":{"mode":"auto"}}`,
	})
	resp, err := httpc.Post("http://wrap/runs", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var submit intake.SubmitRunResponse
	_ = json.NewDecoder(resp.Body).Decode(&submit)
	resp.Body.Close()
	if submit.RunID == "" {
		t.Fatal("empty run id")
	}

	// Real claude is slow; poll generously.
	deadline := time.Now().Add(15 * time.Minute)
	var got intake.GetRunResponse
	for time.Now().Before(deadline) {
		r, err := httpc.Get("http://wrap/runs/" + submit.RunID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if r.StatusCode != http.StatusOK {
			r.Body.Close()
			t.Fatalf("get run status %d", r.StatusCode)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		r.Body.Close()
		if got.Phase == "done" {
			break
		}
		if got.Phase == "failed" || got.Phase == "killed" {
			t.Fatalf("run ended in %q (response: %+v)", got.Phase, got)
		}
		time.Sleep(2 * time.Second)
	}
	if got.Phase != "done" {
		t.Fatalf("phase = %q after wait, want done", got.Phase)
	}

	// The merged branch should contain the file a worker created+committed and the
	// merger merged in. We assert the artifact exists and is non-empty — NOT its
	// exact text: planner plan-quality and worker/merger content fidelity are
	// deliberately untested (they are real-LLM behavior, not wrap behavior).
	mergeBranch := "wrap/" + submit.RunID + "/merge"
	out, err := exec.Command("git", "-C", repo, "show", mergeBranch+":GREETING.txt").Output()
	if err != nil {
		t.Fatalf("merged branch %s missing GREETING.txt (the worker's committed artifact): %v", mergeBranch, err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		t.Errorf("GREETING.txt is empty on %s", mergeBranch)
	}
	t.Logf("e2e ok: run %s reached done; %s:GREETING.txt = %q (wanted ~%q)", submit.RunID, mergeBranch, out, want)
}
