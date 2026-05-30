// wrap-mcp is the MCP server `claude` spawns as a worker's `wrap` tool provider.
// It exposes the five wrap.* tools over stdio (MCP) and relays each call to the
// daemon over the Unix socket, scoped to a single worker via --worker. The
// daemon gains no new network surface; per-worker scoping is just this arg.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
)

// workerAPI is the worker-scoped daemon surface the bridge relays to.
// *client.Client satisfies it.
type workerAPI interface {
	WorkerTask(ctx context.Context, workerID string) (intake.WorkerTaskResponse, error)
	WorkerSiblings(ctx context.Context, workerID string) (intake.SiblingTasksResponse, error)
	WorkerReportProgress(ctx context.Context, workerID, msg string) error
	WorkerReportDone(ctx context.Context, workerID, summary string) error
	WorkerReportBlocked(ctx context.Context, workerID, reason string) error
}

type emptyIn struct{}
type noOut struct{}
type progressIn struct {
	Msg string `json:"msg" jsonschema:"the progress message to show"`
}
type doneIn struct {
	Summary string `json:"summary" jsonschema:"a summary of the completed work"`
}
type blockedIn struct {
	Reason string `json:"reason" jsonschema:"why the worker is stuck and needs human help"`
}

// newServer builds the MCP server exposing the wrap.* tools for one worker.
func newServer(api workerAPI, workerID string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "wrap", Version: "0.1.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "report_progress", Description: "Report a free-text status line for this task."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in progressIn) (*mcp.CallToolResult, noOut, error) {
			return nil, noOut{}, api.WorkerReportProgress(ctx, workerID, in.Msg)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "report_done", Description: "Declare this task complete, with a summary."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in doneIn) (*mcp.CallToolResult, noOut, error) {
			return nil, noOut{}, api.WorkerReportDone(ctx, workerID, in.Summary)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "report_blocked", Description: "Signal that you are blocked and need human help."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in blockedIn) (*mcp.CallToolResult, noOut, error) {
			return nil, noOut{}, api.WorkerReportBlocked(ctx, workerID, in.Reason)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "read_task", Description: "Return this worker's task (title and description)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, intake.WorkerTaskResponse, error) {
			task, err := api.WorkerTask(ctx, workerID)
			return nil, task, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_sibling_tasks", Description: "Return the titles of sibling workers' tasks."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, intake.SiblingTasksResponse, error) {
			sib, err := api.WorkerSiblings(ctx, workerID)
			return nil, sib, err
		})
	return s
}

func defaultSocketPath() string {
	if v := os.Getenv("WRAP_SOCKET"); v != "" {
		return v
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "wrap.sock")
	}
	return filepath.Join(os.TempDir(), "wrap.sock")
}

func main() {
	socket := flag.String("socket", defaultSocketPath(), "wrapd Unix socket path")
	worker := flag.String("worker", "", "worker id this MCP session is scoped to")
	flag.Parse()
	if *worker == "" {
		fmt.Fprintln(os.Stderr, "wrap-mcp: --worker is required")
		os.Exit(2)
	}
	srv := newServer(client.New(*socket), *worker)
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "wrap-mcp: %v\n", err)
		os.Exit(1)
	}
}
