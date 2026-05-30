# Phase 10: Deferred Features — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the seven deferred `wrap` features (Phase 10 spec) atop two cross-cutting foundations — idempotent additive schema migration and typed gate resolution actions.

**Architecture:** Foundations land first: F1 adds guarded `ALTER TABLE ADD COLUMN` migrations to `store.Open()`; F2 threads a typed `action` through the gate resolution path (`gates` validation → `store.ResolveGate` → API → client → CLI). The seven features (10a–10g) build on those and are decomposed by `wrap`'s own planner at dogfood time — they appear here as **briefs** (intent + spec pointer + touch-points), not bite-sized tasks, because the planner re-plans from the spec.

**Tech Stack:** Go 1.25, `modernc.org/sqlite`, the existing `internal/{store,gates,api,client,orchestrator}` packages, `fake-claude` shim for integration tests.

---

## Spec

This plan implements `docs/superpowers/specs/2026-05-30-phase-10-deferred-features-design.md`. Read it first; section references below (F1, F2, 10a…10g) are to that document.

## File structure

**Foundations (full TDD below):**

- `internal/store/schema.go` — add `additiveMigrations` slice + `isDuplicateColumnErr`; run them in `applySchema` after `schema.sql`.
- `internal/store/schema.sql` — add the new columns to the canonical (fresh-DB) schema.
- `internal/store/gates.go` — `ResolveGate` gains a trailing `action string` param.
- `internal/gates/gates.go` — add `ValidAction(kind, action string) bool` + action constants.
- `internal/intake/intake.go` — `ResolveGateRequest` gains `Action string`.
- `internal/api/handlers.go` — `handleResolveGate` reads/validates `action`; new `POST /runs/{id}/resolve`.
- `internal/client/client.go` — `resolveGate` sends `action`; add `Resolve(ctx, runID, decision, action, by)`.
- `cmd/wrap/main.go` — `wrap resolve <run-id> --action <a>` command.

**Features (briefs only — planner decomposes):** touch-points listed per brief.

---

## Task F1: Idempotent additive schema migration

**Files:**
- Modify: `internal/store/schema.go`
- Modify: `internal/store/schema.sql`
- Test: `internal/store/schema_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/store/schema_test.go`:

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
)

// columnExists reports whether table has a column of the given name.
func columnExists(t *testing.T, s *Store, table, col string) bool {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == col {
			return true
		}
	}
	return false
}

func TestOpenAppliesAdditiveMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wrap.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := map[string][]string{
		"gates":   {"action"},
		"runs":    {"max_workers", "worker_idle_timeout_ms"},
		"workers": {"last_progress_at"},
	}
	for table, cols := range want {
		for _, c := range cols {
			if !columnExists(t, s, table, c) {
				t.Errorf("expected column %s.%s to exist", table, c)
			}
		}
	}
	_ = s.Close()

	// Idempotency: re-opening the same DB must not error (duplicate-column guard).
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s2.Close()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestOpenAppliesAdditiveMigrations -v`
Expected: FAIL — columns `gates.action`, `runs.max_workers`, `runs.worker_idle_timeout_ms`, `workers.last_progress_at` do not exist.

- [ ] **Step 3: Add the additive migrations to `schema.go`**

In `internal/store/schema.go`, add after the `schemaSQL` embed and extend `applySchema`:

```go
// additiveMigrations are idempotent ALTER TABLE statements applied after
// schema.sql. Each must tolerate re-application (existing DBs already have the
// column); isDuplicateColumnErr filters SQLite's "duplicate column name". Append
// only — never rewrite or reorder (the guard relies on additive semantics).
var additiveMigrations = []string{
	`ALTER TABLE gates ADD COLUMN action TEXT`,
	`ALTER TABLE runs ADD COLUMN max_workers INTEGER`,
	`ALTER TABLE runs ADD COLUMN worker_idle_timeout_ms INTEGER`,
	`ALTER TABLE workers ADD COLUMN last_progress_at INTEGER`,
}

func isDuplicateColumnErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}
```

Then, inside `applySchema`, after the `schema.sql` loop but before `tx.Commit()`, add:

```go
	for _, stmt := range additiveMigrations {
		if _, err := tx.ExecContext(ctx, stmt); err != nil && !isDuplicateColumnErr(err) {
			return fmt.Errorf("migration %q: %w", firstLine(stmt), err)
		}
	}
```

- [ ] **Step 4: Add the columns to `schema.sql` (fresh-DB path)**

In `internal/store/schema.sql`, add `action TEXT` to the `gates` table, `max_workers INTEGER` and `worker_idle_timeout_ms INTEGER` to `runs`, and `last_progress_at INTEGER` to `workers`. Fresh DBs then already have them; the guarded ALTERs become no-ops (duplicate-column, swallowed).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestOpenAppliesAdditiveMigrations -v`
Expected: PASS.

- [ ] **Step 6: Run the full store suite + race**

Run: `go test ./internal/store/ -race`
Expected: PASS (no regression from the schema change).

- [ ] **Step 7: Commit**

```bash
git add internal/store/schema.go internal/store/schema.sql internal/store/schema_test.go
git commit -m "feat(store): idempotent additive ADD COLUMN migrations on Open()"
```

---

## Task F2.1: Gate action vocabulary (pure validation)

**Files:**
- Modify: `internal/gates/gates.go`
- Test: `internal/gates/gates_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/gates/gates_test.go`:

```go
func TestValidAction(t *testing.T) {
	cases := []struct {
		kind, action string
		want         bool
	}{
		{"plan", "proceed", true},
		{"plan", "abort", true},
		{"plan", "", true},                 // empty = default decision semantics
		{"merge", "drop_branch", false},    // not a merge-gate action
		{"worker_blocked", "proceed", true},
		{"worker_blocked", "retry", true},
		{"worker_blocked", "abort", true},
		{"worker_blocked", "drop_branch", false},
		{"merge_conflict", "drop_branch", true},
		{"merge_conflict", "takeover", true},
		{"merge_conflict", "abort", true},
		{"merge_conflict", "retry", false},
	}
	for _, c := range cases {
		if got := ValidAction(c.kind, c.action); got != c.want {
			t.Errorf("ValidAction(%q,%q)=%v want %v", c.kind, c.action, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gates/ -run TestValidAction -v`
Expected: FAIL — `ValidAction` undefined.

- [ ] **Step 3: Implement `ValidAction`**

Add to `internal/gates/gates.go`:

```go
// Action constants for typed gate resolution. The empty action is always valid
// and preserves the legacy approve=proceed / reject=abort semantics.
const (
	ActionProceed    = "proceed"
	ActionAbort      = "abort"
	ActionRetry      = "retry"
	ActionDropBranch = "drop_branch"
	ActionTakeover   = "takeover"
)

// actionsByKind lists the non-empty actions each gate kind accepts. Plan/merge
// gates take only proceed/abort (the approve/reject defaults). worker_blocked and
// merge_conflict take their recovery-specific actions.
var actionsByKind = map[string][]string{
	"plan":           {ActionProceed, ActionAbort},
	"merge":          {ActionProceed, ActionAbort},
	"worker_done":    {ActionProceed, ActionAbort},
	"worker_blocked": {ActionProceed, ActionRetry, ActionAbort},
	"merge_conflict": {ActionDropBranch, ActionTakeover, ActionAbort},
}

// ValidAction reports whether action is acceptable for gate kind. The empty
// action is always valid (default decision semantics). An unknown kind accepts
// only the empty action (never invent recovery options for it).
func ValidAction(kind, action string) bool {
	if action == "" {
		return true
	}
	for _, a := range actionsByKind[kind] {
		if a == action {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gates/ -run TestValidAction -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gates/gates.go internal/gates/gates_test.go
git commit -m "feat(gates): typed gate resolution action vocabulary + ValidAction"
```

---

## Task F2.2: Thread `action` through `store.ResolveGate`

**Files:**
- Modify: `internal/store/gates.go`
- Modify: `internal/store/gates_test.go` (existing callers) and any other `ResolveGate` callers
- Test: `internal/store/gates_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/store/gates_test.go` a test that resolves with an action and reads it back. The `gates` row gains an `action` column (from F1); extend `scanGate`/`Gate` to surface it:

```go
func TestResolveGatePersistsAction(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t) // existing helper
	runID := seedRun(t, s) // existing helper pattern; see gates_test.go
	id, err := s.InsertGate(ctx, Gate{RunID: runID, Kind: "merge_conflict", PayloadJSON: "{}"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.ResolveGate(ctx, id, "approved", "tester", "drop_branch"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	g, err := s.LatestGateByKind(ctx, runID, "merge_conflict")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if g.Action != "drop_branch" {
		t.Errorf("Action=%q want drop_branch", g.Action)
	}
}
```

(Use the existing test helpers in `gates_test.go`/`testing_helpers_test.go` for store + run seeding — match their current names.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestResolveGatePersistsAction -v`
Expected: FAIL — `ResolveGate` takes 3 args not 4; `Gate.Action` undefined.

- [ ] **Step 3: Add `Action` to `Gate`, scan it, write it in `ResolveGate`**

In `internal/store/gates.go`:
- Add `Action string` to the `Gate` struct.
- Update `gateColumns` to include `COALESCE(action, '')` and `scanGate` to scan into `&g.Action` (append at the end; keep column/scan order aligned).
- Change the signature to `func (s *Store) ResolveGate(ctx context.Context, id, status, resolvedBy, action string) error` and set `action = ?` in the UPDATE:

```go
	res, err := s.db.ExecContext(ctx, `
		UPDATE gates SET status = ?, resolved_by = ?, resolved_at = ?, action = ? WHERE id = ? AND status = 'pending'
	`, status, resolvedBy, now, action, id)
```

- [ ] **Step 4: Update existing callers**

Update every `ResolveGate` caller to pass an action. In `internal/api/handlers.go:60` (kill path) pass `""`. Any other callers (orchestrator) pass `""` for now; F2.3 wires the real value at the API boundary.

Run: `grep -rn "ResolveGate(" internal/ cmd/ | grep -v _test.go` and fix each.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestResolveGate -v` then `go build ./...`
Expected: PASS and a clean build (all callers updated).

- [ ] **Step 6: Commit**

```bash
git add internal/store/gates.go internal/store/gates_test.go internal/api/handlers.go
git commit -m "feat(store): persist a resolution action on ResolveGate"
```

---

## Task F2.3: API — accept and validate `action`

**Files:**
- Modify: `internal/intake/intake.go` (the file defining `ResolveGateRequest`)
- Modify: `internal/api/handlers.go`
- Test: `internal/api/handlers_test.go` (the gate-resolution handler test)

- [ ] **Step 1: Write the failing test**

`internal/api` tests get a socket path from `testutil.StartInProcessServerWithStore(t)` (returns `(sock string, st *store.Store)`) and drive it with a socket-backed `*http.Client` — follow the existing pattern in `handlers_test.go` (see the helper that builds an `http.Client` over the Unix socket, used by the tests at `handlers_test.go:57+`). Seed a run + a pending `merge` gate directly via `st`, then POST `/runs/{id}/approve` with `{"by":"t","action":"drop_branch"}` and assert HTTP 400 (invalid action for kind `merge`):

```go
func TestResolveGateRejectsInvalidAction(t *testing.T) {
	sock, st := testutil.StartInProcessServerWithStore(t)
	httpc := socketHTTPClient(sock) // existing helper in this test file
	runID := seedRunWithPendingGate(t, st, "merge") // local helper: InsertRun + InsertGate(kind=merge, pending)
	resp, err := httpc.Post("http://x/runs/"+runID+"/approve", "application/json",
		strings.NewReader(`{"by":"t","action":"drop_branch"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
}
```

(Match the exact socket-client helper name already used in `handlers_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestResolveGateRejectsInvalidAction -v`
Expected: FAIL — action ignored, returns 200.

- [ ] **Step 3: Add `Action` to the DTO and validate in the handler**

- In `internal/intake/intake.go`, add `Action string \`json:"action,omitempty"\`` to `ResolveGateRequest`.
- In `internal/api/handlers.go`, in `handleResolveGate`, after loading the pending `gate`, validate: `if !gates.ValidAction(gate.Kind, req.Action) { writeError(w, http.StatusBadRequest, ...) ; return }`, then pass `req.Action` to `s.store.ResolveGate(ctx, gate.ID, status, by, req.Action)`.
- Add the new route `mux.HandleFunc("POST /runs/{id}/resolve", s.handleResolveDecision())` where `handleResolveDecision` reads `{"decision","action","by"}` (decision ∈ approve→"approved" | reject→"rejected"), validates the action, and resolves. This covers actions like `retry`/`takeover` that are neither a plain approve nor reject. Reuse the same load+validate+resolve body as `handleResolveGate`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/ -run TestResolveGate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/intake/dto.go internal/api/handlers.go internal/api/*_test.go
git commit -m "feat(api): validate gate resolution action; add POST /runs/{id}/resolve"
```

---

## Task F2.4: Client + CLI — `wrap resolve --action`

**Files:**
- Modify: `internal/client/client.go`
- Modify: `cmd/wrap/main.go`
- Test: `internal/client/*_test.go`

- [ ] **Step 1: Write the failing test**

In the client test, assert `Resolve(ctx, runID, "approve", "drop_branch", "by")` sends a body containing `"action":"drop_branch"` to the right path (use the existing client test harness that records the request).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/client/ -run TestResolve -v`
Expected: FAIL — `Resolve` undefined.

- [ ] **Step 3: Implement client + CLI**

- In `internal/client/client.go`: change `resolveGate` to marshal `intake.ResolveGateRequest{By: by, Action: action}` and add `func (c *Client) Resolve(ctx context.Context, runID, decision, action, by string) (intake.ResolveGateResponse, error)` that POSTs to `/runs/{id}/resolve`. Keep `Approve`/`Reject` delegating with `action=""`.
- In `cmd/wrap/main.go`: add `case "resolve": return cmdResolve(rest)`; `cmdResolve` parses `<run-id>` + `--action <a>` (and optional `--decision approve|reject`, default approve) and calls `client.Resolve`. Update the usage string to include `resolve`.

- [ ] **Step 4: Run test + build**

Run: `go test ./internal/client/ -run TestResolve -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/client/client.go cmd/wrap/main.go internal/client/*_test.go
git commit -m "feat(cli): wrap resolve <run-id> --action; client Resolve()"
```

---

## Foundation gate

After F1+F2: `go test ./... -race` and `make test-integration` must be green before any feature work. The feature briefs below assume `gates.ValidAction`, the new columns, and `store.ResolveGate(…, action)` exist.

---

## Feature briefs (planner decomposes these)

Each brief is the intent + spec section + touch-points. `wrap`'s planner turns these into `tasks_json`; a worker implements each TDD-style (pure unit test → integration test via `fake-claude`). Dependencies are in the spec's DAG.

### Brief 10a — `wrap prune <run-id>` (spec §10a)
Terminal-runs-only destructive cleanup. **Touch:** `internal/worktree` (`PruneRun`: `git worktree remove` each retained worktree, delete `wrap/<run>/*` branches, serialized with the worktree-add mutex), `internal/store` (`ErrRunNotTerminal` guard), `internal/api` (`POST /runs/{id}/prune`), `internal/client`, `cmd/wrap` (`prune`), `events` (`pruned`). **Tests:** unit `PruneRun` against a temp repo with seeded worktrees; integration 409 on non-terminal run + happy path.

### Brief 10b — `worker_blocked` gate (spec §10b)
`report_blocked` opens a pending `worker_blocked` gate instead of failing; run holds at `working`; siblings keep running. **Touch:** `internal/scheduler` (new non-propagating `blocked` outcome), `internal/orchestrator/workers.go` (blocked → open gate + hold, not fail), `internal/orchestrator/gates.go` (resolve: `proceed` drop task + continue, `retry` re-dispatch via Phase 8 attempt machinery, `abort` fail). **Tests:** scheduler unit tests for non-propagating `blocked`; integration with a `fake-claude` `{"kind":"blocked"}` worker asserting the hold + each action's outcome.

### Brief 10c — `merge_conflict` gate (spec §10c)
Merger conflict opens a pending `merge_conflict` gate; run holds at `merging`. Built atop 10b's hold machinery. **Touch:** `internal/orchestrator/merger.go` (conflict → open gate + hold), gate resolution (`drop_branch` re-merge excluding conflicting branches from the event payload, `takeover` pause for manual merge then `proceed`, `abort` fail), `cmd/fake-claude/main.go` (new `{"kind":"conflict","branches":[...]}` script action). **Tests:** integration driving a merger that reports a conflict, asserting the `merging` hold + each action.

### Brief 10d — Per-run `max_workers` (spec §10d)
Move the concurrency cap into per-run config. **Touch:** `runs.max_workers` column (F1), `intake.SubmitRunRequest` (+ optional `max_workers`), API submit handler (default chain: request → `--max-workers` flag → persist), `internal/orchestrator/workers.go` (read `run.MaxWorkers`), `cmd/wrap` (`run --max-workers`), `GET /runs/{id}` exposes it. **Tests:** unit default chain; integration that `--max-workers 1` serializes two independent tasks.

### Brief 10e — Worker idle timeout (spec §10e)
Distinct idle (silence) timeout vs the hard runtime ceiling. **Touch:** `workers.last_progress_at` (F1, bumped on `report_progress`/reports), `runs.worker_idle_timeout_ms` (F1), worker step idle watchdog (cancels step ctx on idle → retryable timeout, `worker_timeout{reason:"idle"}`), `--worker-idle-timeout` daemon flag (default disabled). **Tests:** integration with a `fake-claude` worker that emits one progress then sleeps past the idle budget; assert idle timeout + retry. Run with `-race` (new goroutine).

### Brief 10f — Emission long-poll auto-dispatch (spec §10f)
Auto-fire emission on `done` for connected adapters; `wrap emit` stays the fallback. **Touch:** `GET /runs/{id}/emission` long-poll endpoint, daemon dispatch on `merge_gate → done` (`emission_requested` event + wake subscriber), `wrap run --wait` (keeps CLI attached), `internal/client` subscription method. Emission dispatch logic (`intake.EmitDeps`) unchanged — only the trigger is new. **Tests:** integration that a `--wait` subscriber receives the signal on `done`; pull path still works with no subscriber. Run with `-race`.

### Brief 10g — Full mid-working crash resume (spec §10g)
`Reconcile` resumes mid-`working` runs from partial progress instead of failing them. **Touch:** `internal/orchestrator/reconcile.go` (for `working` runs: derive done tasks from `worker_done` events, fail only orphaned running workers as retryable `daemon_restart`, re-enter scheduler with survivors; `planning`/`merging` still fail). `daemon_recovered` event notes a resume. **Tests:** integration seeding one `worker_done` + one orphaned running worker, running `Reconcile`, asserting the done task is not re-run, the orphan retries, and the run proceeds to `merging`.

---

## Dogfood execution (spec §Dogfooding plan)

Per the user's choice, the whole phase is built **by `wrap`**, including F1/F2 as the first DAG task, with `require_approval` gates kept on:

1. Start `wrapd` against this repo with real `claude` (defaults: `--planner-cmd/--worker-cmd/--merger-cmd claude`, `--prompt-dir prompts`), `require_approval` plan/merge gates.
2. `wrap run --repo <this repo> docs/superpowers/specs/2026-05-30-phase-10-deferred-features-design.md` (the spec is the planner's input; this plan is supporting context).
3. Review the plan gate (expect a DAG resembling the spec's: foundation first, then the feature leaves). Approve.
4. Supervise workers; use the new `worker_blocked` gate if one gets stuck. Review the merge gate before it lands.
5. After `done`: run the suites (`go test ./... -race`, `make test-integration`), then `wrap prune` the run's worktrees.

## Self-review notes

- **Spec coverage:** F1↔spec F1, F2.1–F2.4↔spec F2, briefs 10a–10g↔spec §10a–§10g. All seven features + both foundations are represented.
- **Type consistency:** `ResolveGate(…, action)` (F2.2) matches the API call site (F2.3) and `client.Resolve(…, action, …)` (F2.4); `gates.ValidAction(kind, action)` is defined in F2.1 and consumed in F2.3.
- **Foundations are full TDD; features are briefs by design** (the planner re-decomposes from the spec — see the plan-depth decision).
