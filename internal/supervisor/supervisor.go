// Package supervisor owns the lifecycle of one worker subprocess.
// It writes a stdin payload (if any), collects RPC messages from stdout,
// captures stderr for forensics, and returns when the process exits or
// the context is cancelled.
package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"syscall"

	"github.com/Lithial/ManageBot/internal/workerrpc"
)

type Request struct {
	Cmd          *exec.Cmd
	StdinPayload []byte // optional; nil = no stdin
}

type Outcome struct {
	ExitCode       int
	Messages       []workerrpc.Message
	MalformedLines int    // lines that started with '{' but failed json.Unmarshal
	Stderr         []byte // captured tail (full stderr for Phase 2)
}

// Run spawns the configured Cmd, writes StdinPayload to its stdin, drains
// stdout as workerrpc NDJSON, captures stderr to a buffer, and waits for
// the process to exit. If the context is cancelled, the process is killed.
// Returns an Outcome describing exit code, parsed messages, malformed-line
// count (an indicator of worker protocol bugs), and stderr.
func Run(ctx context.Context, req Request) (Outcome, error) {
	cmd := req.Cmd
	if cmd == nil {
		return Outcome{}, fmt.Errorf("supervisor.Run: Cmd is required")
	}

	// Place the child in its own process group so that killing it also
	// kills any grandchildren (e.g. /bin/sh -c "sleep 30" spawns sleep as
	// a child; killing only the shell leaves sleep holding the pipes open).
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	// Wire pipes.
	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		return Outcome{}, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	var stdinW io.WriteCloser
	if req.StdinPayload != nil {
		stdinW, err = cmd.StdinPipe()
		if err != nil {
			return Outcome{}, fmt.Errorf("stdin pipe: %w", err)
		}
	}

	if err := cmd.Start(); err != nil {
		return Outcome{}, fmt.Errorf("start: %w", err)
	}

	// Feed stdin.
	if stdinW != nil {
		go func() {
			_, _ = stdinW.Write(req.StdinPayload)
			_ = stdinW.Close()
		}()
	}

	// Drain stdout in a goroutine so context cancellation can race with reads.
	type decodeResult struct {
		msgs      []workerrpc.Message
		malformed int
		err       error
	}
	decCh := make(chan decodeResult, 1)
	go func() {
		msgs, malformed, err := workerrpc.DecodeAll(stdoutR)
		decCh <- decodeResult{msgs: msgs, malformed: malformed, err: err}
	}()

	// Wait for either context cancellation or process exit.
	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-doneCh:
	case <-ctx.Done():
		// Kill the entire process group so grandchildren (e.g. shell-spawned
		// subprocesses) also die and release any pipe write ends.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr = <-doneCh
	}

	dec := <-decCh // tolerated; partial parse is fine

	exit := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}

	return Outcome{
		ExitCode:       exit,
		Messages:       dec.msgs,
		MalformedLines: dec.malformed,
		Stderr:         stderrBuf.Bytes(),
	}, nil
}
