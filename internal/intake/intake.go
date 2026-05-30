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
	GatesJSON           string `json:"gates_json,omitempty"`            // optional; daemon falls back to project default
	VerificationCommand string `json:"verification_command,omitempty"`  // optional; only used when project is being created
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
	RunID     string `json:"run_id"`
	ProjectID string `json:"project_id"`
	Phase     string `json:"phase"`
	PlanMD    string `json:"plan_md,omitempty"`
	TasksJSON string `json:"tasks_json,omitempty"`
}
