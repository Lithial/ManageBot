package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Lithial/ManageBot/internal/api"
	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
)

func defaultStateDir() string {
	if v := os.Getenv("WRAP_STATE_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "wrap")
	}
	return filepath.Join(home, ".wrap")
}

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
	stateDir := flag.String("state-dir", defaultStateDir(), "directory for wrapd state (DB, worktrees)")
	socket := flag.String("socket", defaultSocketPath(), "Unix socket path to listen on")
	plannerCmd := flag.String("planner-cmd", "claude", "executable to spawn as the planner (Phase 2: bare path; future phases add args)")
	plannerEnvFlag := flag.String("planner-env", "", "comma-separated KEY=VAL pairs to add to the planner's environment (test helper)")
	tickInterval := flag.Duration("tick-interval", 500*time.Millisecond, "orchestrator poll interval")
	flag.Parse()

	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		log.Fatalf("mkdir state dir: %v", err)
	}
	dbPath := filepath.Join(*stateDir, "wrap.db")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		log.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	srv := api.NewServer(s, *socket)
	srvErrCh := make(chan error, 1)
	go func() { srvErrCh <- srv.Serve() }()

	select {
	case <-srv.Ready():
		fmt.Printf("wrapd: listening on %s, state in %s\n", *socket, *stateDir)
	case err := <-srvErrCh:
		if err != nil {
			log.Fatalf("serve: %v", err)
		}
	}

	plannerEnv := parseEnvFlag(*plannerEnvFlag)
	orch := orchestrator.New(orchestrator.Config{
		Store:    s,
		StateDir: *stateDir,
		PlannerCmd: func(spec string) *exec.Cmd {
			c := exec.Command(*plannerCmd)
			if len(plannerEnv) > 0 {
				c.Env = append(os.Environ(), plannerEnv...)
			}
			return c
		},
		StepTimeout: 5 * time.Minute,
	})
	orchCtx, orchCancel := context.WithCancel(context.Background())
	go orch.Run(orchCtx, *tickInterval)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		fmt.Printf("wrapd: caught %s, shutting down\n", s)
	case err := <-srvErrCh:
		if err != nil {
			log.Fatalf("serve: %v", err)
		}
	}
	orchCancel()
	if err := srv.Close(); err != nil {
		log.Printf("wrapd: shutdown error: %v", err)
	}
}

// parseEnvFlag splits "K1=V1,K2=V2" into []string{"K1=V1","K2=V2"}.
// Empty input returns nil. Pairs without '=' are dropped silently.
func parseEnvFlag(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || !strings.Contains(p, "=") {
			continue
		}
		out = append(out, p)
	}
	return out
}
