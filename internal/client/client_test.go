package client_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestClientListRuns(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := client.New(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "a", GatesJSON: "{}", Phase: "done"})

	runs, err := c.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != rid || runs[0].Phase != "done" {
		t.Errorf("runs = %+v", runs)
	}
}

func TestClientApprove(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := client.New(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "plan_gate"})
	gid, _ := st.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", PayloadJSON: "{}"})

	resp, err := c.Approve(ctx, rid, "bob")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if resp.GateID != gid || resp.Status != "approved" {
		t.Errorf("resp = %+v", resp)
	}
	g, _ := st.LatestGateByKind(ctx, rid, "plan")
	if g.Status != "approved" || g.ResolvedBy != "bob" {
		t.Errorf("gate = %+v", g)
	}
}

func TestClientReject_noPendingGate(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := client.New(sock)
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "working"})

	if _, err := c.Reject(ctx, rid, ""); err == nil {
		t.Fatal("Reject with no pending gate: want error, got nil")
	}
}

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
