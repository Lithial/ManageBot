package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

// registerWorkerRoutes wires the worker-facing endpoints the MCP bridge relays
// the wrap.* tool calls to. These mutate/read only via recorded events; the
// orchestrator finalizes worker status post-exit.
func (s *Server) registerWorkerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /workers/{id}/task", s.handleWorkerTask)
	mux.HandleFunc("GET /workers/{id}/siblings", s.handleWorkerSiblings)
	mux.HandleFunc("POST /workers/{id}/progress", s.handleWorkerReport("worker_progress"))
	mux.HandleFunc("POST /workers/{id}/done", s.handleWorkerReport("worker_report_done"))
	mux.HandleFunc("POST /workers/{id}/blocked", s.handleWorkerReport("worker_report_blocked"))
}

// taskRow is the subset of a plan's tasks_json entry the worker endpoints need.
type taskRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// workerPlanTasks loads a worker and its run's plan tasks. Returns ErrNotFound
// (mapped by the caller) if the worker is unknown.
func (s *Server) workerPlanTasks(ctx context.Context, workerID string) (store.Worker, []taskRow, error) {
	w, err := s.store.GetWorker(ctx, workerID)
	if err != nil {
		return store.Worker{}, nil, err
	}
	plan, err := s.store.GetPlanByRun(ctx, w.RunID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return w, nil, nil // worker exists, plan not yet — empty task set
		}
		return store.Worker{}, nil, err
	}
	var tasks []taskRow
	_ = json.Unmarshal([]byte(plan.TasksJSON), &tasks)
	return w, tasks, nil
}

func (s *Server) handleWorkerTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	worker, tasks, err := s.workerPlanTasks(r.Context(), id)
	if err != nil {
		s.writeWorkerErr(w, err)
		return
	}
	out := intake.WorkerTaskResponse{TaskID: worker.TaskID}
	for _, t := range tasks {
		if t.ID == worker.TaskID {
			out.Title = t.Title
			out.Description = t.Description
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleWorkerSiblings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	worker, tasks, err := s.workerPlanTasks(r.Context(), id)
	if err != nil {
		s.writeWorkerErr(w, err)
		return
	}
	titles := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if t.ID != worker.TaskID {
			titles = append(titles, t.Title)
		}
	}
	writeJSON(w, http.StatusOK, intake.SiblingTasksResponse{Titles: titles})
}

// handleWorkerReport records a worker's MCP report as an event of `kind`.
func (s *Server) handleWorkerReport(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ctx := r.Context()
		worker, err := s.store.GetWorker(ctx, id)
		if err != nil {
			s.writeWorkerErr(w, err)
			return
		}
		var req intake.WorkerReportRequest
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		payload := map[string]string{}
		switch kind {
		case "worker_progress":
			payload["msg"] = req.Msg
		case "worker_report_done":
			payload["summary"] = req.Summary
		case "worker_report_blocked":
			payload["reason"] = req.Reason
		}
		b, _ := json.Marshal(payload)
		if _, err := s.store.InsertEvent(ctx, store.Event{
			RunID: worker.RunID, WorkerID: worker.ID, Kind: kind, PayloadJSON: string(b),
		}); err != nil {
			log.Printf("api: worker report %s: %v", kind, err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) writeWorkerErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "worker not found")
		return
	}
	log.Printf("api: %v", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}
