package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/gates"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /runs", s.handleSubmitRun)
	mux.HandleFunc("GET /runs", s.handleListRuns)
	mux.HandleFunc("GET /runs/{id}", s.handleGetRun)
	mux.HandleFunc("POST /runs/{id}/approve", s.handleResolveGate("approved"))
	mux.HandleFunc("POST /runs/{id}/reject", s.handleResolveGate("rejected"))
	mux.HandleFunc("POST /runs/{id}/resolve", s.handleResolveDecision)
	mux.HandleFunc("POST /runs/{id}/kill", s.handleKill)
	s.registerWorkerRoutes(mux)
}

// handleKill moves a run to the terminal `killed` phase and rejects any pending
// gate. The orchestrator's kill watcher cancels in-flight subprocesses; this
// handler only writes state.
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	phase, err := fsm.ParsePhase(run.Phase)
	if err != nil {
		log.Printf("api: kill parse phase: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// Kill is invalid from a terminal phase.
	if _, err := fsm.Advance(phase, fsm.EventKill); err != nil {
		writeError(w, http.StatusConflict, "run is already terminal")
		return
	}
	if err := s.store.UpdateRunPhase(ctx, id, string(fsm.PhaseKilled)); err != nil {
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// Reject any pending gate so it doesn't linger awaiting a decision.
	if g, err := s.store.PendingGateByRun(ctx, id); err == nil {
		if err := s.store.ResolveGate(ctx, g.ID, "rejected", "killed_by_user", ""); err != nil && !errors.Is(err, store.ErrGateNotPending) {
			log.Printf("api: kill reject gate: %v", err)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("api: kill pending gate: %v", err)
	}
	writeJSON(w, http.StatusOK, intake.KillResponse{RunID: id, Phase: string(fsm.PhaseKilled)})
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
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	gates := req.GatesJSON
	if gates == "" {
		// Pull the project's default gates.
		p, err := s.store.ProjectByName(ctx, req.ProjectName)
		if err != nil {
			log.Printf("api: %v", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
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
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, intake.SubmitRunResponse{
		RunID:     rid,
		ProjectID: pid,
		Phase:     "pending",
	})
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runs, err := s.store.ListRuns(ctx)
	if err != nil {
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	out := intake.ListRunsResponse{Runs: make([]intake.RunSummary, 0, len(runs))}
	for _, run := range runs {
		summary := intake.RunSummary{RunID: run.ID, Phase: run.Phase}
		// Annotate rows awaiting a human decision so the dashboard can flag them.
		if g, err := s.store.PendingGateByRun(ctx, run.ID); err == nil {
			summary.PendingGateKind = g.Kind
		} else if !errors.Is(err, store.ErrNotFound) {
			log.Printf("api: pending gate for %s: %v", run.ID, err)
		}
		out.Runs = append(out.Runs, summary)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	out := intake.GetRunResponse{
		RunID:      run.ID,
		ProjectID:  run.ProjectID,
		Phase:      run.Phase,
		IntakeKind: run.IntakeKind,
		IntakeRef:  run.IntakeRef,
	}
	plan, err := s.store.GetPlanByRun(ctx, id)
	if err == nil {
		out.PlanMD = plan.PlanMD
		out.TasksJSON = plan.TasksJSON
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("api: get plan: %v", err)
	}

	// Merge result, once produced, lives in the latest merge_done event.
	if ev, err := s.store.LatestEventByKind(ctx, id, "merge_done"); err == nil {
		var p struct {
			Branch  string `json:"branch"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal([]byte(ev.PayloadJSON), &p) == nil {
			out.MergeBranch = p.Branch
			out.MergeSummary = p.Summary
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("api: get merge event: %v", err)
	}

	// Surface a gate awaiting human resolution, if any.
	if g, err := s.store.PendingGateByRun(ctx, id); err == nil {
		out.PendingGateKind = g.Kind
		out.PendingGateID = g.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("api: get pending gate: %v", err)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleResolveGate returns a handler that resolves a run's current pending gate
// to `status` (approved | rejected). The orchestrator observes the resolution on
// its next tick and advances the FSM; the API never mutates run phase directly.
func (s *Server) handleResolveGate(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.resolvePendingGate(w, r, status)
	}
}

// handleResolveDecision resolves the pending gate using a {decision, action}
// body. It exists for the richer actions (retry/takeover/drop_branch) that are
// neither a plain approve nor reject. Decision defaults to approve.
func (s *Server) handleResolveDecision(w http.ResponseWriter, r *http.Request) {
	// Peek the decision to choose the status; the body is decoded again inside
	// resolvePendingGate for `by`/`action` (cheap, and keeps one decode path).
	status := "approved"
	if r.Body != nil {
		var peek intake.ResolveGateRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &peek)
		if peek.Decision == "reject" || peek.Decision == "rejected" {
			status = "rejected"
		}
		r.Body = io.NopCloser(bytes.NewReader(body)) // restore for the re-decode
	}
	s.resolvePendingGate(w, r, status)
}

// resolvePendingGate is the shared resolution path: decode the body, validate
// the action against the gate kind, and resolve. Single writer of phase remains
// the orchestrator — this only flips the gate row.
func (s *Server) resolvePendingGate(w http.ResponseWriter, r *http.Request, status string) {
	id := r.PathValue("id")
	ctx := r.Context()

	// Body is optional; default resolver is "cli".
	req := intake.ResolveGateRequest{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // tolerate empty/invalid body
	}
	by := req.By
	if by == "" {
		by = "cli"
	}

	gate, err := s.store.PendingGateByRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusConflict, "no pending gate for this run")
			return
		}
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// Reject an action that doesn't belong to this gate kind before touching the
	// row, so the orchestrator never observes a nonsense action.
	if !gates.ValidAction(gate.Kind, req.Action) {
		writeError(w, http.StatusBadRequest, "invalid action for gate kind "+gate.Kind)
		return
	}
	if err := s.store.ResolveGate(ctx, gate.ID, status, by, req.Action); err != nil {
		if errors.Is(err, store.ErrGateNotPending) {
			// Lost the race to a concurrent resolution.
			writeError(w, http.StatusConflict, "gate already resolved")
			return
		}
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, intake.ResolveGateResponse{
		RunID: id, GateID: gate.ID, Status: status,
	})
}

func (s *Server) findOrCreateProject(ctx context.Context, req intake.SubmitRunRequest) (string, error) {
	p, err := s.store.ProjectByName(ctx, req.ProjectName)
	if err == nil {
		return p.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		// Real DB error, not "row not found" — propagate.
		return "", err
	}
	defaultGates := `{"plan":{"mode":"require_approval"},"worker_done":{"mode":"auto"},"merge":{"mode":"require_approval"},"custom":[]}`
	return s.store.InsertProject(ctx, store.Project{
		Name:                req.ProjectName,
		RepoPath:            req.RepoPath,
		DefaultGatesJSON:    defaultGates,
		VerificationCommand: req.VerificationCommand,
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
