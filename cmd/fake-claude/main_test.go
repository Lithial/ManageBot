package main_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/testutil"
	"github.com/Lithial/ManageBot/internal/workerrpc"
)

func TestFakeClaude_scriptEmitsRPC(t *testing.T) {
	bin, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude binary not built: %v (run `make fake-claude`)", err)
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.jsonl")
	lines := []string{
		`{"kind":"progress","msg":"starting"}`,
		`{"kind":"plan","plan_md":"# Plan","tasks_json":"[]"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(scriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	msgs, malformed, err := workerrpc.DecodeAll(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if malformed != 0 {
		t.Errorf("malformed = %d, want 0", malformed)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}

	prog, err := workerrpc.AsProgress(msgs[0])
	if err != nil {
		t.Fatalf("AsProgress: %v", err)
	}
	if prog.Msg != "starting" {
		t.Errorf("prog.Msg = %q, want %q", prog.Msg, "starting")
	}

	plan, err := workerrpc.AsPlan(msgs[1])
	if err != nil {
		t.Fatalf("AsPlan: %v", err)
	}
	if plan.PlanMD != "# Plan" {
		t.Errorf("plan.PlanMD = %q, want %q", plan.PlanMD, "# Plan")
	}
	if plan.TasksJSON != "[]" {
		t.Errorf("plan.TasksJSON = %q, want %q", plan.TasksJSON, "[]")
	}
}

func TestFakeClaude_scriptCustomExit(t *testing.T) {
	bin, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude binary not built: %v", err)
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.jsonl")
	if err := os.WriteFile(scriptPath, []byte(`{"kind":"exit","code":3}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
	err = cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 3 {
		t.Fatalf("expected exit 3, got err=%v", err)
	}
}

func TestFakeClaude_scriptStderrAction(t *testing.T) {
	bin, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude binary not built: %v", err)
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.jsonl")
	lines := []string{
		`{"kind":"stderr","text":"warning: thing\n"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(scriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stderr.String(), "warning: thing") {
		t.Errorf("stderr = %q, want to contain 'warning: thing'", stderr.String())
	}
}

func TestFakeClaude_scriptSleep(t *testing.T) {
	bin, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude binary not built: %v", err)
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.jsonl")
	lines := []string{
		`{"kind":"sleep_ms","ms":80}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(scriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
	start := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 80*time.Millisecond {
		t.Errorf("elapsed = %v, want at least 80ms (sleep was skipped)", elapsed)
	}
	if elapsed > 1*time.Second {
		t.Errorf("elapsed = %v, want < 1s (sleep stuck)", elapsed)
	}
}
