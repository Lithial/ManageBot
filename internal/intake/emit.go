package intake

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// EmitDeps are the side-effecting operations emission needs, injected so the
// dispatch logic stays testable. cmd/wrap supplies the real implementations
// (filesystem sidecar; `git push` + `gh pr create`).
type EmitDeps struct {
	Stdout io.Writer
	// WriteSidecar writes the specfile DONE sidecar (path, content).
	WriteSidecar func(path, content string) error
	// PushAndPR pushes branch from repo and opens a PR, returning its URL.
	PushAndPR func(ctx context.Context, repo, branch, title, body, issueURL string) (prURL string, err error)
}

// Emit performs the emission for a finished run, dispatched by intake_kind:
// cli → print the merge branch; specfile → write a `<spec>.DONE` sidecar;
// github → push the merge branch and open a PR. The run must be `done`.
func Emit(ctx context.Context, run GetRunResponse, repoPath string, deps EmitDeps) error {
	if run.Phase != "done" {
		return fmt.Errorf("run %s is not done (phase=%s); nothing to emit", run.RunID, run.Phase)
	}
	switch run.IntakeKind {
	case "cli":
		fmt.Fprintf(deps.Stdout, "run %s done — merged branch: %s\n", run.RunID, run.MergeBranch)
		if run.MergeSummary != "" {
			fmt.Fprintf(deps.Stdout, "summary: %s\n", run.MergeSummary)
		}
		return nil

	case "specfile":
		if deps.WriteSidecar == nil {
			return fmt.Errorf("specfile emission: no sidecar writer configured")
		}
		path := run.IntakeRef + ".DONE"
		content := fmt.Sprintf("run: %s\nbranch: %s\nsummary: %s\n", run.RunID, run.MergeBranch, run.MergeSummary)
		if err := deps.WriteSidecar(path, content); err != nil {
			return fmt.Errorf("write sidecar %s: %w", path, err)
		}
		fmt.Fprintf(deps.Stdout, "wrote %s\n", path)
		return nil

	case "github":
		if deps.PushAndPR == nil {
			return fmt.Errorf("github emission: no push/PR function configured")
		}
		title := firstLine(run.MergeSummary)
		if title == "" {
			title = "wrap run " + run.RunID
		}
		body := run.MergeSummary
		if run.IntakeRef != "" {
			body = strings.TrimRight(body, "\n") + "\n\nCloses " + run.IntakeRef + "\n"
		}
		prURL, err := deps.PushAndPR(ctx, repoPath, run.MergeBranch, title, body, run.IntakeRef)
		if err != nil {
			return fmt.Errorf("push + open PR: %w", err)
		}
		fmt.Fprintf(deps.Stdout, "opened PR: %s\n", prURL)
		return nil

	default:
		return fmt.Errorf("unknown intake kind %q for run %s", run.IntakeKind, run.RunID)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
