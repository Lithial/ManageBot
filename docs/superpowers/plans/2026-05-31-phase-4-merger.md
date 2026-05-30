# wrap Phase 4: Merger Phase + Basic Emission

> Compact plan. Base: `main` (Phase 3 merged). TDD per task, commit per task, push + PR at the end.

**Goal:** Drive a run `merging → merge_gate → done`. On entering `merging`, the orchestrator gathers the
surviving worker branches (status `done`) and their summaries, spawns one merger subprocess in a
`wrap/<run>/merge` worktree, and on `report_done` records the merge result and advances to `merge_gate`.
Under the `AutoAdvanceGates` scaffold, `merge_gate → done` auto-resolves, and a `run_done` event is recorded
("basic emission"). The merged branch and summary become queryable via `GET /runs/{id}`.

Emission scope (user decision): **Minimal** — events table + `run_done` event + API exposure. The full
adapter long-poll subscription (spec step 7) is explicitly deferred.

## Key design decisions

1. **No `merges` table.** The canonical schema (spec) has none. The merge result is recorded in the `events`
   table as a `merge_done` event (payload: summary). The merged branch is `wrap/<run>/merge` (derivable).
2. **`merging` is driven unconditionally; `merge_gate → done` is gated by `AutoAdvanceGates`.** Merging is
   automatic work, not a gate. `driveMerger` self-guards: if `MergerCmd == nil` it no-ops (run rests at
   `merging`), so Phase 3 unit tests that reach `merging` without a merger configured still pass.
3. **Merger configured like planner/worker:** `MergerCmdFunc(context)` + `--merger-cmd`/`--merger-env`
   (default `claude`). The merger gets the surviving branches + summaries + `verification_command` on stdin.
4. **Daemon does NOT run verification.** Per spec "daemon never parses output for meaning," the merger
   subprocess runs `verification_command` itself; the daemon only trusts its `report_done`/exit code.
5. **Merge terminal predicate** mirrors the worker predicate: `report_done` AND exit 0 → `merge_done`;
   anything else → `merge_failed` → `failed`.
6. **Merger worktree/branch retained** (the output artifact), like worker worktrees.
7. **Worker summaries threaded via `worker_done` events.** Phase 3's `runWorker` is retrofitted to emit a
   `worker_done`/`worker_blocked` event (the events table's first use), giving the merger real per-branch
   context and seeding the emission story.
8. **FSM:** `merging --merge_done--> merge_gate`, `merging --merge_failed--> failed`,
   `merge_gate --gate_approve--> done`.

## Tasks

1. **FSM transitions** — `merging→merge_gate`, `merging→failed`, `merge_gate→done`; table-driven tests.
2. **store events** — `Event` struct, `InsertEvent`, `ListEventsByRun`, `LatestEventByKind` (ErrNotFound) + tests.
3. **orchestrator: worker_done events** — `interpretWorkerOutcome` also returns the done summary; `runWorker`
   emits a `worker_done`/`worker_blocked` event. Extend Phase 3 tests.
4. **orchestrator/merger.go** — `MergerCmdFunc`, Config fields; `driveMerger` (gather survivors, worktree,
   spawn, interpret, record `merge_done` event, advance `merge_gate`); `driveMergeGate` (auto `merge_gate→done`
   + `run_done` event). `Tick` drives `merging` always and `merge_gate` under `AutoAdvanceGates`. Unit tests.
5. **wrapd wiring** — `--merger-cmd`/`--merger-env`; pass `MergerCmd` to Config.
6. **API** — `GetRunResponse` gains `merge_summary` + `merge_branch`; `handleGetRun` sources them from the
   latest `merge_done` event. Handler test.
7. **integration** — extend the worker happy-path test to configure a merger and assert it reaches `done`
   with a merge summary exposed; add a merger-failure test (merger exits nonzero → `failed`).
8. **docs** — update CLAUDE.md (Phase 4 packages/conventions) and this plan.
