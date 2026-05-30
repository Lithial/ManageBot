// Package intake defines the canonical request/response types that flow
// between intake adapters, the daemon API, and the daemon's storage layer.
package intake

// SubmitRunRequest is the body of POST /runs.
type SubmitRunRequest struct {
	ProjectName         string `json:"project_name"`
	RepoPath            string `json:"repo_path"`
	IntakeKind          string `json:"intake_kind"` // "cli" | "specfile" | "github"
	IntakeRef           string `json:"intake_ref,omitempty"`
	SpecMD              string `json:"spec_md"`
	GatesJSON           string `json:"gates_json,omitempty"`           // optional; daemon falls back to project default
	VerificationCommand string `json:"verification_command,omitempty"` // optional; only used when project is being created
	MaxWorkers          int    `json:"max_workers,omitempty"`          // optional per-run concurrency cap; 0 ⇒ daemon --max-workers default
}

// SubmitRunResponse is the body of a successful POST /runs.
type SubmitRunResponse struct {
	RunID     string `json:"run_id"`
	ProjectID string `json:"project_id"`
	Phase     string `json:"phase"`
}

// ErrorResponse is the body of any non-2xx response.
type ErrorResponse struct {
	Error string `json:"error"`
}

// GetRunResponse is the body of GET /runs/{id}.
type GetRunResponse struct {
	RunID        string `json:"run_id"`
	ProjectID    string `json:"project_id"`
	Phase        string `json:"phase"`
	IntakeKind   string `json:"intake_kind"`          // "cli" | "specfile" | "github"
	IntakeRef    string `json:"intake_ref,omitempty"` // spec path / issue URL — used by `wrap emit`
	PlanMD       string `json:"plan_md,omitempty"`
	TasksJSON    string `json:"tasks_json,omitempty"`
	MergeBranch  string `json:"merge_branch,omitempty"`  // set once the merger has produced a branch
	MergeSummary string `json:"merge_summary,omitempty"` // the merger's report_done summary
	MaxWorkers   int    `json:"max_workers,omitempty"`   // effective per-run concurrency cap

	PendingGateKind string `json:"pending_gate_kind,omitempty"` // kind of the gate awaiting resolution, if any
	PendingGateID   string `json:"pending_gate_id,omitempty"`   // id of that gate
}

// RunSummary is one entry in the GET /runs list (TUI dashboard row).
type RunSummary struct {
	RunID           string `json:"run_id"`
	Phase           string `json:"phase"`
	PendingGateKind string `json:"pending_gate_kind,omitempty"`
}

// ListRunsResponse is the body of GET /runs.
type ListRunsResponse struct {
	Runs []RunSummary `json:"runs"`
}

// ResolveGateRequest is the body of POST /runs/{id}/approve|reject|resolve.
type ResolveGateRequest struct {
	By string `json:"by,omitempty"` // who resolved it; defaults to "cli"
	// Action is the optional typed resolution action (e.g. proceed, retry,
	// drop_branch, takeover, abort). Empty preserves the default decision
	// semantics (approve=proceed / reject=abort). Validated against the gate
	// kind at the API boundary.
	Action string `json:"action,omitempty"`
	// Decision selects approve vs reject for the POST /resolve route, which is
	// neither a plain approve nor reject. Ignored by /approve and /reject.
	Decision string `json:"decision,omitempty"`
}

// ResolveGateResponse is the body of a successful gate resolution.
type ResolveGateResponse struct {
	RunID  string `json:"run_id"`
	GateID string `json:"gate_id"`
	Status string `json:"status"` // "approved" | "rejected"
}

// KillResponse is the body of a successful POST /runs/{id}/kill.
type KillResponse struct {
	RunID string `json:"run_id"`
	Phase string `json:"phase"` // "killed"
}

// WorkerTaskResponse is the body of GET /workers/{id}/task (the wrap.read_task tool).
type WorkerTaskResponse struct {
	TaskID      string `json:"task_id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// SiblingTasksResponse is the body of GET /workers/{id}/siblings (wrap.list_sibling_tasks).
type SiblingTasksResponse struct {
	Titles []string `json:"titles"`
}

// WorkerReportRequest is the body of POST /workers/{id}/progress|done|blocked|plan.
// Each endpoint reads the field(s) it cares about.
type WorkerReportRequest struct {
	Msg       string `json:"msg,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Reason    string `json:"reason,omitempty"`
	PlanMD    string `json:"plan_md,omitempty"`
	TasksJSON string `json:"tasks_json,omitempty"`
}
