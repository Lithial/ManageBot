package client_test

import (
	"context"
	"net"
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
		t.Fatalf("store.Open: %v", err)
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
	t.Fatal("daemon socket never came up")
	return ""
}

func TestClientSubmitRun(t *testing.T) {
	sock := startTestDaemon(t)
	c := client.New(sock)

	resp, err := c.SubmitRun(context.Background(), intake.SubmitRunRequest{
		ProjectName: "demo",
		RepoPath:    "/tmp/demo",
		IntakeKind:  "cli",
		SpecMD:      "# spec",
	})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}
	if resp.RunID == "" {
		t.Error("RunID empty")
	}
	if resp.Phase != "pending" {
		t.Errorf("Phase = %q, want %q", resp.Phase, "pending")
	}
}

func TestClientHealthz(t *testing.T) {
	sock := startTestDaemon(t)
	c := client.New(sock)
	if err := c.Healthz(context.Background()); err != nil {
		t.Errorf("Healthz: %v", err)
	}
}
