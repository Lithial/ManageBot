package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
)

// repoOrCwd returns the flag value, or the current working directory if empty.
func repoOrCwd(repo string) (string, error) {
	if repo != "" {
		return repo, nil
	}
	return os.Getwd()
}

// cmdSubmit implements the specfile adapter: `wrap submit <spec.md>`.
func cmdSubmit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	socket := fs.String("socket", defaultSocketPath(), "wrapd Unix socket path")
	repo := fs.String("repo", "", "repo path fallback when the spec omits `repo` (defaults to cwd)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: wrap submit [--socket PATH] [--repo PATH] <spec.md>")
	}
	repoPath, err := repoOrCwd(*repo)
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	adapter := intake.NewSpecfileAdapter(client.New(*socket))
	resp, err := adapter.SubmitFromFile(context.Background(), fs.Arg(0), repoPath)
	if err != nil {
		return err
	}
	return printJSON(resp)
}

// cmdGitHub implements the GitHub adapter: `wrap github <issue-ref>`.
func cmdGitHub(args []string) error {
	fs := flag.NewFlagSet("github", flag.ContinueOnError)
	socket := fs.String("socket", defaultSocketPath(), "wrapd Unix socket path")
	repo := fs.String("repo", "", "local repo path the run acts on (defaults to cwd)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: wrap github [--socket PATH] [--repo PATH] <issue-url|owner/repo#n>")
	}
	repoPath, err := repoOrCwd(*repo)
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	adapter := intake.NewGitHubAdapter(client.New(*socket), ghFetcher{})
	resp, err := adapter.SubmitFromIssue(context.Background(), fs.Arg(0), repoPath)
	if err != nil {
		return err
	}
	return printJSON(resp)
}

// cmdEmit implements `wrap emit <run-id>`: read the run and perform its
// intake-kind-specific emission (print branch / write sidecar / push + PR).
func cmdEmit(args []string) error {
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	socket := fs.String("socket", defaultSocketPath(), "wrapd Unix socket path")
	repo := fs.String("repo", "", "repo path for github push/PR (defaults to cwd)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: wrap emit [--socket PATH] [--repo PATH] <run-id>")
	}
	repoPath, err := repoOrCwd(*repo)
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	ctx := context.Background()
	run, err := client.New(*socket).GetRun(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return intake.Emit(ctx, run, repoPath, intake.EmitDeps{
		Stdout:       os.Stdout,
		WriteSidecar: func(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) },
		PushAndPR:    pushAndPR,
	})
}

// ghFetcher fetches an issue via the `gh` CLI (reusing the user's gh auth).
type ghFetcher struct{}

func (ghFetcher) Fetch(ctx context.Context, ref string) (intake.Issue, error) {
	out, err := exec.CommandContext(ctx, "gh", "issue", "view", ref, "--json", "title,body,url").Output()
	if err != nil {
		return intake.Issue{}, fmt.Errorf("gh issue view: %w", withStderr(err))
	}
	var j struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(out, &j); err != nil {
		return intake.Issue{}, fmt.Errorf("parse gh output: %w", err)
	}
	return intake.Issue{Title: j.Title, Body: j.Body, URL: j.URL}, nil
}

// pushAndPR pushes branch from repo to origin and opens a PR via `gh`.
func pushAndPR(ctx context.Context, repo, branch, title, body, _ string) (string, error) {
	push := exec.CommandContext(ctx, "git", "-C", repo, "push", "-u", "origin", branch)
	if out, err := push.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git push %s: %w\n%s", branch, err, out)
	}
	pr := exec.CommandContext(ctx, "gh", "pr", "create", "--head", branch, "--title", title, "--body", body)
	pr.Dir = repo
	out, err := pr.Output()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %w", withStderr(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// withStderr enriches an *exec.ExitError with the captured stderr tail.
func withStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

func printJSON(v any) error {
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
	return nil
}
