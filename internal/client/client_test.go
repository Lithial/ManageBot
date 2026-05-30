package client_test

import (
	"context"
	"errors"
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

func TestClientGetRun(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := client.New(sock)

	submit, err := c.SubmitRun(context.Background(), intake.SubmitRunRequest{
		ProjectName: "demo",
		RepoPath:    "/tmp/demo",
		IntakeKind:  "cli",
		SpecMD:      "# spec",
	})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}

	got, err := c.GetRun(context.Background(), submit.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.RunID != submit.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, submit.RunID)
	}
	if got.Phase != "pending" {
		t.Errorf("Phase = %q, want pending", got.Phase)
	}
	if got.PlanMD != "" {
		t.Errorf("PlanMD = %q, want empty for pending run", got.PlanMD)
	}
}

func TestClientGetRun_notFound(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := client.New(sock)
	_, err := c.GetRun(context.Background(), "01ABCNOTFOUND")
	if err == nil {
		t.Fatal("GetRun for unknown id: want error, got nil")
	}
	if !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want client.ErrNotFound", err)
	}
}
