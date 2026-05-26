package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Lithial/ManageBot/internal/api"
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
	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("wrapd: listening on %s, state in %s\n", *socket, *stateDir)
		errCh <- srv.Serve()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		fmt.Printf("wrapd: caught %s, shutting down\n", s)
	case err := <-errCh:
		if err != nil {
			log.Fatalf("serve: %v", err)
		}
	}
	if err := srv.Close(); err != nil {
		log.Printf("wrapd: shutdown error: %v", err)
	}
}
