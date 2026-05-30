package client_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func seedWorker(t *testing.T, st *store.Store) (runID, workerID string) {
	t.Helper()
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "working"})
	_, _ = st.InsertPlan(ctx, store.Plan{RunID: rid, PlanMD: "# P", TasksJSON: `[{"id":"t1","title":"Do A","description":"the A"}]`})
	wid, _ := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "b", WorktreePath: "/wt"})
	return rid, wid
}

func TestClientWorkerTask(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := client.New(sock)
	_, wid := seedWorker(t, st)

	task, err := c.WorkerTask(context.Background(), wid)
	if err != nil {
		t.Fatalf("WorkerTask: %v", err)
	}
	if task.Title != "Do A" || task.Description != "the A" {
		t.Errorf("task = %+v", task)
	}
}

func TestClientWorkerReportDone(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := client.New(sock)
	rid, wid := seedWorker(t, st)

	if err := c.WorkerReportDone(context.Background(), wid, "all done"); err != nil {
		t.Fatalf("WorkerReportDone: %v", err)
	}
	evs, _ := st.ListEventsByRun(context.Background(), rid)
	var found bool
	for _, e := range evs {
		if e.Kind == "worker_report_done" && strings.Contains(e.PayloadJSON, "all done") {
			found = true
		}
	}
	if !found {
		t.Errorf("no worker_report_done event recorded")
	}
}

func TestClientWorkerReport_unknownWorker(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := client.New(sock)
	if err := c.WorkerReportProgress(context.Background(), "nope", "hi"); err == nil {
		t.Fatal("want error for unknown worker")
	}
}
