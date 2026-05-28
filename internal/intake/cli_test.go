package intake_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestCLIAdapterSubmitsSpecFile(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := client.New(sock)

	// Create a repo dir with a spec file inside.
	repo := t.TempDir()
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# my spec\n\nDo a thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := intake.NewCLIAdapter(c)
	resp, err := adapter.SubmitFromSpec(context.Background(), specPath, repo)
	if err != nil {
		t.Fatalf("SubmitFromSpec: %v", err)
	}
	if resp.RunID == "" {
		t.Error("RunID empty")
	}
}
