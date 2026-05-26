// Package testutil provides helpers for integration tests that need to
// spawn the real wrapd binary against an ephemeral state directory and
// Unix socket.
package testutil

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type Daemon struct {
	SocketPath string
	StateDir   string
	cmd        *exec.Cmd
}

// StartTestDaemon spawns the wrapd binary in a temp state dir, waits for the
// socket to become available, and registers a cleanup that kills the process.
// `wrapdBinary` should be the absolute path to a built wrapd binary; tests
// typically pass the result of LocateBinary("wrapd").
func StartTestDaemon(t *testing.T, wrapdBinary string) *Daemon {
	t.Helper()

	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "wrap.sock")

	cmd := exec.Command(wrapdBinary, "--state-dir", stateDir, "--socket", sock)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapd: %v", err)
	}

	d := &Daemon{SocketPath: sock, StateDir: stateDir, cmd: cmd}
	t.Cleanup(func() { d.Stop() })

	if err := d.waitForSocket(2 * time.Second); err != nil {
		d.Stop()
		t.Fatalf("wait for socket: %v", err)
	}
	return d
}

func (d *Daemon) waitForSocket(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("unix", d.SocketPath, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return context.DeadlineExceeded
}

func (d *Daemon) Stop() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _, _ = d.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = d.cmd.Process.Kill()
		<-done
	}
}
