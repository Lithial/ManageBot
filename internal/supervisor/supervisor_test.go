package supervisor_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/supervisor"
)

func TestRun_capturesRPCMessages(t *testing.T) {
	// Use a shell script that emits two RPC lines then exits 0.
	script := `printf '%s\n%s\n' '{"method":"report_progress","params":{"msg":"hi"}}' '{"method":"report_plan","params":{"plan_md":"# P","tasks_json":"[]"}}'`
	cmd := exec.Command("/bin/sh", "-c", script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := supervisor.Run(ctx, supervisor.Request{Cmd: cmd})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Method != "report_progress" {
		t.Errorf("Messages[0].Method = %q", out.Messages[0].Method)
	}
	if out.Messages[1].Method != "report_plan" {
		t.Errorf("Messages[1].Method = %q", out.Messages[1].Method)
	}
	if out.MalformedLines != 0 {
		t.Errorf("MalformedLines = %d, want 0", out.MalformedLines)
	}
}

func TestRun_capturesStdinAndStderr(t *testing.T) {
	// Reads stdin, echoes to stderr, exits 7.
	script := `cat 1>&2; exit 7`
	cmd := exec.Command("/bin/sh", "-c", script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := supervisor.Run(ctx, supervisor.Request{
		Cmd:          cmd,
		StdinPayload: []byte("hello from spec\n"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", out.ExitCode)
	}
	if !strings.Contains(string(out.Stderr), "hello from spec") {
		t.Errorf("Stderr = %q, want to contain stdin payload", out.Stderr)
	}
}

func TestRun_contextCancelKillsProcess(t *testing.T) {
	// Long-sleeping subprocess; cancel context, expect non-zero exit quickly.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	out, err := supervisor.Run(ctx, supervisor.Request{Cmd: cmd})
	elapsed := time.Since(start)
	if err == nil && out.ExitCode == 0 {
		t.Fatal("expected non-zero exit when context cancelled")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Run took %v, expected sub-second after cancel", elapsed)
	}
}

func TestRun_capturesMalformedLineCount(t *testing.T) {
	// Mix one valid JSON line, one truncated JSON line, and one plain stdout line.
	script := `printf '%s\n%s\n%s\n' '{"method":"report_progress","params":{"msg":"ok"}}' '{truncated' 'plain text'`
	cmd := exec.Command("/bin/sh", "-c", script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := supervisor.Run(ctx, supervisor.Request{Cmd: cmd})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Method != "report_progress" {
		t.Errorf("Messages = %+v, want one report_progress", out.Messages)
	}
	if out.MalformedLines != 1 {
		t.Errorf("MalformedLines = %d, want 1", out.MalformedLines)
	}
}
