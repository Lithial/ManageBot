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
	workerCmd := flag.String("worker-cmd", "claude", "executable to spawn as each worker (Phase 3: bare path; future phases add args)")
	workerEnvFlag := flag.String("worker-env", "", "comma-separated KEY=VAL pairs to add to each worker's environment (test helper)")
	mergerCmd := flag.String("merger-cmd", "claude", "executable to spawn as the merger (Phase 4: bare path; future phases add args)")
	mergerEnvFlag := flag.String("merger-env", "", "comma-separated KEY=VAL pairs to add to the merger's environment (test helper)")
	maxWorkers := flag.Int("max-workers", 4, "max simultaneous worker subprocesses per run")
	retryBudget := flag.Int("worker-retry-budget", 1, "extra attempts a retryable worker failure (crash/timeout) gets")
	tickInterval := flag.Duration("tick-interval", 500*time.Millisecond, "orchestrator poll interval")
	stepTimeout := flag.Duration("step-timeout", 5*time.Minute, "per-step timeout for a planner/worker subprocess (kill budget)")
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
	workerEnv := parseEnvFlag(*workerEnvFlag)
	mergerEnv := parseEnvFlag(*mergerEnvFlag)
	orch := orchestrator.New(orchestrator.Config{
		Store:    s,
		StateDir: *stateDir,
		PlannerCmd: func(_ string) *exec.Cmd {
			c := exec.Command(*plannerCmd)
			if len(plannerEnv) > 0 {
				c.Env = append(os.Environ(), plannerEnv...)
			}
			return c
		},
		WorkerCmd: func(_ string) *exec.Cmd {
			c := exec.Command(*workerCmd)
			if len(workerEnv) > 0 {
				c.Env = append(os.Environ(), workerEnv...)
			}
			return c
		},
		MergerCmd: func(_ string) *exec.Cmd {
			c := exec.Command(*mergerCmd)
			if len(mergerEnv) > 0 {
				c.Env = append(os.Environ(), mergerEnv...)
			}
			return c
		},
		MaxWorkers:  *maxWorkers,
		RetryBudget: *retryBudget,
		StepTimeout: *stepTimeout,
	})
	// Recover any runs/workers left mid-flight by a previous process before the
	// tick loop resumes the survivors.
	if err := orch.Reconcile(context.Background()); err != nil {
		log.Printf("wrapd: reconcile on startup: %v", err)
	}
	orchCtx, orchCancel := context.WithCancel(context.Background())
	go orch.Run(orchCtx, *tickInterval)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		fmt.Printf("wrapd: caught %s, shutting down\n", sig)
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
// Empty input returns nil. Pairs without '=' are logged and dropped.
func parseEnvFlag(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "=") {
			log.Printf("wrapd: --planner-env: ignoring malformed pair %q (no '=')", p)
			continue
		}
		out = append(out, p)
	}
	return out
}
