// Package api hosts wrapd's HTTP API over a Unix-socket listener.
package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Lithial/ManageBot/internal/store"
)

type Server struct {
	store      *store.Store
	socketPath string
	httpSrv    *http.Server
	readyCh    chan struct{}
}

func NewServer(s *store.Store, socketPath string) *Server {
	srv := &Server{store: s, socketPath: socketPath, readyCh: make(chan struct{})}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	srv.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv
}

// Ready returns a channel that is closed once the server's Unix socket is
// bound and ready to accept connections.
func (s *Server) Ready() <-chan struct{} { return s.readyCh }

func (s *Server) Serve() error {
	// Best-effort: remove a stale socket if one exists.
	_ = os.Remove(s.socketPath)

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	// Restrict socket to the current user.
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = l.Close()
		_ = os.Remove(s.socketPath)
		return err
	}
	close(s.readyCh)
	if err := s.httpSrv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.httpSrv.Shutdown(ctx)
	_ = os.Remove(s.socketPath)
	return err
}
