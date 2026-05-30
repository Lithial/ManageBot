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
