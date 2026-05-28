package intake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// RunSubmitter is the interface for submitting runs.
type RunSubmitter interface {
	SubmitRun(ctx context.Context, req SubmitRunRequest) (SubmitRunResponse, error)
}

// CLIAdapter implements the `wrap run <spec>` intake path.
type CLIAdapter struct {
	submitter RunSubmitter
}

func NewCLIAdapter(s RunSubmitter) *CLIAdapter {
	return &CLIAdapter{submitter: s}
}

// SubmitFromSpec reads the spec markdown at specPath and submits a new run
// against the project rooted at repoPath. Returns the daemon's response.
func (a *CLIAdapter) SubmitFromSpec(ctx context.Context, specPath, repoPath string) (SubmitRunResponse, error) {
	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("abs spec path: %w", err)
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("abs repo path: %w", err)
	}
	specBytes, err := os.ReadFile(absSpec)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("read spec: %w", err)
	}
	projectName := filepath.Base(absRepo)
	return a.submitter.SubmitRun(ctx, SubmitRunRequest{
		ProjectName: projectName,
		RepoPath:    absRepo,
		IntakeKind:  "cli",
		IntakeRef:   absSpec,
		SpecMD:      string(specBytes),
	})
}
