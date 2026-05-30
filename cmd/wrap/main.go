package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
)

func defaultSocketPath() string {
	if v := os.Getenv("WRAP_SOCKET"); v != "" {
		return v
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "wrap.sock")
	}
	return filepath.Join(os.TempDir(), "wrap.sock")
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wrap: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: wrap <command> [args...]\ncommands: run, approve, reject")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(rest)
	case "approve":
		return cmdResolveGate("approve", rest)
	case "reject":
		return cmdResolveGate("reject", rest)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// cmdResolveGate implements `wrap approve|reject <run-id>`, resolving the run's
// current pending gate.
func cmdResolveGate(action string, args []string) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	socket := fs.String("socket", defaultSocketPath(), "wrapd Unix socket path")
	by := fs.String("by", "cli", "who is resolving the gate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: wrap %s [--socket PATH] [--by NAME] <run-id>", action)
	}
	runID := fs.Arg(0)

	c := client.New(*socket)
	var resp intake.ResolveGateResponse
	var err error
	if action == "approve" {
		resp, err = c.Approve(context.Background(), runID, *by)
	} else {
		resp, err = c.Reject(context.Background(), runID, *by)
	}
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	socket := fs.String("socket", defaultSocketPath(), "wrapd Unix socket path")
	repo := fs.String("repo", "", "repo path (defaults to current working directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: wrap run [--socket PATH] [--repo PATH] <spec.md>")
	}
	specPath := fs.Arg(0)

	repoPath := *repo
	if repoPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		repoPath = cwd
	}

	c := client.New(*socket)
	adapter := intake.NewCLIAdapter(c)
	resp, err := adapter.SubmitFromSpec(context.Background(), specPath, repoPath)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	return nil
}
