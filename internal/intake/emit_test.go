package intake

import (
	"context"
	"strings"
	"testing"
)

func doneRun(kind string) GetRunResponse {
	return GetRunResponse{
		RunID: "r1", Phase: "done", IntakeKind: kind,
		IntakeRef: "/specs/a.md", MergeBranch: "wrap/r1/merge", MergeSummary: "did the work",
	}
}

func TestEmit_cliPrintsBranch(t *testing.T) {
	var out strings.Builder
	err := Emit(context.Background(), doneRun("cli"), "/repo", EmitDeps{Stdout: &out})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !strings.Contains(out.String(), "wrap/r1/merge") {
		t.Errorf("output = %q, want the merge branch", out.String())
	}
}

func TestEmit_specfileWritesSidecar(t *testing.T) {
	var gotPath, gotContent string
	deps := EmitDeps{
		Stdout: &strings.Builder{},
		WriteSidecar: func(path, content string) error {
			gotPath, gotContent = path, content
			return nil
		},
	}
	if err := Emit(context.Background(), doneRun("specfile"), "/repo", deps); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if gotPath != "/specs/a.md.DONE" {
		t.Errorf("sidecar path = %q, want /specs/a.md.DONE", gotPath)
	}
	if !strings.Contains(gotContent, "wrap/r1/merge") {
		t.Errorf("sidecar content = %q, want the merge branch", gotContent)
	}
}

func TestEmit_githubPushesAndOpensPR(t *testing.T) {
	var gotRepo, gotBranch, gotIssue string
	deps := EmitDeps{
		Stdout: &strings.Builder{},
		PushAndPR: func(_ context.Context, repo, branch, _, _, issueURL string) (string, error) {
			gotRepo, gotBranch, gotIssue = repo, branch, issueURL
			return "https://github.com/o/r/pull/9", nil
		},
	}
	run := doneRun("github")
	run.IntakeRef = "https://github.com/o/r/issues/7"
	if err := Emit(context.Background(), run, "/repo", deps); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if gotRepo != "/repo" || gotBranch != "wrap/r1/merge" {
		t.Errorf("push args repo=%q branch=%q", gotRepo, gotBranch)
	}
	if gotIssue != "https://github.com/o/r/issues/7" {
		t.Errorf("issue URL = %q", gotIssue)
	}
}

func TestEmit_notDoneIsError(t *testing.T) {
	run := doneRun("cli")
	run.Phase = "working"
	if err := Emit(context.Background(), run, "/repo", EmitDeps{Stdout: &strings.Builder{}}); err == nil {
		t.Fatal("want error when run is not done")
	}
}

func TestEmit_unknownKindIsError(t *testing.T) {
	if err := Emit(context.Background(), doneRun("carrier-pigeon"), "/repo", EmitDeps{Stdout: &strings.Builder{}}); err == nil {
		t.Fatal("want error for unknown intake kind")
	}
}
