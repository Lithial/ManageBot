package intake_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/api"
	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

func startTestDaemon(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "wrap.sock")
	dbPath := filepath.Join(t.TempDir(), "wrap.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := api.NewServer(s, sock)
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket never came up")
	return ""
}

func TestCLIAdapterSubmitsSpecFile(t *testing.T) {
	sock := startTestDaemon(t)
	c := client.New(sock)

	// Create a repo dir with a spec file inside.
	repo := t.TempDir()
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# my spec\n\nDo a thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := intake.NewCLIAdapter(c)
	resp, err := adapter.SubmitFromSpec(context.Background(), specPath, repo)
	if err != nil {
		t.Fatalf("SubmitFromSpec: %v", err)
	}
	if resp.RunID == "" {
		t.Error("RunID empty")
	}
}
