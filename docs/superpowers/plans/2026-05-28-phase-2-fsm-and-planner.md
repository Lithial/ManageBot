# wrap Phase 2: FSM + Planner Phase Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Drive a run autonomously from `pending → planning → plan_gate`. The daemon spawns a planner subprocess in an isolated git worktree, the planner reports a plan via a small NDJSON worker-RPC protocol on its stdout, the daemon persists the plan and advances the run to `plan_gate`. No actual gate evaluation yet (Phase 5); no worker phase yet (Phase 3); no real MCP wire protocol yet (Phase 9). The `fake-claude` shim, extended with a script-driven mode, stands in for `claude -p`.

**Architecture:**

- `internal/fsm` — pure transition function over `Phase`. No I/O. Table-driven tests cover every transition (positive + negative).
- `internal/worktree` — thin wrapper over `git worktree add/remove` (`os/exec`).
- `internal/workerrpc` — NDJSON message types and a streaming decoder. Method names mirror the eventual MCP tool surface (`report_progress`, `report_plan`, `report_done`) so swapping in real MCP later is a transport change, not an API change.
- `internal/supervisor` — spawns a single subprocess, pipes a payload to its stdin, collects all `workerrpc.Message`s from its stdout until the process exits, returns an `Outcome`.
- `internal/orchestrator` — long-running goroutine in `wrapd`. Polls the DB for runs in `pending`, advances them through `planning` → `plan_gate` by composing worktree + supervisor + store.
- `cmd/fake-claude` — extended with a `FAKE_CLAUDE_SCRIPT` mode that reads a JSON-line script and emits `workerrpc` messages on stdout interleaved with sleeps/stderr/exit.

**Layering rules preserved:**

- `internal/store` remains the only package importing `database/sql`.
- FSM is pure (no DB, no `os/exec`). Worktree, supervisor, workerrpc are each independent and unit-testable.
- `internal/orchestrator` is the only place that knows about all of FSM, store, worktree, supervisor, workerrpc.

**Tech Stack:** Go 1.25, stdlib only (no new dependencies). Existing deps unchanged: `modernc.org/sqlite`, `github.com/oklog/ulid/v2`.

**Spec reference:** `docs/superpowers/specs/2026-05-26-claude-swarm-wrapper-design.md`, Phase 2 of the "Open questions for the implementation plan" list. Phase 1 plan: `docs/superpowers/plans/2026-05-26-phase-1-skeleton.md`.

---

## File structure produced by this plan

```
/home/lithial/coding/wrap/
├── cmd/
│   └── fake-claude/main.go              # MODIFIED — add FAKE_CLAUDE_SCRIPT mode
├── internal/
│   ├── fsm/
│   │   ├── fsm.go                       # NEW — Phase constants + Advance()
│   │   └── fsm_test.go                  # NEW
│   ├── worktree/
│   │   ├── worktree.go                  # NEW — Manager{} with Add/Remove
│   │   └── worktree_test.go             # NEW
│   ├── workerrpc/
│   │   ├── workerrpc.go                 # NEW — Message types + Decoder
│   │   └── workerrpc_test.go            # NEW
│   ├── supervisor/
│   │   ├── supervisor.go                # NEW — Run() spawns + collects
│   │   └── supervisor_test.go           # NEW
│   ├── orchestrator/
│   │   ├── orchestrator.go              # NEW — Orchestrator{} + RunOnce()
│   │   ├── planner.go                   # NEW — drivePlanner() logic
│   │   └── orchestrator_test.go         # NEW
│   ├── store/
│   │   ├── runs.go                      # MODIFIED — UpdateRunPhase, ListRunsByPhase
│   │   ├── runs_test.go                 # MODIFIED
│   │   ├── plans.go                     # NEW — InsertPlan, GetPlanByRun
│   │   └── plans_test.go                # NEW
│   ├── api/
│   │   ├── handlers.go                  # MODIFIED — add GET /runs/{id}
│   │   └── handlers_test.go             # NEW (file does not currently exist)
│   ├── client/
│   │   ├── client.go                    # MODIFIED — add GetRun()
│   │   └── client_test.go               # MODIFIED
│   └── intake/
│       └── intake.go                    # MODIFIED — add GetRunResponse DTO
├── cmd/wrapd/main.go                    # MODIFIED — start orchestrator goroutine
└── test/integration/
    └── planner_test.go                  # NEW — end-to-end Phase 2 happy path
```

**Worker-RPC protocol summary (defined once here to keep tasks consistent):**

NDJSON over the worker's stdout. Each line is a JSON object with a `method` field.

| Method            | Phase  | Direction      | Params                                     |
| ----------------- | ------ | -------------- | ------------------------------------------ |
| `report_progress` | any    | worker→daemon  | `{"msg": string}`                          |
| `report_plan`     | plan   | worker→daemon  | `{"plan_md": string, "tasks_json": string}` |
| `report_done`     | worker | worker→daemon  | `{"summary": string}` *(Phase 3, not used in Phase 2)* |

Phase 2 uses `report_progress` and `report_plan` only.

---

## Task 1: FSM module (pure transition function)

**Files:**
- Create: `/home/lithial/coding/wrap/internal/fsm/fsm.go`
- Test:   `/home/lithial/coding/wrap/internal/fsm/fsm_test.go`

- [ ] **Step 1: Write failing test for phase constants and Advance() happy path**

Create `/home/lithial/coding/wrap/internal/fsm/fsm_test.go`:

```go
package fsm_test

import (
	"testing"

	"github.com/Lithial/ManageBot/internal/fsm"
)

func TestPhaseConstants(t *testing.T) {
	// Round-trip every defined phase through string form.
	phases := []fsm.Phase{
		fsm.PhasePending,
		fsm.PhasePlanning,
		fsm.PhasePlanGate,
		fsm.PhaseWorking,
		fsm.PhaseMerging,
		fsm.PhaseMergeGate,
		fsm.PhaseDone,
		fsm.PhaseFailed,
		fsm.PhaseKilled,
	}
	for _, p := range phases {
		got, err := fsm.ParsePhase(string(p))
		if err != nil {
			t.Errorf("ParsePhase(%q) error: %v", p, err)
			continue
		}
		if got != p {
			t.Errorf("ParsePhase(%q) = %q, want %q", p, got, p)
		}
	}
}

func TestParsePhaseUnknown(t *testing.T) {
	_, err := fsm.ParsePhase("not-a-phase")
	if err == nil {
		t.Fatal("ParsePhase(unknown): want error, got nil")
	}
}

func TestAdvanceTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    fsm.Phase
		event   fsm.Event
		want    fsm.Phase
		wantErr bool
	}{
		{"pending->planning on plan_start", fsm.PhasePending, fsm.EventPlanStart, fsm.PhasePlanning, false},
		{"planning->plan_gate on plan_done", fsm.PhasePlanning, fsm.EventPlanDone, fsm.PhasePlanGate, false},
		{"planning->failed on plan_failed", fsm.PhasePlanning, fsm.EventPlanFailed, fsm.PhaseFailed, false},
		{"kill from any non-terminal", fsm.PhasePlanning, fsm.EventKill, fsm.PhaseKilled, false},
		{"kill from pending", fsm.PhasePending, fsm.EventKill, fsm.PhaseKilled, false},
		{"invalid: done->planning", fsm.PhaseDone, fsm.EventPlanStart, "", true},
		{"invalid: planning->done", fsm.PhasePlanning, fsm.EventPlanDone, fsm.PhasePlanGate, false}, // sanity: done is two hops away
		{"invalid: pending->plan_gate", fsm.PhasePending, fsm.EventPlanDone, "", true},
		{"invalid: kill from done", fsm.PhaseDone, fsm.EventKill, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fsm.Advance(tt.from, tt.event)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Advance(%q, %q): want error, got %q", tt.from, tt.event, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Advance(%q, %q) error: %v", tt.from, tt.event, err)
			}
			if got != tt.want {
				t.Errorf("Advance(%q, %q) = %q, want %q", tt.from, tt.event, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test, expect compile failure**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/fsm/...
```
Expected: `package github.com/Lithial/ManageBot/internal/fsm: no Go files` or build error referencing missing types.

- [ ] **Step 3: Implement `fsm.go` to satisfy the tests**

Create `/home/lithial/coding/wrap/internal/fsm/fsm.go`:

```go
// Package fsm defines the run phase state machine. It is pure: no I/O,
// no DB. The orchestrator composes fsm with the store to persist transitions.
package fsm

import "fmt"

type Phase string

const (
	PhasePending   Phase = "pending"
	PhasePlanning  Phase = "planning"
	PhasePlanGate  Phase = "plan_gate"
	PhaseWorking   Phase = "working"
	PhaseMerging   Phase = "merging"
	PhaseMergeGate Phase = "merge_gate"
	PhaseDone      Phase = "done"
	PhaseFailed    Phase = "failed"
	PhaseKilled    Phase = "killed"
)

type Event string

const (
	EventPlanStart   Event = "plan_start"
	EventPlanDone    Event = "plan_done"
	EventPlanFailed  Event = "plan_failed"
	EventWorkStart   Event = "work_start"   // Phase 3
	EventWorkDone    Event = "work_done"    // Phase 3
	EventWorkFailed  Event = "work_failed"  // Phase 3
	EventMergeStart  Event = "merge_start"  // Phase 4
	EventMergeDone   Event = "merge_done"   // Phase 4
	EventMergeFailed Event = "merge_failed" // Phase 4
	EventGateApprove Event = "gate_approve" // Phase 5
	EventGateReject  Event = "gate_reject"  // Phase 5
	EventKill        Event = "kill"
)

var validPhases = map[Phase]struct{}{
	PhasePending: {}, PhasePlanning: {}, PhasePlanGate: {},
	PhaseWorking: {}, PhaseMerging: {}, PhaseMergeGate: {},
	PhaseDone: {}, PhaseFailed: {}, PhaseKilled: {},
}

// ParsePhase converts a stored string back to a Phase, returning an error
// if the value is not a known phase.
func ParsePhase(s string) (Phase, error) {
	p := Phase(s)
	if _, ok := validPhases[p]; !ok {
		return "", fmt.Errorf("unknown phase %q", s)
	}
	return p, nil
}

// terminal phases never transition out.
var terminalPhases = map[Phase]struct{}{
	PhaseDone:   {},
	PhaseFailed: {},
	PhaseKilled: {},
}

// transitions[from][event] = to
var transitions = map[Phase]map[Event]Phase{
	PhasePending: {
		EventPlanStart: PhasePlanning,
	},
	PhasePlanning: {
		EventPlanDone:   PhasePlanGate,
		EventPlanFailed: PhaseFailed,
	},
	// Phases 3-5 will fill in the rest. Listed here for the kill-from-any rule.
	PhasePlanGate:  {},
	PhaseWorking:   {},
	PhaseMerging:   {},
	PhaseMergeGate: {},
}

// Advance returns the new phase after applying event to from. It returns
// an error if the transition is not defined. Kill is allowed from any
// non-terminal phase.
func Advance(from Phase, event Event) (Phase, error) {
	if _, terminal := terminalPhases[from]; terminal {
		return "", fmt.Errorf("cannot advance from terminal phase %q", from)
	}
	if event == EventKill {
		return PhaseKilled, nil
	}
	row, ok := transitions[from]
	if !ok {
		return "", fmt.Errorf("no transitions defined from %q", from)
	}
	to, ok := row[event]
	if !ok {
		return "", fmt.Errorf("invalid event %q from phase %q", event, from)
	}
	return to, nil
}
```

- [ ] **Step 4: Run the test, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/fsm/... -v
```
Expected: all subtests pass.

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/fsm/
git commit -m "feat(fsm): add pure phase state machine for run lifecycle"
```

---

## Task 2: Store extensions — UpdateRunPhase, ListRunsByPhase, plans table helpers

**Files:**
- Modify: `/home/lithial/coding/wrap/internal/store/runs.go`
- Modify: `/home/lithial/coding/wrap/internal/store/runs_test.go`
- Create: `/home/lithial/coding/wrap/internal/store/plans.go`
- Create: `/home/lithial/coding/wrap/internal/store/plans_test.go`

- [ ] **Step 1: Add a shared test helper for opening a temp store**

The existing tests duplicate `store.Open(context.Background(), filepath.Join(t.TempDir(), "wrap.db"))` plus cleanup. Phase 2 adds several store tests, so introduce a helper now. Create `/home/lithial/coding/wrap/internal/store/testing_helpers_test.go`:

```go
package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

// openTempStore returns a store backed by a fresh temp DB, with cleanup
// registered via t.Cleanup. Used by all store tests that need to mutate.
func openTempStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "wrap.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
```

- [ ] **Step 2: Write failing test for UpdateRunPhase**

Append to `/home/lithial/coding/wrap/internal/store/runs_test.go`. The file already has `package store_test` and imports `context`, `testing`, and `github.com/Lithial/ManageBot/internal/store`. Add `"errors"` to its import block. Then append:

```go
func TestUpdateRunPhase(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, err := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	rid, err := s.InsertRun(ctx, store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateRunPhase(ctx, rid, "planning"); err != nil {
		t.Fatalf("UpdateRunPhase: %v", err)
	}
	got, err := s.GetRun(ctx, rid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "planning" {
		t.Errorf("phase = %q, want %q", got.Phase, "planning")
	}
	if got.UpdatedAt < got.CreatedAt {
		t.Errorf("UpdatedAt=%d should be >= CreatedAt=%d", got.UpdatedAt, got.CreatedAt)
	}
}

func TestUpdateRunPhase_unknownRun(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	err := s.UpdateRunPhase(ctx, "no-such-id", "planning")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListRunsByPhase(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, _ := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
	})
	r1, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "a", GatesJSON: "{}"})
	r2, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "b", GatesJSON: "{}"})
	_ = s.UpdateRunPhase(ctx, r2, "planning")

	pending, err := s.ListRunsByPhase(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != r1 {
		t.Errorf("pending runs = %+v, want [%s]", pending, r1)
	}
}

func TestGetProjectByID(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, err := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
		VerificationCommand: "make test",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject(ctx, pid)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ID != pid || got.Name != "p" || got.RepoPath != "/tmp/repo" || got.VerificationCommand != "make test" {
		t.Errorf("project mismatch: got %+v", got)
	}
}

func TestGetProjectByID_notFound(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	if _, err := s.GetProject(ctx, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 3: Run the test, expect compile failure**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/store/... -run 'UpdateRunPhase|ListRunsByPhase|GetProject'
```
Expected: build error — `s.UpdateRunPhase undefined`, `s.ListRunsByPhase undefined`, `s.GetProject undefined`.

- [ ] **Step 4: Implement UpdateRunPhase, ListRunsByPhase, and GetProject**

Append to `/home/lithial/coding/wrap/internal/store/runs.go`:

```go
// UpdateRunPhase sets the phase of run `id` and bumps updated_at. Returns
// ErrNotFound if no row matches.
func (s *Store) UpdateRunPhase(ctx context.Context, id string, phase string) error {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE runs SET phase = ?, updated_at = ? WHERE id = ?
	`, phase, now, id)
	if err != nil {
		return fmt.Errorf("update run phase: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update run phase rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update run %q phase: %w", id, ErrNotFound)
	}
	return nil
}

// ListRunsByPhase returns all runs currently in the given phase, ordered by
// created_at ascending so the oldest pending run is picked up first.
func (s *Store) ListRunsByPhase(ctx context.Context, phase string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, intake_kind, COALESCE(intake_ref, ''), spec_md, gates_json, phase, created_at, updated_at
		FROM runs WHERE phase = ? ORDER BY created_at ASC
	`, phase)
	if err != nil {
		return nil, fmt.Errorf("list runs by phase: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.IntakeKind, &r.IntakeRef, &r.SpecMD, &r.GatesJSON, &r.Phase, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// GetProject returns a project by id. Companion to ProjectByName.
func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	var verCmd sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, repo_path, default_gates_json, verification_command, created_at
		FROM projects WHERE id = ?
	`, id).Scan(&p.ID, &p.Name, &p.RepoPath, &p.DefaultGatesJSON, &verCmd, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, fmt.Errorf("get project %q: %w", id, ErrNotFound)
		}
		return Project{}, fmt.Errorf("get project %q: %w", id, err)
	}
	p.VerificationCommand = verCmd.String
	return p, nil
}
```

- [ ] **Step 5: Run the test, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/store/... -run 'UpdateRunPhase|ListRunsByPhase|GetProject' -v
```
Expected: PASS.

- [ ] **Step 6: Write failing test for plans table helpers**

Create `/home/lithial/coding/wrap/internal/store/plans_test.go`:

```go
package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func TestInsertAndGetPlan(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, _ := s.InsertProject(ctx, store.Project{
		Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}",
	})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})

	pl := store.Plan{
		RunID:     rid,
		PlanMD:    "# Plan",
		TasksJSON: `[{"id":"t1","title":"do thing"}]`,
	}
	pid2, err := s.InsertPlan(ctx, pl)
	if err != nil {
		t.Fatalf("InsertPlan: %v", err)
	}
	if pid2 == "" {
		t.Fatal("InsertPlan returned empty id")
	}

	got, err := s.GetPlanByRun(ctx, rid)
	if err != nil {
		t.Fatalf("GetPlanByRun: %v", err)
	}
	if got.PlanMD != pl.PlanMD || got.TasksJSON != pl.TasksJSON || got.RunID != rid {
		t.Errorf("plan mismatch: got %+v want %+v", got, pl)
	}
}

func TestGetPlanByRun_notFound(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	_, err := s.GetPlanByRun(ctx, "no-such-run")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 7: Run the test, expect compile failure**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/store/... -run 'Plan'
```
Expected: build error — `store.Plan` undefined, `InsertPlan` undefined, `GetPlanByRun` undefined.

- [ ] **Step 8: Implement plans helpers**

Create `/home/lithial/coding/wrap/internal/store/plans.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Lithial/ManageBot/internal/ids"
)

type Plan struct {
	ID         string
	RunID      string
	PlanMD     string
	TasksJSON  string
	ApprovedAt int64 // 0 = not approved
	CreatedAt  int64
}

// InsertPlan persists a plan and returns its id.
func (s *Store) InsertPlan(ctx context.Context, p Plan) (string, error) {
	id := p.ID
	if id == "" {
		id = ids.New()
	}
	now := time.Now().Unix()
	var approved sql.NullInt64
	if p.ApprovedAt != 0 {
		approved = sql.NullInt64{Int64: p.ApprovedAt, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO plans (id, run_id, plan_md, tasks_json, approved_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, p.RunID, p.PlanMD, p.TasksJSON, approved, now)
	if err != nil {
		return "", fmt.Errorf("insert plan: %w", err)
	}
	return id, nil
}

// GetPlanByRun returns the single plan for run `runID`. Returns ErrNotFound
// if no plan has been persisted yet. (Phase 2 produces at most one plan per run.)
func (s *Store) GetPlanByRun(ctx context.Context, runID string) (Plan, error) {
	var p Plan
	var approved sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, plan_md, tasks_json, approved_at, created_at
		FROM plans WHERE run_id = ? ORDER BY created_at DESC LIMIT 1
	`, runID).Scan(&p.ID, &p.RunID, &p.PlanMD, &p.TasksJSON, &approved, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Plan{}, fmt.Errorf("get plan for run %q: %w", runID, ErrNotFound)
		}
		return Plan{}, fmt.Errorf("get plan for run %q: %w", runID, err)
	}
	p.ApprovedAt = approved.Int64
	return p, nil
}
```

- [ ] **Step 9: Run all store tests, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/store/... -v
```
Expected: all PASS.

- [ ] **Step 10: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/store/
git commit -m "feat(store): add UpdateRunPhase, ListRunsByPhase, GetProject, plan helpers"
```

---

## Task 3: Worktree manager (git plumbing)

**Files:**
- Create: `/home/lithial/coding/wrap/internal/worktree/worktree.go`
- Create: `/home/lithial/coding/wrap/internal/worktree/worktree_test.go`

This task uses real `git` subprocess calls against a temp repo. Skip the test if `git` is not on PATH.

- [ ] **Step 1: Write failing test**

Create `/home/lithial/coding/wrap/internal/worktree/worktree_test.go`:

```go
package worktree_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/worktree"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// initRepo creates a temp git repo with one commit so worktree add has a base.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestManager_AddRemove(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	stateDir := t.TempDir()
	m := worktree.NewManager(stateDir)
	ctx := context.Background()

	wt, err := m.Add(ctx, worktree.AddRequest{
		RepoPath: repo,
		Branch:   "wrap/run1/plan",
		BaseRef:  "main",
		Subpath:  "runs/run1/plan",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	wantPath := filepath.Join(stateDir, "runs/run1/plan")
	if wt.Path != wantPath {
		t.Errorf("Path = %q, want %q", wt.Path, wantPath)
	}
	if wt.Branch != "wrap/run1/plan" {
		t.Errorf("Branch = %q, want %q", wt.Branch, "wrap/run1/plan")
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "README")); err != nil {
		t.Errorf("README missing from worktree: %v", err)
	}

	if err := m.Remove(ctx, repo, wt.Path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path still exists after Remove: err=%v", err)
	}
}

func TestManager_Add_invalidRepo(t *testing.T) {
	requireGit(t)
	m := worktree.NewManager(t.TempDir())
	_, err := m.Add(context.Background(), worktree.AddRequest{
		RepoPath: "/nonexistent/repo",
		Branch:   "wrap/x/plan",
		BaseRef:  "main",
		Subpath:  "x",
	})
	if err == nil {
		t.Fatal("Add against nonexistent repo: want error, got nil")
	}
}
```

- [ ] **Step 2: Run the test, expect compile failure**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/worktree/...
```
Expected: build error — package does not exist.

- [ ] **Step 3: Implement Manager**

Create `/home/lithial/coding/wrap/internal/worktree/worktree.go`:

```go
// Package worktree wraps `git worktree` operations for the daemon.
// All paths are absolute; callers are responsible for choosing branch
// names and subpaths.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Manager creates worktrees rooted under a state directory (typically
// $WRAP_STATE_DIR, e.g. ~/.wrap). One Manager per daemon.
type Manager struct {
	stateDir string
}

func NewManager(stateDir string) *Manager {
	return &Manager{stateDir: stateDir}
}

// AddRequest names the worktree to create. RepoPath is the source repo;
// Branch is the new branch name (e.g. wrap/<run>/plan); BaseRef is the
// ref the new branch starts from; Subpath is appended to the manager's
// state dir to form the worktree's filesystem path.
type AddRequest struct {
	RepoPath string
	Branch   string
	BaseRef  string
	Subpath  string
}

type Worktree struct {
	Path   string
	Branch string
}

// Add runs `git worktree add -b <branch> <path> <baseRef>` and returns the
// resulting worktree. The parent directory of the worktree path is created
// as needed.
func (m *Manager) Add(ctx context.Context, req AddRequest) (Worktree, error) {
	if req.RepoPath == "" || req.Branch == "" || req.BaseRef == "" || req.Subpath == "" {
		return Worktree{}, fmt.Errorf("worktree.Add: RepoPath, Branch, BaseRef, Subpath are required")
	}
	wtPath := filepath.Join(m.stateDir, req.Subpath)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o700); err != nil {
		return Worktree{}, fmt.Errorf("mkdir parent: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", req.Branch, wtPath, req.BaseRef)
	cmd.Dir = req.RepoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	return Worktree{Path: wtPath, Branch: req.Branch}, nil
}

// Remove runs `git worktree remove --force <path>` against the given repo.
// Force is used because Phase 2 worktrees may have untracked planner output;
// we always want the directory cleaned on success or kill paths. Branches
// are NOT deleted here — Phase 6 `wrap prune` handles branch cleanup.
func (m *Manager) Remove(ctx context.Context, repoPath, wtPath string) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", wtPath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, out)
	}
	return nil
}
```

- [ ] **Step 4: Run the test, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/worktree/... -v
```
Expected: PASS (or SKIP if git is missing on the test machine).

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/worktree/
git commit -m "feat(worktree): add Manager wrapping git worktree add/remove"
```

---

## Task 4: Worker-RPC protocol (NDJSON message types + decoder)

**Files:**
- Create: `/home/lithial/coding/wrap/internal/workerrpc/workerrpc.go`
- Create: `/home/lithial/coding/wrap/internal/workerrpc/workerrpc_test.go`

- [ ] **Step 1: Write failing test**

Create `/home/lithial/coding/wrap/internal/workerrpc/workerrpc_test.go`:

```go
package workerrpc_test

import (
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/workerrpc"
)

func TestDecoder_progressAndPlan(t *testing.T) {
	input := strings.Join([]string{
		`{"method":"report_progress","params":{"msg":"starting"}}`,
		`{"method":"report_plan","params":{"plan_md":"# Plan","tasks_json":"[]"}}`,
		``, // trailing newline produces empty final line; decoder should ignore
	}, "\n")

	got, err := workerrpc.DecodeAll(strings.NewReader(input))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2: %+v", len(got), got)
	}
	if got[0].Method != "report_progress" {
		t.Errorf("got[0].Method = %q", got[0].Method)
	}
	prog, err := workerrpc.AsProgress(got[0])
	if err != nil {
		t.Fatalf("AsProgress: %v", err)
	}
	if prog.Msg != "starting" {
		t.Errorf("prog.Msg = %q", prog.Msg)
	}
	plan, err := workerrpc.AsPlan(got[1])
	if err != nil {
		t.Fatalf("AsPlan: %v", err)
	}
	if plan.PlanMD != "# Plan" || plan.TasksJSON != "[]" {
		t.Errorf("plan = %+v", plan)
	}
}

func TestDecoder_skipsNonJSONLines(t *testing.T) {
	// Real claude (and noisy shims) may interleave plain stdout text with
	// JSON-RPC. The decoder must skip non-JSON lines silently rather than fail.
	input := "starting up...\n" +
		`{"method":"report_progress","params":{"msg":"ok"}}` + "\n" +
		"done.\n"
	got, err := workerrpc.DecodeAll(strings.NewReader(input))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(got) != 1 || got[0].Method != "report_progress" {
		t.Errorf("got = %+v, want one report_progress", got)
	}
}

func TestDecoder_unknownMethodKept(t *testing.T) {
	// Unknown methods are returned to the caller as-is; it's the caller's
	// job to decide whether to ignore them (forward compatibility).
	input := `{"method":"future_thing","params":{}}` + "\n"
	got, err := workerrpc.DecodeAll(strings.NewReader(input))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(got) != 1 || got[0].Method != "future_thing" {
		t.Errorf("got = %+v", got)
	}
}

func TestAsProgress_wrongMethod(t *testing.T) {
	m := workerrpc.Message{Method: "report_plan"}
	if _, err := workerrpc.AsProgress(m); err == nil {
		t.Fatal("AsProgress on report_plan: want error, got nil")
	}
}
```

- [ ] **Step 2: Run the test, expect compile failure**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/workerrpc/...
```
Expected: package missing.

- [ ] **Step 3: Implement workerrpc**

Create `/home/lithial/coding/wrap/internal/workerrpc/workerrpc.go`:

```go
// Package workerrpc defines the small NDJSON protocol that worker
// subprocesses speak on their stdout to report progress and outcomes.
// Method names mirror the planned MCP tool surface so the transport can
// be swapped (stdio NDJSON → real MCP) without changing call sites.
package workerrpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Method names. Add new ones here; unknown methods are tolerated at decode time.
const (
	MethodReportProgress = "report_progress"
	MethodReportPlan     = "report_plan"
	MethodReportDone     = "report_done"     // Phase 3, worker shape
	MethodReportBlocked  = "report_blocked"  // Phase 3+
)

// Message is one decoded NDJSON line.
type Message struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Typed params for the methods Phase 2 uses.

type ProgressParams struct {
	Msg string `json:"msg"`
}

type PlanParams struct {
	PlanMD    string `json:"plan_md"`
	TasksJSON string `json:"tasks_json"`
}

// DecodeAll reads NDJSON from r until EOF and returns every JSON-object
// line that parses cleanly. Non-JSON lines (plain stdout chatter) are
// silently skipped. An I/O error other than EOF is returned.
func DecodeAll(r io.Reader) ([]Message, error) {
	var out []Message
	sc := bufio.NewScanner(r)
	// Worker output is bounded by Phase 2 protocols, but plans can be large.
	// Allow 1 MiB lines (default is 64 KiB).
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			continue // tolerant: looked like JSON but wasn't valid
		}
		if m.Method == "" {
			continue
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return out, nil
}

// AsProgress decodes a Message as a ProgressParams, returning an error if
// the method does not match.
func AsProgress(m Message) (ProgressParams, error) {
	if m.Method != MethodReportProgress {
		return ProgressParams{}, fmt.Errorf("AsProgress: method = %q", m.Method)
	}
	var p ProgressParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return ProgressParams{}, fmt.Errorf("AsProgress unmarshal: %w", err)
	}
	return p, nil
}

// AsPlan decodes a Message as a PlanParams.
func AsPlan(m Message) (PlanParams, error) {
	if m.Method != MethodReportPlan {
		return PlanParams{}, fmt.Errorf("AsPlan: method = %q", m.Method)
	}
	var p PlanParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return PlanParams{}, fmt.Errorf("AsPlan unmarshal: %w", err)
	}
	return p, nil
}
```

- [ ] **Step 4: Run the test, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/workerrpc/... -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/workerrpc/
git commit -m "feat(workerrpc): add NDJSON protocol mirroring MCP tool surface"
```

---

## Task 5: Supervisor (spawn + RPC collection)

**Files:**
- Create: `/home/lithial/coding/wrap/internal/supervisor/supervisor.go`
- Create: `/home/lithial/coding/wrap/internal/supervisor/supervisor_test.go`

The supervisor spawns a subprocess, writes a stdin payload (if any), drains stdout and parses RPC messages, captures stderr for forensics, waits for exit, and returns an Outcome.

- [ ] **Step 1: Write failing test (uses /bin/sh, no fake-claude dependency yet)**

Create `/home/lithial/coding/wrap/internal/supervisor/supervisor_test.go`:

```go
package supervisor_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/supervisor"
)

func TestRun_capturesRPCMessages(t *testing.T) {
	// Use a shell script that emits two RPC lines then exits 0.
	script := `printf '%s\n%s\n' '{"method":"report_progress","params":{"msg":"hi"}}' '{"method":"report_plan","params":{"plan_md":"# P","tasks_json":"[]"}}'`
	cmd := exec.Command("/bin/sh", "-c", script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := supervisor.Run(ctx, supervisor.Request{Cmd: cmd})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Method != "report_progress" {
		t.Errorf("Messages[0].Method = %q", out.Messages[0].Method)
	}
	if out.Messages[1].Method != "report_plan" {
		t.Errorf("Messages[1].Method = %q", out.Messages[1].Method)
	}
}

func TestRun_capturesStdinAndStderr(t *testing.T) {
	// Reads stdin, echoes to stderr, exits 7.
	script := `cat 1>&2; exit 7`
	cmd := exec.Command("/bin/sh", "-c", script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := supervisor.Run(ctx, supervisor.Request{
		Cmd:          cmd,
		StdinPayload: []byte("hello from spec\n"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", out.ExitCode)
	}
	if !strings.Contains(string(out.Stderr), "hello from spec") {
		t.Errorf("Stderr = %q, want to contain stdin payload", out.Stderr)
	}
}

func TestRun_contextCancelKillsProcess(t *testing.T) {
	// Long-sleeping subprocess; cancel context, expect non-zero exit quickly.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	out, err := supervisor.Run(ctx, supervisor.Request{Cmd: cmd})
	elapsed := time.Since(start)
	if err == nil && out.ExitCode == 0 {
		t.Fatal("expected non-zero exit when context cancelled")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Run took %v, expected sub-second after cancel", elapsed)
	}
}
```

- [ ] **Step 2: Run the test, expect compile failure**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/supervisor/...
```
Expected: package missing.

- [ ] **Step 3: Implement supervisor**

Create `/home/lithial/coding/wrap/internal/supervisor/supervisor.go`:

```go
// Package supervisor owns the lifecycle of one worker subprocess.
// It writes a stdin payload (if any), collects RPC messages from stdout,
// captures stderr for forensics, and returns when the process exits or
// the context is cancelled.
package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/Lithial/ManageBot/internal/workerrpc"
)

type Request struct {
	Cmd          *exec.Cmd
	StdinPayload []byte // optional; nil = no stdin
}

type Outcome struct {
	ExitCode int
	Messages []workerrpc.Message
	Stderr   []byte
}

// Run spawns the configured Cmd, writes StdinPayload to its stdin, drains
// stdout as workerrpc NDJSON, captures stderr to a buffer, and waits for
// the process to exit. If the context is cancelled, the process is killed.
// Returns an Outcome describing exit code, parsed messages, and stderr.
func Run(ctx context.Context, req Request) (Outcome, error) {
	cmd := req.Cmd
	if cmd == nil {
		return Outcome{}, fmt.Errorf("supervisor.Run: Cmd is required")
	}

	// Wire pipes.
	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		return Outcome{}, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	var stdinW io.WriteCloser
	if req.StdinPayload != nil {
		stdinW, err = cmd.StdinPipe()
		if err != nil {
			return Outcome{}, fmt.Errorf("stdin pipe: %w", err)
		}
	}

	if err := cmd.Start(); err != nil {
		return Outcome{}, fmt.Errorf("start: %w", err)
	}

	// Feed stdin.
	if stdinW != nil {
		go func() {
			_, _ = stdinW.Write(req.StdinPayload)
			_ = stdinW.Close()
		}()
	}

	// Drain stdout in a goroutine so context cancellation can race with reads.
	msgsCh := make(chan []workerrpc.Message, 1)
	decErrCh := make(chan error, 1)
	go func() {
		msgs, err := workerrpc.DecodeAll(stdoutR)
		msgsCh <- msgs
		decErrCh <- err
	}()

	// Wait for either context cancellation or process exit.
	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-doneCh:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		waitErr = <-doneCh
	}

	msgs := <-msgsCh
	_ = <-decErrCh // tolerated; partial parse is fine

	exit := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}

	return Outcome{
		ExitCode: exit,
		Messages: msgs,
		Stderr:   stderrBuf.Bytes(),
	}, nil
}
```

- [ ] **Step 4: Run the test, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/supervisor/... -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/supervisor/
git commit -m "feat(supervisor): add subprocess lifecycle with RPC collection"
```

---

## Task 6: Extend fake-claude with script mode

**Files:**
- Modify: `/home/lithial/coding/wrap/cmd/fake-claude/main.go`
- Create: `/home/lithial/coding/wrap/cmd/fake-claude/main_test.go`

A new env var `FAKE_CLAUDE_SCRIPT` points to a JSON-lines file. Each line is one action. When set, the shim ignores `FAKE_CLAUDE_STDOUT`/`FAKE_CLAUDE_STDERR`/`FAKE_CLAUDE_SLEEP_MS`/`FAKE_CLAUDE_EXIT_CODE` and executes the script instead.

Script action schema:

```json
{"kind":"progress","msg":"..."}                                          // emit report_progress
{"kind":"plan","plan_md":"...","tasks_json":"..."}                       // emit report_plan
{"kind":"stderr","text":"..."}                                           // write to stderr
{"kind":"sleep_ms","ms":50}                                              // sleep
{"kind":"exit","code":0}                                                 // exit with code; stops script
```

If the script reaches EOF without `exit`, the shim exits 0.

- [ ] **Step 1: Write failing test for script mode**

Create `/home/lithial/coding/wrap/cmd/fake-claude/main_test.go`:

```go
package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestFakeClaude_scriptEmitsRPC(t *testing.T) {
	bin, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude binary not built: %v (run `make fake-claude`)", err)
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.jsonl")
	lines := []string{
		`{"kind":"progress","msg":"starting"}`,
		`{"kind":"plan","plan_md":"# Plan","tasks_json":"[]"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(scriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Parse two NDJSON lines and assert their methods.
	var msgs []struct{ Method string }
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var m struct{ Method string }
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse line %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Method != "report_progress" || msgs[1].Method != "report_plan" {
		t.Errorf("methods: %+v", msgs)
	}
}

func TestFakeClaude_scriptCustomExit(t *testing.T) {
	bin, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude binary not built: %v", err)
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.jsonl")
	if err := os.WriteFile(scriptPath, []byte(`{"kind":"exit","code":3}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
	err = cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 3 {
		t.Fatalf("expected exit 3, got err=%v", err)
	}
}
```

- [ ] **Step 2: Build fake-claude and run the test, expect failure**

Run:
```bash
cd /home/lithial/coding/wrap && make fake-claude && go test ./cmd/fake-claude/... -v
```
Expected: the binary builds, tests fail because script mode is not implemented (the shim ignores `FAKE_CLAUDE_SCRIPT` and uses old env-driven behavior).

- [ ] **Step 3: Extend fake-claude to support script mode**

Replace `/home/lithial/coding/wrap/cmd/fake-claude/main.go` with:

```go
// fake-claude is an env-driven stand-in for `claude -p` used in wrap's
// integration tests.
//
// Modes:
//   FAKE_CLAUDE_SCRIPT=<path>    Script mode: read JSONL actions from the
//                                file and execute them in order. Other
//                                env vars are ignored in this mode.
//   else                         Legacy mode: simple stdout/stderr/sleep/exit
//                                driven by the FAKE_CLAUDE_* vars below.
//
// Script actions (one JSON object per line):
//   {"kind":"progress","msg":"..."}
//   {"kind":"plan","plan_md":"...","tasks_json":"..."}
//   {"kind":"stderr","text":"..."}
//   {"kind":"sleep_ms","ms":N}
//   {"kind":"exit","code":N}
//
// Legacy env vars:
//   FAKE_CLAUDE_EXIT_CODE   integer exit code (default 0)
//   FAKE_CLAUDE_SLEEP_MS    pre-exit sleep
//   FAKE_CLAUDE_STDOUT      string to print to stdout
//   FAKE_CLAUDE_STDERR      string to print to stderr
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

type action struct {
	Kind      string `json:"kind"`
	Msg       string `json:"msg,omitempty"`
	PlanMD    string `json:"plan_md,omitempty"`
	TasksJSON string `json:"tasks_json,omitempty"`
	Text      string `json:"text,omitempty"`
	Ms        int    `json:"ms,omitempty"`
	Code      int    `json:"code,omitempty"`
}

func main() {
	if script := os.Getenv("FAKE_CLAUDE_SCRIPT"); script != "" {
		os.Exit(runScript(script))
	}
	os.Exit(runLegacy())
}

func runScript(path string) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-claude: open script: %v\n", err)
		return 1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var a action
		if err := json.Unmarshal(line, &a); err != nil {
			fmt.Fprintf(os.Stderr, "fake-claude: bad action line %q: %v\n", line, err)
			return 1
		}
		switch a.Kind {
		case "progress":
			emitJSON(out, map[string]any{
				"method": "report_progress",
				"params": map[string]any{"msg": a.Msg},
			})
		case "plan":
			emitJSON(out, map[string]any{
				"method": "report_plan",
				"params": map[string]any{
					"plan_md":    a.PlanMD,
					"tasks_json": a.TasksJSON,
				},
			})
		case "stderr":
			fmt.Fprint(os.Stderr, a.Text)
		case "sleep_ms":
			if a.Ms > 0 {
				_ = out.Flush()
				time.Sleep(time.Duration(a.Ms) * time.Millisecond)
			}
		case "exit":
			_ = out.Flush()
			return a.Code
		default:
			fmt.Fprintf(os.Stderr, "fake-claude: unknown action kind %q\n", a.Kind)
			return 1
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "fake-claude: scan: %v\n", err)
		return 1
	}
	return 0
}

func emitJSON(w *bufio.Writer, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
	_ = w.WriteByte('\n')
	_ = w.Flush()
}

func runLegacy() int {
	if s := os.Getenv("FAKE_CLAUDE_SLEEP_MS"); s != "" {
		if ms, err := strconv.Atoi(s); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	if s := os.Getenv("FAKE_CLAUDE_STDOUT"); s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := os.Getenv("FAKE_CLAUDE_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	code := 0
	if s := os.Getenv("FAKE_CLAUDE_EXIT_CODE"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			code = n
		}
	}
	return code
}
```

- [ ] **Step 4: Rebuild and run, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && make fake-claude && go test ./cmd/fake-claude/... -v
```
Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add cmd/fake-claude/
git commit -m "feat(fake-claude): add scripted mode emitting workerrpc messages"
```

---

## Task 7: Orchestrator — drive the planner phase

**Files:**
- Create: `/home/lithial/coding/wrap/internal/orchestrator/orchestrator.go`
- Create: `/home/lithial/coding/wrap/internal/orchestrator/planner.go`
- Create: `/home/lithial/coding/wrap/internal/orchestrator/orchestrator_test.go`

The orchestrator owns a `tick` method that:
1. Lists runs in `pending`, advances each to `planning` (FSM event `plan_start`), then spawns a planner.
2. For each planner, on success persists the plan and advances to `plan_gate` (event `plan_done`).
3. On failure advances to `failed` (event `plan_failed`).

The orchestrator takes a `WorkerCommand` function so tests can inject `fake-claude` instead of `claude`.

- [ ] **Step 1: Write failing test (using fake-claude script)**

Create `/home/lithial/coding/wrap/internal/orchestrator/orchestrator_test.go`:

```go
package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

// makeRepo creates a temp git repo with one commit and returns its path.
func makeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestTick_pendingToPlanGate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v", err)
	}

	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	repo := makeRepo(t)
	pid, _ := st.InsertProject(context.Background(), store.Project{
		Name: "proj", RepoPath: repo, DefaultGatesJSON: "{}",
	})
	rid, _ := st.InsertRun(context.Background(), store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "build a thing", GatesJSON: "{}",
	})

	// Script: emit a plan and exit 0.
	scriptPath := filepath.Join(stateDir, "planner.jsonl")
	lines := []string{
		`{"kind":"progress","msg":"thinking"}`,
		`{"kind":"plan","plan_md":"# Plan\n- step","tasks_json":"[{\"id\":\"t1\",\"title\":\"do it\"}]"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(scriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmdFactory := func(spec string) *exec.Cmd {
		c := exec.Command(fakeClaude)
		c.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
		return c
	}
	o := orchestrator.New(orchestrator.Config{
		Store:         st,
		StateDir:      stateDir,
		PlannerCmd:    cmdFactory,
		StepTimeout:   10 * time.Second,
	})

	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.GetRun(context.Background(), rid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "plan_gate" {
		t.Fatalf("phase = %q, want plan_gate", got.Phase)
	}
	plan, err := st.GetPlanByRun(context.Background(), rid)
	if err != nil {
		t.Fatalf("GetPlanByRun: %v", err)
	}
	if !strings.Contains(plan.PlanMD, "# Plan") {
		t.Errorf("plan_md = %q", plan.PlanMD)
	}
	var tasks []map[string]string
	if err := json.Unmarshal([]byte(plan.TasksJSON), &tasks); err != nil {
		t.Errorf("tasks_json parse: %v", err)
	}
	if len(tasks) != 1 || tasks[0]["id"] != "t1" {
		t.Errorf("tasks = %+v", tasks)
	}
}

func TestTick_plannerExitNonZero_failsRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v", err)
	}

	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	repo := makeRepo(t)
	pid, _ := st.InsertProject(context.Background(), store.Project{
		Name: "proj", RepoPath: repo, DefaultGatesJSON: "{}",
	})
	rid, _ := st.InsertRun(context.Background(), store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "x", GatesJSON: "{}",
	})

	scriptPath := filepath.Join(stateDir, "fail.jsonl")
	if err := os.WriteFile(scriptPath, []byte(`{"kind":"exit","code":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdFactory := func(spec string) *exec.Cmd {
		c := exec.Command(fakeClaude)
		c.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
		return c
	}
	o := orchestrator.New(orchestrator.Config{
		Store:       st,
		StateDir:    stateDir,
		PlannerCmd:  cmdFactory,
		StepTimeout: 10 * time.Second,
	})
	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "failed" {
		t.Errorf("phase = %q, want failed", got.Phase)
	}
}
```

- [ ] **Step 2: Build fake-claude and run the test, expect compile failure**

Run:
```bash
cd /home/lithial/coding/wrap && make fake-claude && go test ./internal/orchestrator/...
```
Expected: `package github.com/Lithial/ManageBot/internal/orchestrator: no Go files`.

- [ ] **Step 3: Implement orchestrator skeleton**

Create `/home/lithial/coding/wrap/internal/orchestrator/orchestrator.go`:

```go
// Package orchestrator drives runs through the phase state machine by
// composing the FSM, store, worktree manager, and supervisor. Phase 2
// implements only the planner phase: pending → planning → plan_gate.
package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"time"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/worktree"
)

// PlannerCmdFunc returns a freshly-configured *exec.Cmd for the planner
// subprocess. It is called once per run; the spec markdown is passed in
// so callers can wire it into the command (e.g. via env or args) if they
// don't want it on stdin. Phase 2 production wiring passes spec on stdin
// via supervisor.Request.StdinPayload, so this func only configures the
// program path, env, and args.
type PlannerCmdFunc func(specMD string) *exec.Cmd

type Config struct {
	Store       *store.Store
	StateDir    string         // root for worktrees (e.g. ~/.wrap)
	PlannerCmd  PlannerCmdFunc // factory for the planner subprocess
	StepTimeout time.Duration  // per-step timeout for planner subprocess
}

type Orchestrator struct {
	cfg  Config
	wt   *worktree.Manager
}

func New(cfg Config) *Orchestrator {
	return &Orchestrator{
		cfg: cfg,
		wt:  worktree.NewManager(cfg.StateDir),
	}
}

// Tick runs one orchestration pass: advance every pending run by one
// planner phase. Errors on individual runs are logged but do not stop
// other runs in the same pass.
func (o *Orchestrator) Tick(ctx context.Context) error {
	pending, err := o.cfg.Store.ListRunsByPhase(ctx, string(fsm.PhasePending))
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	for _, r := range pending {
		if err := o.drivePlanner(ctx, r); err != nil {
			log.Printf("orchestrator: run %s planner: %v", r.ID, err)
		}
	}
	return nil
}

// Run loops Tick on interval until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context, interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		if err := o.Tick(ctx); err != nil {
			log.Printf("orchestrator: tick: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}
```

Create `/home/lithial/coding/wrap/internal/orchestrator/planner.go`:

```go
package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Lithial/ManageBot/internal/fsm"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/supervisor"
	"github.com/Lithial/ManageBot/internal/worktree"
	"github.com/Lithial/ManageBot/internal/workerrpc"
)

// drivePlanner advances a single run pending → planning → (plan_gate | failed).
// It is best-effort idempotent: if the worktree already exists from a prior
// failed attempt, this returns an error rather than retrying (Phase 8 will
// add retry budgeting).
func (o *Orchestrator) drivePlanner(ctx context.Context, r store.Run) error {
	// Pre-flight: load the project so we know the repo path.
	proj, err := o.cfg.Store.GetProject(ctx, r.ProjectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	// Advance pending → planning.
	next, err := fsm.Advance(fsm.PhasePending, fsm.EventPlanStart)
	if err != nil {
		return fmt.Errorf("fsm: %w", err)
	}
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(next)); err != nil {
		return fmt.Errorf("update run phase planning: %w", err)
	}

	// Create the planner worktree.
	wt, err := o.wt.Add(ctx, worktree.AddRequest{
		RepoPath: proj.RepoPath,
		Branch:   fmt.Sprintf("wrap/%s/plan", r.ID),
		BaseRef:  "HEAD",
		Subpath:  filepath.Join("runs", r.ID, "plan"),
	})
	if err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("worktree add: %w", err)
	}
	defer func() {
		_ = o.wt.Remove(context.Background(), proj.RepoPath, wt.Path)
	}()

	// Spawn the planner.
	cmd := o.cfg.PlannerCmd(r.SpecMD)
	cmd.Dir = wt.Path

	stepCtx := ctx
	if o.cfg.StepTimeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, o.cfg.StepTimeout)
		defer cancel()
	}
	out, err := supervisor.Run(stepCtx, supervisor.Request{
		Cmd:          cmd,
		StdinPayload: []byte(r.SpecMD),
	})
	if err != nil {
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(fsm.PhaseFailed))
		return fmt.Errorf("supervise planner: %w", err)
	}

	// Find the plan message.
	var planMsg *workerrpc.PlanParams
	for _, m := range out.Messages {
		if m.Method == workerrpc.MethodReportPlan {
			p, perr := workerrpc.AsPlan(m)
			if perr != nil {
				continue
			}
			planMsg = &p
		}
	}

	if out.ExitCode != 0 || planMsg == nil {
		nextPhase, _ := fsm.Advance(fsm.PhasePlanning, fsm.EventPlanFailed)
		_ = o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(nextPhase))
		return fmt.Errorf("planner failed: exit=%d hasPlan=%v", out.ExitCode, planMsg != nil)
	}

	if _, err := o.cfg.Store.InsertPlan(ctx, store.Plan{
		RunID:     r.ID,
		PlanMD:    planMsg.PlanMD,
		TasksJSON: planMsg.TasksJSON,
	}); err != nil {
		return fmt.Errorf("insert plan: %w", err)
	}
	nextPhase, _ := fsm.Advance(fsm.PhasePlanning, fsm.EventPlanDone)
	if err := o.cfg.Store.UpdateRunPhase(ctx, r.ID, string(nextPhase)); err != nil {
		return fmt.Errorf("update run phase plan_gate: %w", err)
	}
	return nil
}
```


- [ ] **Step 4: Build fake-claude and run, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && make fake-claude && go test ./internal/orchestrator/... -v
```
Expected: both subtests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/orchestrator/
git commit -m "feat(orchestrator): drive pending->plan_gate via planner subprocess"
```

---

## Task 8: API `GET /runs/{id}` and client method

**Files:**
- Modify: `/home/lithial/coding/wrap/internal/intake/intake.go`
- Modify: `/home/lithial/coding/wrap/internal/api/handlers.go`
- Create: `/home/lithial/coding/wrap/internal/api/handlers_test.go`
- Modify: `/home/lithial/coding/wrap/internal/client/client.go`
- Modify: `/home/lithial/coding/wrap/internal/client/client_test.go`

This adds a read endpoint so the integration test and future CLI/TUI can inspect a run's current phase and (if present) its plan.

- [ ] **Step 1: Add DTO**

Append to `/home/lithial/coding/wrap/internal/intake/intake.go`:

```go
// GetRunResponse is the body of GET /runs/{id}.
type GetRunResponse struct {
	RunID     string `json:"run_id"`
	ProjectID string `json:"project_id"`
	Phase     string `json:"phase"`
	PlanMD    string `json:"plan_md,omitempty"`
	TasksJSON string `json:"tasks_json,omitempty"`
}
```

- [ ] **Step 2: Write failing API test**

Create `/home/lithial/coding/wrap/internal/api/handlers_test.go`:

```go
package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestGetRun_pendingHasNoPlan(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := newSocketClient(sock)

	// Submit a run first.
	body, _ := json.Marshal(intake.SubmitRunRequest{
		ProjectName: "p", RepoPath: "/tmp/x", IntakeKind: "cli", SpecMD: "spec",
	})
	resp, err := c.Post("http://wrap/runs", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	var submit intake.SubmitRunResponse
	_ = json.NewDecoder(resp.Body).Decode(&submit)
	resp.Body.Close()

	// Fetch it.
	resp2, err := c.Get("http://wrap/runs/" + submit.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("status %d: %s", resp2.StatusCode, raw)
	}
	var got intake.GetRunResponse
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != submit.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, submit.RunID)
	}
	if got.Phase != "pending" {
		t.Errorf("Phase = %q, want pending", got.Phase)
	}
	if got.PlanMD != "" {
		t.Errorf("PlanMD should be empty for pending run, got %q", got.PlanMD)
	}
}

func TestGetRun_notFound(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := newSocketClient(sock)
	resp, err := c.Get("http://wrap/runs/01ABCNOTFOUND")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func newSocketClient(sock string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
}
```

- [ ] **Step 3: Run the test, expect failure**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/api/... -run TestGetRun
```
Expected: 404 for the "pending has no plan" test (route not registered).

- [ ] **Step 4: Implement the handler**

In `/home/lithial/coding/wrap/internal/api/handlers.go`, register a new route in `registerRoutes`:

```go
	mux.HandleFunc("GET /runs/{id}", s.handleGetRun)
```

Append the handler:

```go
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
		RunID:     run.ID,
		ProjectID: run.ProjectID,
		Phase:     run.Phase,
	}
	plan, err := s.store.GetPlanByRun(ctx, id)
	if err == nil {
		out.PlanMD = plan.PlanMD
		out.TasksJSON = plan.TasksJSON
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("api: get plan: %v", err)
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 5: Run the test, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/api/... -v
```
Expected: PASS.

- [ ] **Step 6: Add client.GetRun**

Append to `/home/lithial/coding/wrap/internal/client/client.go`:

```go
func (c *Client) GetRun(ctx context.Context, id string) (intake.GetRunResponse, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://wrap/runs/"+id, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return intake.GetRunResponse{}, fmt.Errorf("get run: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return intake.GetRunResponse{}, fmt.Errorf("run %q: not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return intake.GetRunResponse{}, fmt.Errorf("get run: status %d: %s", resp.StatusCode, raw)
	}
	var out intake.GetRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return intake.GetRunResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}
```

Append to `/home/lithial/coding/wrap/internal/client/client_test.go`:

```go
func TestClientGetRun(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := client.New(sock)

	submit, err := c.SubmitRun(context.Background(), intake.SubmitRunRequest{
		ProjectName: "demo",
		RepoPath:    "/tmp/demo",
		IntakeKind:  "cli",
		SpecMD:      "# spec",
	})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}

	got, err := c.GetRun(context.Background(), submit.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.RunID != submit.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, submit.RunID)
	}
	if got.Phase != "pending" {
		t.Errorf("Phase = %q, want pending", got.Phase)
	}
	if got.PlanMD != "" {
		t.Errorf("PlanMD = %q, want empty for pending run", got.PlanMD)
	}
}

func TestClientGetRun_notFound(t *testing.T) {
	sock := testutil.StartInProcessServer(t)
	c := client.New(sock)
	if _, err := c.GetRun(context.Background(), "01ABCNOTFOUND"); err == nil {
		t.Fatal("GetRun for unknown id: want error, got nil")
	}
}
```

- [ ] **Step 7: Run all client + api tests**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./internal/api/... ./internal/client/... ./internal/intake/... -v
```
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/intake/ internal/api/ internal/client/
git commit -m "feat(api): add GET /runs/{id} returning phase + plan"
```

---

## Task 9: Wire orchestrator into wrapd; expose `--planner-cmd` flag

**Files:**
- Modify: `/home/lithial/coding/wrap/cmd/wrapd/main.go`

`wrapd` starts the orchestrator goroutine after the socket is ready. The planner subprocess command is configurable via `--planner-cmd` so production can point at `claude` and tests can point at `fake-claude` with a script env var.

For Phase 2 the command is taken verbatim from the flag — a single executable, no args. Phase 9 will introduce a richer template that includes `--mcp wrap --append-system-prompt …`.

- [ ] **Step 1: Modify wrapd main to start the orchestrator**

Replace `/home/lithial/coding/wrap/cmd/wrapd/main.go` with:

```go
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

	// Start orchestrator.
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
```

- [ ] **Step 2: Build and verify wrapd compiles**

Run:
```bash
cd /home/lithial/coding/wrap && make wrapd
```
Expected: builds without error to `./bin/wrapd`.

- [ ] **Step 3: Smoke-check wrapd starts and shuts down cleanly**

Run:
```bash
cd /home/lithial/coding/wrap && \
  WRAP_STATE_DIR="$(mktemp -d)" \
  WRAP_SOCKET="$(mktemp -u)" \
  timeout 2 ./bin/wrapd --planner-cmd /bin/true --tick-interval 100ms ; \
  echo "exit=$?"
```
Expected: prints `wrapd: listening on ...`, then exits with `exit=124` (timeout killed it) or `exit=143` (SIGTERM) — both are acceptable as long as no Go runtime error appears.

- [ ] **Step 4: Commit**

```bash
cd /home/lithial/coding/wrap
git add cmd/wrapd/main.go
git commit -m "feat(wrapd): start orchestrator goroutine and add --planner-cmd flag"
```

---

## Task 10: End-to-end integration test for Phase 2 happy path

**Files:**
- Create: `/home/lithial/coding/wrap/test/integration/planner_test.go`

This test spawns a real `wrapd` configured to use `fake-claude` as its planner, submits a run via the `wrap` CLI, waits for the orchestrator to drive it to `plan_gate`, and asserts the persisted plan.

- [ ] **Step 1: Write the integration test**

Create `/home/lithial/coding/wrap/test/integration/planner_test.go`:

```go
//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestPlannerHappyPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	wrapdBin, err := testutil.LocateBinary("wrapd")
	if err != nil {
		t.Fatalf("locate wrapd: %v (did you run `make wrapd`?)", err)
	}
	wrapBin, err := testutil.LocateBinary("wrap")
	if err != nil {
		t.Fatalf("locate wrap: %v (did you run `make wrap`?)", err)
	}
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Fatalf("locate fake-claude: %v (did you run `make fake-claude`?)", err)
	}

	// Build a temp repo and planner script.
	repo := makeIntegrationRepo(t)
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# spec\nbuild a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(t.TempDir(), "planner.jsonl")
	lines := []string{
		`{"kind":"progress","msg":"planning"}`,
		`{"kind":"plan","plan_md":"# Plan\nsteps","tasks_json":"[{\"id\":\"t1\",\"title\":\"do it\"}]"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(scriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Start wrapd with planner-cmd=fake-claude and FAKE_CLAUDE_SCRIPT in its planner env.
	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "wrap.sock")
	cmd := exec.Command(wrapdBin,
		"--state-dir", stateDir,
		"--socket", sock,
		"--planner-cmd", fakeClaude,
		"--planner-env", "FAKE_CLAUDE_SCRIPT="+scriptPath,
		"--tick-interval", "100ms",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapd: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	// Wait for socket.
	waitForSocket(t, sock, 3*time.Second)

	// Submit run via the wrap CLI.
	out, err := exec.Command(wrapBin, "run", "--socket", sock, "--repo", repo, specPath).CombinedOutput()
	if err != nil {
		t.Fatalf("wrap run: %v\noutput: %s", err, out)
	}
	var submit intake.SubmitRunResponse
	if err := json.Unmarshal(out, &submit); err != nil {
		t.Fatalf("decode submit response: %v\noutput: %s", err, out)
	}

	// Poll GET /runs/{id} until phase == plan_gate or timeout.
	httpc := socketHTTPClient(sock)
	deadline := time.Now().Add(15 * time.Second)
	var got intake.GetRunResponse
	for time.Now().Before(deadline) {
		resp, err := httpc.Get("http://wrap/runs/" + submit.RunID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		_ = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if got.Phase == "plan_gate" {
			break
		}
		if got.Phase == "failed" {
			t.Fatalf("run failed unexpectedly: %+v", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got.Phase != "plan_gate" {
		t.Fatalf("phase = %q after wait, want plan_gate (response: %+v)", got.Phase, got)
	}
	if !strings.Contains(got.PlanMD, "# Plan") {
		t.Errorf("PlanMD = %q, want to contain '# Plan'", got.PlanMD)
	}
	if !strings.Contains(got.TasksJSON, `"id":"t1"`) {
		t.Errorf("TasksJSON = %q, want to contain task t1", got.TasksJSON)
	}
}

func makeIntegrationRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "initial")
	return dir
}

func waitForSocket(t *testing.T, sock string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("unix", sock, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("socket %s never came up", sock)
}

func socketHTTPClient(sock string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
}
```

- [ ] **Step 2: Run all integration tests, expect PASS**

Run:
```bash
cd /home/lithial/coding/wrap && make test-integration
```
Expected: both `TestSkeletonEndToEnd` (Phase 1) and `TestPlannerHappyPath` (Phase 2) PASS.

- [ ] **Step 3: Run full unit test suite to confirm no regressions**

Run:
```bash
cd /home/lithial/coding/wrap && go test ./...
```
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
cd /home/lithial/coding/wrap
git add test/integration/planner_test.go
git commit -m "test(integration): cover phase 2 planner happy path end-to-end"
```

---

## Task 11: Update CLAUDE.md and project memory for Phase 2 deliverables

**Files:**
- Modify: `/home/lithial/coding/wrap/CLAUDE.md`

- [ ] **Step 1: Add a Phase 2 stanza to CLAUDE.md**

In `/home/lithial/coding/wrap/CLAUDE.md`, under the existing "What this project is" section's status sentence ("Phase 1 (skeleton) is merged. ..."), append:

```markdown
Phase 2 (FSM + planner phase) introduces `internal/fsm` (pure transitions), `internal/worktree` (git plumbing), `internal/workerrpc` (NDJSON protocol mirroring MCP tool names), `internal/supervisor` (one-shot subprocess + RPC collection), and `internal/orchestrator` (polling loop that drives `pending → planning → plan_gate`). The planner subprocess is configurable via `wrapd --planner-cmd`; integration tests point it at `fake-claude` with `--planner-env FAKE_CLAUDE_SCRIPT=...`.
```

Also add to the "Project conventions worth knowing" list:

```markdown
- **Worker-RPC over NDJSON, not real MCP yet.** `internal/workerrpc` uses method names (`report_progress`, `report_plan`, `report_done`) chosen to match the eventual MCP tool surface. When real MCP lands (Phase 9), swap the transport, keep the method names.
- **`--planner-cmd` is bare path only in Phase 2.** Phase 9 will introduce a richer template with `--mcp wrap --append-system-prompt <planner.md>` args.
```

- [ ] **Step 2: Commit**

```bash
cd /home/lithial/coding/wrap
git add CLAUDE.md
git commit -m "docs(claude.md): document phase 2 packages and conventions"
```

---

## Self-review checklist

After running every task end-to-end, verify:

1. **`go test ./...` passes** with no failures or skipped tests on a machine with `git` installed.
2. **`make test-integration` passes** end-to-end including both Phase 1 and Phase 2 integration tests.
3. **A run submitted to a real `wrapd` reaches `plan_gate`** within a few seconds when `--planner-cmd` is `fake-claude` + a valid script.
4. **The orchestrator does NOT advance a run past `plan_gate`.** That's Phase 5's job (gates) and Phase 3's job (workers). Confirm by reading the FSM transitions map — `transitions[PhasePlanGate]` is empty.
5. **No package outside `internal/store` imports `database/sql`.** Run `grep -r 'database/sql' internal/ cmd/` and confirm matches appear only under `internal/store/`.
6. **The `workerrpc` method names match the eventual MCP tool surface** (`report_progress`, `report_plan`, `report_done`, `report_blocked`).

---

## Out of scope for Phase 2 (defer to later phases)

- **Real MCP wire integration.** Phase 9 (when real `claude` integration lands).
- **Gate engine, plan approval prompts.** Phase 5.
- **Worker phase, worktree concurrency, dependency DAG execution.** Phase 3.
- **Merger phase, emission.** Phase 4.
- **Daemon-restart reconciliation, retry budgets, idle timeouts.** Phase 8.
- **TUI.** Phase 6.
- **Specfile / GitHub intake adapters.** Phase 7.
- **Event recording into the `events` table.** Phase 5 (introduced alongside the gate engine, which needs the event stream for the TUI).
