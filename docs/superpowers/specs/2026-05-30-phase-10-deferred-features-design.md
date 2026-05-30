# wrap — Phase 10: Deferred Features

**Status:** Design (brainstorming complete, implementation plans pending)
**Date:** 2026-05-30
**Owner:** jameshessell@gmail.com
**Builds on:** the completed Phases 1–9 (`docs/superpowers/specs/2026-05-26-claude-swarm-wrapper-design.md`)

## Summary

Phases 1–9 implemented the full `wrap` spec end-to-end. Along the way, seven items were
deliberately deferred — each non-blocking, each recorded in `CLAUDE.md` and the project
memory. Phase 10 closes them. This document is the overarching design; each feature gets
its own bite-sized implementation plan (`10a`–`10g`) under `docs/superpowers/plans/`,
following the established 9a/9b/9c precedent.

The seven deferred items, with current behavior:

| Plan | Feature | Current behavior |
| ---- | ------- | ---------------- |
| 10a | `wrap prune <run-id>` | no cleanup; worktrees + `wrap/<run>/*` branches accumulate forever |
| 10b | `worker_blocked` gate | `report_blocked` → run `failed` (no human pause mid-phase) |
| 10c | `merge_conflict` gate | merger conflict → run `failed` |
| 10d | Per-run `max_workers` | daemon-wide `--max-workers` flag only |
| 10e | Worker idle timeout | only a hard runtime ceiling (`--step-timeout`) exists |
| 10f | Emission long-poll auto-dispatch | pull-only via `wrap emit` |
| 10g | Full mid-working crash resume | `Reconcile` fails any run caught mid-`working` |

The explicit spec non-goals (recursive workers, remote/containerized workers,
multi-user/hosted mode, plan/merge eval harnesses) remain **out of scope** and are not
addressed here.

## Goals

- Close all seven deferred items without regressing the Phase 1–9 behavior or violating
  the established package-layering and single-writer-of-phase invariants.
- Establish the project's first two reusable patterns for growth: **idempotent additive
  schema migration** and **typed gate resolution actions**.
- Keep every feature independently testable and shippable as its own PR.
- Produce a build-order DAG that the `wrap` orchestrator itself can plan against — this
  phase is intended to be dogfooded: `wrap` builds `wrap`.

## Non-goals

- The four spec-level non-goals above (recursive/remote/multi-user/evals).
- A general migration framework with version tracking and down-migrations — Phase 10 uses
  the minimal idempotent-`ADD COLUMN` approach (see F1); a versioned runner can come later
  if the column count ever justifies it.
- Changing the worker substrate, the MCP tool surface names, or the intake adapter contract.

## Cross-cutting foundations

These two changes underpin several features and land first.

### F1 — Idempotent additive schema evolution (`internal/store`)

Today `store.Open()` applies the embedded `schema.sql` once. Phase 10 needs new columns on
existing tables. Rather than introduce a versioned migration framework, `Open()` will,
**after** applying `schema.sql`, execute an ordered list of additive statements, each
tolerant of SQLite's "duplicate column name" error:

```go
// after schema.sql is applied
for _, stmt := range additiveMigrations {
    if _, err := db.Exec(stmt); err != nil && !isDuplicateColumnErr(err) {
        return nil, fmt.Errorf("migration %q: %w", stmt, err)
    }
}
```

- Fresh databases get the columns from an updated `schema.sql`; existing databases get them
  via the guarded `ALTER TABLE ... ADD COLUMN`. Both converge to the same shape.
- `isDuplicateColumnErr` matches SQLite's `duplicate column name` message; every other error
  is fatal (we never silently swallow a real migration failure).
- New columns this phase: `gates.action`, `runs.max_workers`, `runs.worker_idle_timeout_ms`,
  `workers.last_progress_at`.
- `database/sql` stays confined to `store` (the existing hard rule). The additive list lives
  beside the `go:embed` of `schema.sql`.

This is deliberately minimal and ad hoc; it is the documented trade-off the user chose over a
version table.

### F2 — Typed gate resolution actions (`internal/gates`, `internal/store`, `internal/api`, `internal/client`)

The Phase 5 gate engine resolves gates with a binary approve/reject. The `worker_blocked` and
`merge_conflict` gates need richer outcomes (the original spec lists abort / drop-branch /
human-takeover for conflicts). Phase 10 adds a typed **action** carried on resolution.

- **Schema:** `gates.action TEXT` (nullable; empty = today's default semantics).
- **Store:** `store.ResolveGate(ctx, gateID, decision, resolvedBy, action)` — `action` is the
  new trailing parameter. The existing `status='pending'` optimistic-lock guard
  (`ErrGateNotPending` → 409) is preserved.
- **Action vocabulary** (validated in `internal/gates`):
  - Plan/merge gates: `proceed` (default for approve), `abort` (default for reject). Unchanged
    behavior when action is empty.
  - `worker_blocked`: `proceed` | `retry` | `abort`.
  - `merge_conflict`: `drop_branch` | `takeover` | `abort`.
  - An action invalid for a gate kind is rejected at the API boundary (400) before the
    orchestrator ever sees it.
- **API:** `POST /runs/{id}/approve` and `/reject` accept an optional `{"action": "..."}`
  body; a new `POST /runs/{id}/resolve` takes `{"decision","action"}` for the cases that are
  neither a plain approve nor reject. All three still only flip the pending `gates` row —
  **the orchestrator remains the single writer of `runs.phase`** (the Phase 5 invariant).
- **CLI:** `wrap resolve <run-id> --action <a>` joins `wrap approve`/`wrap reject`. The latter
  two keep working with their default actions.
- **Orchestrator:** the gate-driver functions branch on the resolved action when they observe
  the resolution on the next tick.

## Feature designs

### 10a — `wrap prune <run-id>`

Destructive cleanup of a terminal run's git artifacts, per the spec's "no data loss until
explicit prune" rule.

- **Guard:** only runs in a terminal phase (`done` | `failed` | `killed`) may be pruned;
  otherwise `409` / `ErrRunNotTerminal`.
- **Action:** for the run, `git worktree remove` each retained worktree under
  `<state-dir>/runs/<run>/<wid>/` (never `rm -rf`), then delete every `wrap/<run>/*` branch.
  A pure helper (`worktree.PruneRun`) does the git plumbing; serialized with the same mutex
  `driveWorkers` uses for `git worktree add` (repo-wide ref/index locks collide).
- **Record:** a `pruned` event (forensic log; no new table).
- **Surface:** `POST /runs/{id}/prune` + `wrap prune <run-id>`; `client.PruneRun`.
- **Tests:** unit-test `worktree.PruneRun` against a real temp repo with seeded worktrees;
  integration-test the terminal-only guard (409 on a non-terminal run) and the happy path.

### 10b — `worker_blocked` gate

A blocked worker becomes a human decision point instead of an immediate run failure.

- **Trigger:** a worker's `report_blocked` (today → `failed`) now opens a pending
  `gate kind=worker_blocked` and records the existing `worker_blocked` event. The blocked
  worker's task is marked blocked (a tolerated, non-propagating state — distinct from
  `failed`, which still propagates to dependents).
- **Hold:** in-flight and ready independent workers **keep running**; the run holds at phase
  `working` and does not advance to `merging` while a `worker_blocked` gate is pending.
  `GET /runs/{id}` exposes the pending gate (existing `pending_gate_kind` field), preserving
  the no-window invariant (gate opened before the hold is observable).
- **Resolution actions (F2):**
  - `proceed` — drop the blocked task; continue to `merging` with the surviving branches.
  - `retry` — re-dispatch the blocked task as a fresh attempt (a new `workers` row, reusing
    the Phase 8 attempt machinery).
  - `abort` — fail the run (today's behavior, now opt-in).
- **Scheduler:** this is the scheduler's first "tolerated failure" path — a blocked task must
  not transitively fail its dependents (unlike a hard failure). The pure DAG scheduler gains a
  `blocked` outcome alongside `done`/`failed`.
- **Tests:** pure scheduler unit tests for the non-propagating `blocked` outcome; an
  integration test driving a `fake-claude` worker that emits `{"kind":"blocked"}`, asserting
  the run holds at `working` with a pending gate, then `proceed`/`retry`/`abort` each reach the
  expected terminal state.

### 10c — `merge_conflict` gate

The merger reporting a conflict becomes a human decision point with conflict-specific recovery
options. Built atop 10b's hold machinery.

- **Trigger:** the merger reporting a conflict (a `report_blocked` from the merger, or a
  non-zero merge with a conflict signal) opens a pending `gate kind=merge_conflict` and records
  a `merge_conflict` event; the run holds at phase `merging`.
- **Resolution actions (F2):**
  - `drop_branch` — re-run the merge excluding the conflicting branch(es). The conflicting
    branch list rides on the `merge_conflict` event payload so the human and the re-run both
    know which branches to drop.
  - `takeover` — pause for a human to resolve the conflict manually in the retained
    `wrap/<run>/merge` worktree; a follow-up `proceed` resumes (merger re-invoked or merge
    accepted as-is).
  - `abort` — fail the run.
- **`fake-claude`:** extended with a `{"kind":"conflict","branches":[...]}` script action so
  the integration path is deterministic and API-free.
- **Tests:** integration test driving a merger that reports a conflict, asserting the
  `merging` hold + pending `merge_conflict` gate, then each action's outcome.

### 10d — Per-run `max_workers`

Move the concurrency cap from a daemon-wide flag into per-run config (the documented Phase 5
debt).

- **Schema:** `runs.max_workers INTEGER`.
- **Default chain:** `SubmitRunRequest.MaxWorkers` (optional) → else the daemon's
  `--max-workers` flag (now the *default source*, default 4) → persisted on the run at submit
  time. (A project-level default is a natural future extension but out of scope; the daemon
  flag is the default.)
- **Scheduler:** `driveWorkers` reads `run.MaxWorkers` instead of the daemon flag.
- **Surface:** `intake.SubmitRunRequest` gains an optional `max_workers`; the CLI `wrap run`
  gains `--max-workers`. `GET /runs/{id}` exposes the effective value.
- **Tests:** unit-test the default chain; integration-test that a run submitted with
  `--max-workers 1` serializes two independent tasks while the daemon default would allow
  parallelism.

### 10e — Worker idle timeout

A distinct *idle* timeout (silence) separate from the hard runtime ceiling.

- **Schema:** `workers.last_progress_at INTEGER` (0 = unset sentinel, matching the existing
  `PID`/`StartedAt` convention), bumped on every `report_progress` (and on `report_*` reports).
  `runs.worker_idle_timeout_ms INTEGER` (0 = disabled) for the per-run idle budget.
- **Mechanism:** the worker step grows an idle watchdog: if `now - last_progress_at` exceeds
  the idle budget, the watchdog cancels the worker's step context. This reuses the Phase 8
  timeout path — an idle kill is a **retryable timeout** — and records
  `worker_timeout{reason:"idle"}` (vs the existing runtime-ceiling timeout). The hard
  `--step-timeout` ceiling still applies as the upper bound.
- **Default chain:** `--worker-idle-timeout` daemon flag (default: disabled, preserving
  current behavior) → `runs.worker_idle_timeout_ms`.
- **Tests:** integration test with a `fake-claude` worker that emits one progress then sleeps
  past the idle budget without further progress; assert an idle timeout + retry.

### 10f — Emission long-poll auto-dispatch

Realize the spec's step-7 push model: emission fires automatically on `done` for connected
adapters, while `wrap emit` stays the disconnected-adapter fallback.

- **Subscription:** a `GET /runs/{id}/emission` long-poll endpoint. An adapter that submitted
  the run opens this subscription and blocks until the daemon signals emission-ready (or a
  timeout → re-poll). Because today's `wrap run`/`submit`/`github` submit-and-exit, staying
  attached is opt-in: a new `--wait` flag keeps the CLI process alive on the subscription
  through to emission. Without `--wait`, behavior is unchanged (submit and exit; emit later).
- **Dispatch:** on `merge_gate → done`, the daemon records an `emission_requested` event and
  wakes any subscriber, which then runs the same `intake.EmitDeps` dispatch the pull path uses
  (CLI prints the branch; specfile writes the `.DONE` sidecar; github pushes + opens a PR).
- **Fallback preserved:** if no subscriber is connected, the emission stays pending and
  `wrap emit <run-id>` triggers it later (today's behavior). Emission dispatch logic is
  unchanged — only the trigger is new — so the adapter-purity rule (`intake` stays
  interface-driven, `cmd/wrap` wires the impls) holds.
- **Tests:** integration test that a `wrap run --wait` subscriber receives the emission signal
  once the run reaches `done`, and that the pull path still works when no subscriber is attached.

### 10g — Full mid-working crash resume

`Reconcile` resumes mid-`working` runs from partial progress instead of failing them.

- **Today:** `Reconcile` (run once at `wrapd` startup) fails any run caught in an active phase
  (`planning`/`working`/`merging`) because resuming mid-subprocess is unsafe.
- **Change (scoped to `working`):** for a run in `working`, `Reconcile` re-derives state from
  the event log — tasks with a `worker_done` event are complete; workers still marked running
  (orphaned by the crash) are failed as retryable (`worker_failed{reason:"daemon_restart"}`) —
  then re-enters the scheduler with the completed tasks treated as done, resuming rather than
  discarding partial progress. `planning` and `merging` mid-phase runs still fail (resuming a
  single in-flight planner/merger subprocess remains unsafe and is not worth the complexity).
- **Record:** the existing `daemon_recovered` event notes a resume (vs a failure).
- **Tests:** integration test that seeds a run with one `worker_done` event and one orphaned
  running worker, runs `Reconcile`, and asserts the completed task is not re-run while the
  orphan is retried and the run proceeds to `merging`.

## Build order (DAG)

This is the dependency graph the `wrap` planner should produce when this phase is dogfooded:

```
F1 (schema guards) ──┬─→ 10d (per-run max_workers)
                     ├─→ 10e (idle timeout)
                     │
F2 (gate action) ────┼─→ 10b (worker_blocked) ─→ 10c (merge_conflict)
                     │
10a (prune) ─────────┘   10f (emission)   10g (resume)   ← independent
```

- **F1** and **F2** are the foundations and land first (they introduce the schema guards and
  the gate-action vocabulary the feature plans depend on). They may be combined into a single
  `10-foundation` plan or split; the implementation plan step decides.
- **10b → 10c** is sequential: 10c reuses 10b's working/merging hold machinery and the F2
  action path.
- **10d**, **10e** depend only on F1 (new columns). **10a**, **10f**, **10g** are independent.
- The independent leaves are exactly the parallelism a multi-worker `wrap` run exploits.

## Testing strategy

- **Unit-first**, matching the project norm: pure logic in `internal/gates` (action validation),
  `internal/scheduler` (the new `blocked` non-propagating outcome), and `internal/worktree`
  (`PruneRun`) gets table-driven unit tests with no daemon.
- **Integration** tests drive each new path through `fake-claude` over the real event-driven
  MCP path, extending the shim with new script actions only where required (10c's `conflict`).
  `fake-claude` stays env/script-driven, no flags (the standing rule).
- **No new e2e.** `make test-e2e` remains the single opt-in real-`claude` smoke; it is not
  expanded for Phase 10.
- `go test ./... -race` must stay green (the idle watchdog and the long-poll subscription both
  add goroutines — race coverage is mandatory for those two).

## Dogfooding plan

Phase 10 is intended to be built *by* `wrap`:

1. Land F1/F2 (foundation) — by hand or as the first `wrap` run, operator's choice.
2. Submit the remaining features as a `wrap` run against this repo, with the build-order DAG as
   the plan and **`require_approval` gates kept on** so the operator reviews the plan and every
   merge before it lands.
3. Use the new features as they arrive (e.g. `wrap prune` to clean up the dogfooding run's own
   worktrees; the `worker_blocked` gate if a worker gets stuck).

The implementation-plan step (`superpowers:writing-plans`) produces the per-feature plans that
become the worker tasks.
