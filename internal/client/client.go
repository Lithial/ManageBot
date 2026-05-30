// Package client is the Go-level client for the wrapd HTTP API over Unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/Lithial/ManageBot/internal/intake"
)

type Client struct {
	http       *http.Client
	socketPath string
}

// ErrNotFound is returned by GetRun (and future read methods) when the
// server responds 404. Callers should use errors.Is(err, client.ErrNotFound).
var ErrNotFound = errors.New("not found")

func New(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

func (c *Client) Healthz(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://wrap/healthz", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("healthz: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) ListRuns(ctx context.Context) ([]intake.RunSummary, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://wrap/runs", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list runs: status %d: %s", resp.StatusCode, raw)
	}
	var out intake.ListRunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out.Runs, nil
}

func (c *Client) GetRun(ctx context.Context, id string) (intake.GetRunResponse, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://wrap/runs/"+id, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return intake.GetRunResponse{}, fmt.Errorf("get run: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return intake.GetRunResponse{}, fmt.Errorf("run %q: %w", id, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return intake.GetRunResponse{}, fmt.Errorf("get run: status %d: %s", resp.StatusCode, raw)
	}
	var out intake.GetRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return intake.GetRunResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// Approve resolves the run's current pending gate as approved.
func (c *Client) Approve(ctx context.Context, runID, by string) (intake.ResolveGateResponse, error) {
	return c.resolveGate(ctx, runID, "approve", by)
}

// Reject resolves the run's current pending gate as rejected.
func (c *Client) Reject(ctx context.Context, runID, by string) (intake.ResolveGateResponse, error) {
	return c.resolveGate(ctx, runID, "reject", by)
}

func (c *Client) resolveGate(ctx context.Context, runID, action, by string) (intake.ResolveGateResponse, error) {
	body, err := json.Marshal(intake.ResolveGateRequest{By: by})
	if err != nil {
		return intake.ResolveGateResponse{}, err
	}
	url := "http://wrap/runs/" + runID + "/" + action
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return intake.ResolveGateResponse{}, fmt.Errorf("%s gate: %w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return intake.ResolveGateResponse{}, fmt.Errorf("run %q: %w", runID, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return intake.ResolveGateResponse{}, fmt.Errorf("%s gate: status %d: %s", action, resp.StatusCode, raw)
	}
	var out intake.ResolveGateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return intake.ResolveGateResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func (c *Client) SubmitRun(ctx context.Context, req intake.SubmitRunRequest) (intake.SubmitRunResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return intake.SubmitRunResponse{}, err
	}
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://wrap/runs", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return intake.SubmitRunResponse{}, fmt.Errorf("submit run: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return intake.SubmitRunResponse{}, fmt.Errorf("submit run: status %d: %s", resp.StatusCode, raw)
	}
	var out intake.SubmitRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return intake.SubmitRunResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}
