package intake

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type fakeFetcher struct {
	issue Issue
	err   error
	ref   string
}

func (f *fakeFetcher) Fetch(_ context.Context, ref string) (Issue, error) {
	f.ref = ref
	return f.issue, f.err
}

func TestGitHubAdapter_submitsIssueAsRun(t *testing.T) {
	repo := t.TempDir()
	ff := &fakeFetcher{issue: Issue{
		Title: "Add dark mode",
		Body:  "Users want a dark theme.",
		URL:   "https://github.com/o/r/issues/7",
	}}
	fs := &fakeSubmitter{}

	_, err := NewGitHubAdapter(fs, ff).SubmitFromIssue(context.Background(), "o/r#7", repo)
	if err != nil {
		t.Fatalf("SubmitFromIssue: %v", err)
	}
	if ff.ref != "o/r#7" {
		t.Errorf("fetcher got ref %q, want o/r#7", ff.ref)
	}
	if fs.got.IntakeKind != "github" {
		t.Errorf("IntakeKind = %q, want github", fs.got.IntakeKind)
	}
	if fs.got.IntakeRef != "https://github.com/o/r/issues/7" {
		t.Errorf("IntakeRef = %q, want the issue URL", fs.got.IntakeRef)
	}
	if fs.got.RepoPath != repo || fs.got.ProjectName != filepath.Base(repo) {
		t.Errorf("repo/project = %q/%q", fs.got.RepoPath, fs.got.ProjectName)
	}
	if !strings.Contains(fs.got.SpecMD, "Add dark mode") || !strings.Contains(fs.got.SpecMD, "Users want a dark theme.") {
		t.Errorf("SpecMD = %q, want title + body", fs.got.SpecMD)
	}
}

func TestGitHubAdapter_fetchError(t *testing.T) {
	ff := &fakeFetcher{err: errors.New("gh: not found")}
	_, err := NewGitHubAdapter(&fakeSubmitter{}, ff).SubmitFromIssue(context.Background(), "o/r#404", t.TempDir())
	if err == nil {
		t.Fatal("want error when issue fetch fails")
	}
}
