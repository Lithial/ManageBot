package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Lithial/ManageBot/internal/intake"
)

// Worker-scoped client methods, used by the wrap-mcp bridge to relay a worker's
// MCP tool calls to the daemon.

// WorkerTask returns the worker's task (wrap.read_task).
func (c *Client) WorkerTask(ctx context.Context, workerID string) (intake.WorkerTaskResponse, error) {
	var out intake.WorkerTaskResponse
	return out, c.workerGet(ctx, workerID, "task", &out)
}

// WorkerSiblings returns sibling task titles (wrap.list_sibling_tasks).
func (c *Client) WorkerSiblings(ctx context.Context, workerID string) (intake.SiblingTasksResponse, error) {
	var out intake.SiblingTasksResponse
	return out, c.workerGet(ctx, workerID, "siblings", &out)
}

// WorkerReportProgress records a free-text progress line (wrap.report_progress).
func (c *Client) WorkerReportProgress(ctx context.Context, workerID, msg string) error {
	return c.workerPost(ctx, workerID, "progress", intake.WorkerReportRequest{Msg: msg})
}

// WorkerReportDone records the worker's completion summary (wrap.report_done).
func (c *Client) WorkerReportDone(ctx context.Context, workerID, summary string) error {
	return c.workerPost(ctx, workerID, "done", intake.WorkerReportRequest{Summary: summary})
}

// WorkerReportBlocked records that the worker is blocked (wrap.report_blocked).
func (c *Client) WorkerReportBlocked(ctx context.Context, workerID, reason string) error {
	return c.workerPost(ctx, workerID, "blocked", intake.WorkerReportRequest{Reason: reason})
}

func (c *Client) workerGet(ctx context.Context, workerID, sub string, out any) error {
	url := "http://wrap/workers/" + workerID + "/" + sub
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("worker %s: %w", sub, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("worker %q: %w", workerID, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker %s: status %d: %s", sub, resp.StatusCode, raw)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) workerPost(ctx context.Context, workerID, sub string, body intake.WorkerReportRequest) error {
	b, _ := json.Marshal(body)
	url := "http://wrap/workers/" + workerID + "/" + sub
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("worker %s: %w", sub, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("worker %q: %w", workerID, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker %s: status %d: %s", sub, resp.StatusCode, raw)
	}
	return nil
}
