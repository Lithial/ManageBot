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
