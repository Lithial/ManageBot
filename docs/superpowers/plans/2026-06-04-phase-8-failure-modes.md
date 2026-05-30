# wrap Phase 8: Failure Modes (retries, timeouts, restart reconciliation, kill, 409)

> Compact plan. Base: `main` (Phase 7 merged). TDD per task, commit per task, push + PR at the end.

**Goal:** Make the daemon survive the rough edges. Scope (user pick: "3 headliners + kill & 409"):
worker **retries**, worker **timeout**, **daemon-restart reconciliation**, **`wrap kill`**, and **optimistic-lock
409** on concurrent gate resolution.

## Key design decisions

1. **Retries inside `runWorker`.** `Config.RetryBudget` (default 1). Retry only *retryable* failures
   (crash = nonzero exit / no `report_done`; timeout); never `report_blocked` or success. Each attempt is its
   own `workers` row (forensics); `runWorker` returns the final attempt's status. Records a `worker_retry` event.
2. **Timeout = per-worker hard ceiling** (existing step timeout). Detected via the step context deadline →
   `failed` with reason `timeout` (a `worker_timeout` event), retry-eligible. Distinct idle-timeout deferred.
3. **Reconciliation at startup** (`Orchestrator.Reconcile`, called by `wrapd` before the tick loop): for each
   non-terminal run, mark `running` workers `failed` + `worker_failed`/`daemon_restart` event; runs in
   `planning`/`working`/`merging` → `failed` (unsafe to resume mid-subprocess); runs at a gate or `pending`
   are left for the tick loop to resume; emit `daemon_recovered`.
4. **Kill = cancel registry + watcher.** A `killRegistry` (runID→cancel) lives in the orchestrator;
   `driveWorkers`/`driveMerger` register a run-scoped cancelable context (their subprocess steps derive from
   it). A concurrent `WatchKills` goroutine polls for `phase==killed` runs and cancels them → subprocesses get
   SIGKILL via the supervisor's process-group kill. `POST /runs/{id}/kill` (pure DB): FSM `kill` →`killed` +
   reject pending gates (`resolved_by=killed_by_user`). Drive functions re-check phase before advancing and
   never overwrite `killed`. Killed runs' worktrees are preserved.
5. **409 = conditional `ResolveGate`.** `ResolveGate` updates `WHERE id=? AND status='pending'`; 0 rows →
   `ErrGateNotPending`. The API maps it to `409`. Two concurrent approves: first wins, second 409s.

## Tasks

1. **store: conditional ResolveGate** → `ErrGateNotPending`; API maps to 409. Update callers + tests.
2. **orchestrator: retry budget + timeout reason** in `runWorker`; `RetryBudget` config; `worker_timeout`/
   `worker_retry` events. Unit tests (retry succeeds, budget exhausted, timeout reason, blocked not retried).
3. **orchestrator: `Reconcile`** + `wrapd` startup wiring; `daemon_recovered`/`daemon_restart` events. Tests.
4. **kill: registry + `WatchKills` + API/client/CLI `wrap kill`**; drive functions respect `killed`. Tests
   (registry unit; kill a parked run → killed + gate rejected).
5. **integration** — concurrent approve → 409; kill a gated run → killed; (timeout/restart covered by units +
   a restart reconcile integration if cheap).
6. **docs** — CLAUDE.md + this plan.
