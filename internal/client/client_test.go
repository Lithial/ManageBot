package client_test

import (
	"context"
	"testing"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestClientSubmitRun(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
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
	sock := testutil.StartInProcessServer(t)
	c := client.New(sock)
	if err := c.Healthz(context.Background()); err != nil {
		t.Errorf("Healthz: %v", err)
	}
}
