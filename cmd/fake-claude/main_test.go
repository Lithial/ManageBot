package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/testutil"
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

	// Parse two NDJSON lines and assert their methods.
	var msgs []struct{ Method string }
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var m struct{ Method string }
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse line %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Method != "report_progress" || msgs[1].Method != "report_plan" {
		t.Errorf("methods: %+v", msgs)
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
