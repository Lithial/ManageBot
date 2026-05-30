package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestGetRun_pendingHasNoPlan(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := newSocketClient(sock)

	body, _ := json.Marshal(intake.SubmitRunRequest{
		ProjectName: "p", RepoPath: "/tmp/x", IntakeKind: "cli", SpecMD: "spec",
	})
	resp, err := c.Post("http://wrap/runs", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	var submit intake.SubmitRunResponse
	_ = json.NewDecoder(resp.Body).Decode(&submit)
	resp.Body.Close()

	resp2, err := c.Get("http://wrap/runs/" + submit.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("status %d: %s", resp2.StatusCode, raw)
	}
	var got intake.GetRunResponse
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != submit.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, submit.RunID)
	}
	if got.Phase != "pending" {
		t.Errorf("Phase = %q, want pending", got.Phase)
	}
	if got.PlanMD != "" {
		t.Errorf("PlanMD should be empty for pending run, got %q", got.PlanMD)
	}
}

func TestGetRun_exposesMergeResult(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "done"})
	if _, err := st.InsertEvent(ctx, store.Event{
		RunID: rid, Kind: "merge_done",
		PayloadJSON: `{"branch":"wrap/` + rid + `/merge","summary":"all merged"}`,
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := c.Get("http://wrap/runs/" + rid)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got intake.GetRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Phase != "done" {
		t.Errorf("Phase = %q, want done", got.Phase)
	}
	if got.MergeSummary != "all merged" {
		t.Errorf("MergeSummary = %q, want %q", got.MergeSummary, "all merged")
	}
	if got.MergeBranch != "wrap/"+rid+"/merge" {
		t.Errorf("MergeBranch = %q, want %q", got.MergeBranch, "wrap/"+rid+"/merge")
	}
}

func TestGetRun_exposesPendingGate(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "plan_gate"})
	gid, _ := st.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", PayloadJSON: "{}"})

	resp, err := c.Get("http://wrap/runs/" + rid)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got intake.GetRunResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.PendingGateKind != "plan" {
		t.Errorf("PendingGateKind = %q, want plan", got.PendingGateKind)
	}
	if got.PendingGateID != gid {
		t.Errorf("PendingGateID = %q, want %q", got.PendingGateID, gid)
	}
}

func TestApproveGate(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "plan_gate"})
	gid, _ := st.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", PayloadJSON: "{}"})

	resp, err := c.Post("http://wrap/runs/"+rid+"/approve", "application/json", strings.NewReader(`{"by":"alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	g, err := st.LatestGateByKind(ctx, rid, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != gid || g.Status != "approved" || g.ResolvedBy != "alice" {
		t.Errorf("gate after approve = %+v", g)
	}
}

func TestRejectGate(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "merge_gate"})
	_, _ = st.InsertGate(ctx, store.Gate{RunID: rid, Kind: "merge", PayloadJSON: "{}"})

	resp, err := c.Post("http://wrap/runs/"+rid+"/reject", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	g, _ := st.LatestGateByKind(ctx, rid, "merge")
	if g.Status != "rejected" || g.ResolvedBy != "cli" {
		t.Errorf("gate after reject = %+v (want rejected, default resolver 'cli')", g)
	}
}

func TestApproveGate_noPendingGate(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "working"})

	resp, err := c.Post("http://wrap/runs/"+rid+"/approve", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 (no pending gate)", resp.StatusCode)
	}
}

func TestListRuns(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	r1, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "a", GatesJSON: "{}", Phase: "working"})
	r2, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "b", GatesJSON: "{}", Phase: "plan_gate"})
	_, _ = st.InsertGate(ctx, store.Gate{RunID: r2, Kind: "plan", PayloadJSON: "{}"})

	resp, err := c.Get("http://wrap/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var got intake.ListRunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 2 {
		t.Fatalf("len=%d, want 2: %+v", len(got.Runs), got.Runs)
	}
	// Newest first: r2 then r1.
	if got.Runs[0].RunID != r2 || got.Runs[1].RunID != r1 {
		t.Errorf("order = [%s %s], want [%s %s]", got.Runs[0].RunID, got.Runs[1].RunID, r2, r1)
	}
	if got.Runs[0].PendingGateKind != "plan" {
		t.Errorf("r2 PendingGateKind = %q, want plan", got.Runs[0].PendingGateKind)
	}
	if got.Runs[1].PendingGateKind != "" {
		t.Errorf("r1 PendingGateKind = %q, want empty", got.Runs[1].PendingGateKind)
	}
}

func TestGetRun_exposesIntake(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "specfile", IntakeRef: "/specs/a.md", SpecMD: "s", GatesJSON: "{}", Phase: "done"})

	resp, err := c.Get("http://wrap/runs/" + rid)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got intake.GetRunResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.IntakeKind != "specfile" {
		t.Errorf("IntakeKind = %q, want specfile", got.IntakeKind)
	}
	if got.IntakeRef != "/specs/a.md" {
		t.Errorf("IntakeRef = %q, want /specs/a.md", got.IntakeRef)
	}
}

func TestKill_parkedRunKilledAndGateRejected(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "plan_gate"})
	gid, _ := st.InsertGate(ctx, store.Gate{RunID: rid, Kind: "plan", PayloadJSON: "{}"})

	resp, err := c.Post("http://wrap/runs/"+rid+"/kill", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if run, _ := st.GetRun(ctx, rid); run.Phase != "killed" {
		t.Errorf("phase = %q, want killed", run.Phase)
	}
	g, _ := st.LatestGateByKind(ctx, rid, "plan")
	if g.ID != gid || g.Status != "rejected" || g.ResolvedBy != "killed_by_user" {
		t.Errorf("gate after kill = %+v, want rejected by killed_by_user", g)
	}
}

func TestKill_terminalRunConflicts(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "done"})

	resp, err := c.Post("http://wrap/runs/"+rid+"/kill", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 (run already terminal)", resp.StatusCode)
	}
}

func TestWorkerTaskAndSiblings(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()

	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "working"})
	_, _ = st.InsertPlan(ctx, store.Plan{RunID: rid, PlanMD: "# P", TasksJSON: `[{"id":"t1","title":"Build A","description":"do the A thing"},{"id":"t2","title":"Build B"}]`})
	wid, _ := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "b", WorktreePath: "/wt"})

	resp, err := c.Get("http://wrap/workers/" + wid + "/task")
	if err != nil {
		t.Fatal(err)
	}
	var task intake.WorkerTaskResponse
	_ = json.NewDecoder(resp.Body).Decode(&task)
	resp.Body.Close()
	if task.Title != "Build A" || task.Description != "do the A thing" {
		t.Errorf("task = %+v", task)
	}

	resp2, err := c.Get("http://wrap/workers/" + wid + "/siblings")
	if err != nil {
		t.Fatal(err)
	}
	var sib intake.SiblingTasksResponse
	_ = json.NewDecoder(resp2.Body).Decode(&sib)
	resp2.Body.Close()
	if len(sib.Titles) != 1 || sib.Titles[0] != "Build B" {
		t.Errorf("siblings = %+v, want [Build B]", sib.Titles)
	}
}

func TestWorkerReportDone(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "working"})
	wid, _ := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "b", WorktreePath: "/wt"})

	resp, err := c.Post("http://wrap/workers/"+wid+"/done", "application/json", strings.NewReader(`{"summary":"shipped it"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	evs, _ := st.ListEventsByRun(ctx, rid)
	var found bool
	for _, e := range evs {
		if e.Kind == "worker_report_done" && e.WorkerID == wid && strings.Contains(e.PayloadJSON, "shipped it") {
			found = true
		}
	}
	if !found {
		t.Errorf("no worker_report_done event with summary; events=%+v", evs)
	}
}

func TestWorkerReportPlan(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "planning"})
	wid, _ := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "__planner__", Branch: "b", WorktreePath: "/wt"})

	body := `{"plan_md":"# The Plan","tasks_json":"[{\"id\":\"t1\",\"title\":\"A\"}]"}`
	resp, err := c.Post("http://wrap/workers/"+wid+"/plan", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	ev, err := st.LatestWorkerEventByKind(ctx, wid, "worker_report_plan")
	if err != nil {
		t.Fatalf("no worker_report_plan event: %v", err)
	}
	if !strings.Contains(ev.PayloadJSON, "The Plan") || !strings.Contains(ev.PayloadJSON, "\\\"id\\\":\\\"t1\\\"") {
		t.Errorf("plan event payload = %q", ev.PayloadJSON)
	}
}

func TestWorkerTask_notFound(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := newSocketClient(sock)
	resp, err := c.Get("http://wrap/workers/nope/task")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetRun_notFound(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := newSocketClient(sock)
	resp, err := c.Get("http://wrap/runs/01ABCNOTFOUND")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	var errResp intake.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errResp.Error == "" {
		t.Error("error body missing 'error' field")
	}
}

func newSocketClient(sock string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
}

// seedPendingGate inserts a project, a run in plan_gate, and a pending gate of
// the given kind, returning the run id.
func seedPendingGate(t *testing.T, st *store.Store, kind string) string {
	t.Helper()
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p-" + kind, RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "plan_gate"})
	if _, err := st.InsertGate(ctx, store.Gate{RunID: rid, Kind: kind, PayloadJSON: "{}"}); err != nil {
		t.Fatalf("insert gate: %v", err)
	}
	return rid
}

// TestResolveGate_rejectsInvalidAction: an action not valid for the gate kind is
// a 400, before any resolution happens.
func TestResolveGate_rejectsInvalidAction(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	rid := seedPendingGate(t, st, "merge") // merge gates take proceed|abort, not drop_branch

	resp, err := c.Post("http://wrap/runs/"+rid+"/approve", "application/json",
		strings.NewReader(`{"by":"alice","action":"drop_branch"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	// The gate must still be pending (no resolution on a rejected action).
	g, err := st.PendingGateByRun(context.Background(), rid)
	if err != nil {
		t.Fatalf("pending gate should still exist: %v", err)
	}
	if g.Status != "pending" {
		t.Errorf("gate status = %q, want pending", g.Status)
	}
}

// TestResolveDecision_persistsAction: POST /resolve carries decision+action and
// persists both.
func TestResolveDecision_persistsAction(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	c := newSocketClient(sock)
	rid := seedPendingGate(t, st, "merge_conflict")

	resp, err := c.Post("http://wrap/runs/"+rid+"/resolve", "application/json",
		strings.NewReader(`{"by":"alice","decision":"approve","action":"drop_branch"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (%s)", resp.StatusCode, raw)
	}
	g, err := st.LatestGateByKind(context.Background(), rid, "merge_conflict")
	if err != nil {
		t.Fatalf("latest gate: %v", err)
	}
	if g.Status != "approved" || g.Action != "drop_branch" {
		t.Errorf("gate = {status:%q action:%q}, want {approved drop_branch}", g.Status, g.Action)
	}
}
