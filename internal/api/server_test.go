package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/api"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

func readerOf(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func startServer(t *testing.T) (*http.Client, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "wrap.sock")
	dbPath := filepath.Join(t.TempDir(), "wrap.db")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := api.NewServer(s, sock)
	go func() {
		_ = srv.Serve()
	}()
	t.Cleanup(func() { _ = srv.Close() })

	// Wait for the socket to appear (up to 1s).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
	return client, sock
}

func TestHealthz(t *testing.T) {
	client, _ := startServer(t)

	resp, err := client.Get("http://wrap/healthz")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestSubmitRunCreatesProjectAndRun(t *testing.T) {
	client, _ := startServer(t)

	body := intake.SubmitRunRequest{
		ProjectName: "demo",
		RepoPath:    "/tmp/demo-repo",
		IntakeKind:  "cli",
		IntakeRef:   "/tmp/spec.md",
		SpecMD:      "# demo",
	}
	buf, _ := json.Marshal(body)

	resp, err := client.Post("http://wrap/runs", "application/json", readerOf(buf))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}

	var out intake.SubmitRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RunID == "" {
		t.Error("RunID empty")
	}
	if out.ProjectID == "" {
		t.Error("ProjectID empty")
	}
	if out.Phase != "pending" {
		t.Errorf("Phase = %q, want %q", out.Phase, "pending")
	}
}

func TestSubmitRunReusesExistingProject(t *testing.T) {
	client, _ := startServer(t)

	body := intake.SubmitRunRequest{
		ProjectName: "demo",
		RepoPath:    "/tmp/demo-repo",
		IntakeKind:  "cli",
		SpecMD:      "# first",
	}
	buf, _ := json.Marshal(body)
	resp1, err := client.Post("http://wrap/runs", "application/json", readerOf(buf))
	if err != nil {
		t.Fatalf("first Post: %v", err)
	}
	defer resp1.Body.Close()
	var out1 intake.SubmitRunResponse
	_ = json.NewDecoder(resp1.Body).Decode(&out1)

	body.SpecMD = "# second"
	buf, _ = json.Marshal(body)
	resp2, err := client.Post("http://wrap/runs", "application/json", readerOf(buf))
	if err != nil {
		t.Fatalf("second Post: %v", err)
	}
	defer resp2.Body.Close()
	var out2 intake.SubmitRunResponse
	_ = json.NewDecoder(resp2.Body).Decode(&out2)

	if out1.ProjectID != out2.ProjectID {
		t.Errorf("project IDs differ: %q vs %q", out1.ProjectID, out2.ProjectID)
	}
	if out1.RunID == out2.RunID {
		t.Errorf("run IDs equal but should differ")
	}
}
