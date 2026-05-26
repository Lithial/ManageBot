package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /runs", s.handleSubmitRun)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleSubmitRun(w http.ResponseWriter, r *http.Request) {
	var req intake.SubmitRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ProjectName == "" || req.RepoPath == "" || req.IntakeKind == "" || req.SpecMD == "" {
		writeError(w, http.StatusBadRequest, "project_name, repo_path, intake_kind, spec_md are required")
		return
	}

	ctx := r.Context()
	pid, err := s.findOrCreateProject(ctx, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	gates := req.GatesJSON
	if gates == "" {
		// Pull the project's default gates.
		p, err := s.store.ProjectByName(ctx, req.ProjectName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		gates = p.DefaultGatesJSON
	}

	rid, err := s.store.InsertRun(ctx, store.Run{
		ProjectID:  pid,
		IntakeKind: req.IntakeKind,
		IntakeRef:  req.IntakeRef,
		SpecMD:     req.SpecMD,
		GatesJSON:  gates,
		Phase:      "pending",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, intake.SubmitRunResponse{
		RunID:     rid,
		ProjectID: pid,
		Phase:     "pending",
	})
}

func (s *Server) findOrCreateProject(ctx context.Context, req intake.SubmitRunRequest) (string, error) {
	p, err := s.store.ProjectByName(ctx, req.ProjectName)
	if err == nil {
		return p.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		// Real DB error, not "row not found" — propagate.
		return "", err
	}
	defaultGates := `{"plan":{"mode":"require_approval"},"worker_done":{"mode":"auto"},"merge":{"mode":"require_approval"},"custom":[]}`
	verCmd := sql.NullString{}
	if req.VerificationCommand != "" {
		verCmd = sql.NullString{String: req.VerificationCommand, Valid: true}
	}
	return s.store.InsertProject(ctx, store.Project{
		Name:                req.ProjectName,
		RepoPath:            req.RepoPath,
		DefaultGatesJSON:    defaultGates,
		VerificationCommand: verCmd,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, intake.ErrorResponse{Error: msg})
}
