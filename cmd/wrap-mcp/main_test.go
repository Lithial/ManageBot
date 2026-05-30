package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// connectBridge wires a test MCP client to the bridge server (backed by a real
// in-process daemon) over an in-memory transport.
func connectBridge(t *testing.T, workerID, sock string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := newServer(client.New(sock), workerID)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mc := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := mc.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func seedWorker(t *testing.T, st *store.Store) (runID, workerID string) {
	t.Helper()
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/x", DefaultGatesJSON: "{}"})
	rid, _ := st.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}", Phase: "working"})
	_, _ = st.InsertPlan(ctx, store.Plan{RunID: rid, PlanMD: "# P", TasksJSON: `[{"id":"t1","title":"Build A","description":"the A thing"},{"id":"t2","title":"Build B"}]`})
	wid, _ := st.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "b", WorktreePath: "/wt"})
	return rid, wid
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestBridge_readTaskAndSiblings(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	_, wid := seedWorker(t, st)
	cs := connectBridge(t, wid, sock)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_task", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("read_task: %v", err)
	}
	if txt := resultText(res); !strings.Contains(txt, "Build A") || !strings.Contains(txt, "the A thing") {
		t.Errorf("read_task result = %q", txt)
	}

	sib, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_sibling_tasks", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("list_sibling_tasks: %v", err)
	}
	if txt := resultText(sib); !strings.Contains(txt, "Build B") {
		t.Errorf("siblings result = %q", txt)
	}
}

func TestBridge_reportDoneRelaysToDaemon(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	rid, wid := seedWorker(t, st)
	cs := connectBridge(t, wid, sock)

	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "report_done", Arguments: map[string]any{"summary": "finished the A thing"},
	}); err != nil {
		t.Fatalf("report_done: %v", err)
	}
	evs, _ := st.ListEventsByRun(context.Background(), rid)
	var found bool
	for _, e := range evs {
		if e.Kind == "worker_report_done" && strings.Contains(e.PayloadJSON, "finished the A thing") {
			found = true
		}
	}
	if !found {
		t.Errorf("report_done did not relay to the daemon; events=%+v", evs)
	}
}
