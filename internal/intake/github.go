package intake

import (
	"context"
	"fmt"
	"path/filepath"
)

// Issue is the subset of a GitHub issue the GitHub adapter needs.
type Issue struct {
	Title string
	Body  string
	URL   string
}

// IssueFetcher fetches an issue by reference (URL or owner/repo#number). The
// production impl shells out to the `gh` CLI; tests use a fake.
type IssueFetcher interface {
	Fetch(ctx context.Context, ref string) (Issue, error)
}

// GitHubAdapter implements the `wrap github <issue-ref>` intake path: a GitHub
// issue becomes a run whose spec is the issue title + body. Emission pushes the
// merge branch and opens a PR.
type GitHubAdapter struct {
	submitter RunSubmitter
	fetcher   IssueFetcher
}

func NewGitHubAdapter(s RunSubmitter, f IssueFetcher) *GitHubAdapter {
	return &GitHubAdapter{submitter: s, fetcher: f}
}

// SubmitFromIssue fetches the issue and submits a run against the repo at
// repoPath. IntakeRef is the issue URL so `wrap emit` can open a PR closing it.
func (a *GitHubAdapter) SubmitFromIssue(ctx context.Context, issueRef, repoPath string) (SubmitRunResponse, error) {
	issue, err := a.fetcher.Fetch(ctx, issueRef)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("fetch issue %q: %w", issueRef, err)
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("abs repo path: %w", err)
	}
	ref := issue.URL
	if ref == "" {
		ref = issueRef
	}
	return a.submitter.SubmitRun(ctx, SubmitRunRequest{
		ProjectName: filepath.Base(absRepo),
		RepoPath:    absRepo,
		IntakeKind:  "github",
		IntakeRef:   ref,
		SpecMD:      fmt.Sprintf("# %s\n\n%s\n", issue.Title, issue.Body),
	})
}
