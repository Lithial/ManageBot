package testutil

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/api"
	"github.com/Lithial/ManageBot/internal/store"
)

// StartInProcessServer spins up a real api.Server backed by a real
// store.Store inside the test process, on a temp Unix socket.
// It blocks until either the server's Ready signal fires or up to 1s
// of socket polling has elapsed (whichever comes first). Returns the
// socket path. Cleanup (Close + store close) is registered via t.Cleanup.
//
// This is the in-process counterpart to StartTestDaemon, which spawns
// the wrapd binary as an external subprocess.
func StartInProcessServer(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "wrap.sock")
	dbPath := filepath.Join(t.TempDir(), "wrap.db")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := api.NewServer(s, sock)
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })

	// Wait for ready (with a 1s safety timeout).
	select {
	case <-srv.Ready():
	case <-time.After(time.Second):
	}
	// Belt-and-braces: also confirm we can actually dial.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("in-process server never came up")
	return ""
}
