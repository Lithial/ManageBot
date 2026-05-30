package intake

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeSubmitter captures the last SubmitRunRequest for assertions.
type fakeSubmitter struct {
	got SubmitRunRequest
	err error
}

func (f *fakeSubmitter) SubmitRun(_ context.Context, req SubmitRunRequest) (SubmitRunResponse, error) {
	f.got = req
	if f.err != nil {
		return SubmitRunResponse{}, f.err
	}
	return SubmitRunResponse{RunID: "r1", Phase: "pending"}, nil
}

func TestParseFrontmatter_present(t *testing.T) {
	meta, body := parseFrontmatter("---\nproject: demo\nrepo: /tmp/r\nverification_command: make test\n---\n# Spec\n\nbody line\n")
	if meta.Project != "demo" || meta.Repo != "/tmp/r" || meta.VerificationCommand != "make test" {
		t.Errorf("meta = %+v", meta)
	}
	if body != "# Spec\n\nbody line\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontmatter_absent(t *testing.T) {
	content := "# Just a spec\n\nno frontmatter\n"
	meta, body := parseFrontmatter(content)
	if meta.Project != "" || meta.Repo != "" || meta.VerificationCommand != "" {
		t.Errorf("meta should be empty: %+v", meta)
	}
	if body != content {
		t.Errorf("body = %q, want full content", body)
	}
}

func TestParseFrontmatter_ignoresUnknownKeysAndBlanks(t *testing.T) {
	meta, body := parseFrontmatter("---\nproject: p\nweird: x\n\n---\nB\n")
	if meta.Project != "p" {
		t.Errorf("project = %q", meta.Project)
	}
	if body != "B\n" {
		t.Errorf("body = %q", body)
	}
}

func TestSpecfileAdapter_usesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "feature.md")
	repo := t.TempDir()
	content := "---\nproject: myproj\nrepo: " + repo + "\nverification_command: go test ./...\n---\nBuild the feature.\n"
	if err := os.WriteFile(spec, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := &fakeSubmitter{}
	_, err := NewSpecfileAdapter(fs).SubmitFromFile(context.Background(), spec, "/ignored/fallback")
	if err != nil {
		t.Fatalf("SubmitFromFile: %v", err)
	}
	if fs.got.IntakeKind != "specfile" {
		t.Errorf("IntakeKind = %q, want specfile", fs.got.IntakeKind)
	}
	if fs.got.ProjectName != "myproj" {
		t.Errorf("ProjectName = %q, want myproj", fs.got.ProjectName)
	}
	if fs.got.RepoPath != repo {
		t.Errorf("RepoPath = %q, want %q", fs.got.RepoPath, repo)
	}
	if fs.got.VerificationCommand != "go test ./..." {
		t.Errorf("VerificationCommand = %q", fs.got.VerificationCommand)
	}
	if fs.got.SpecMD != "Build the feature.\n" {
		t.Errorf("SpecMD = %q", fs.got.SpecMD)
	}
	if fs.got.IntakeRef != spec {
		t.Errorf("IntakeRef = %q, want %q", fs.got.IntakeRef, spec)
	}
}

func TestSpecfileAdapter_fallbackRepoAndProject(t *testing.T) {
	repo := t.TempDir() // basename becomes the project name
	spec := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(spec, []byte("no frontmatter here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSubmitter{}
	if _, err := NewSpecfileAdapter(fs).SubmitFromFile(context.Background(), spec, repo); err != nil {
		t.Fatalf("SubmitFromFile: %v", err)
	}
	if fs.got.RepoPath != repo {
		t.Errorf("RepoPath = %q, want fallback %q", fs.got.RepoPath, repo)
	}
	if fs.got.ProjectName != filepath.Base(repo) {
		t.Errorf("ProjectName = %q, want %q", fs.got.ProjectName, filepath.Base(repo))
	}
	if fs.got.SpecMD != "no frontmatter here\n" {
		t.Errorf("SpecMD = %q", fs.got.SpecMD)
	}
}
