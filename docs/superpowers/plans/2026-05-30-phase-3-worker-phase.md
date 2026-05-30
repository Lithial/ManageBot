# wrap Phase 3: Worker Phase (worktrees + concurrency cap)

> Compact plan. Phase 2 (`phase-2-fsm-and-planner`) is the base. TDD per task, commit per task.

**Goal:** Drive a run autonomously `plan_gate → working → merging | failed`. On entering `working`, the
orchestrator reads the persisted plan's `tasks_json`, topologically schedules worker subprocesses under a
concurrency cap, isolates each in its own git worktree/branch, collects each worker's terminal status over the
existing worker-RPC protocol, and advances the FSM: `merging` if ≥1 worker is `done`, else `failed`.

No merger (Phase 4) and no gate engine (Phase 5) yet — so the happy path **terminates observably at `merging`**
(workers done, waiting for a merger that does not exist yet), exactly as Phase 2 terminated at `plan_gate`.

## Key design decisions

1. **`plan_gate` auto-advances to `working`.** No gate engine until Phase 5, so the orchestrator treats every
   `plan_gate` run as auto-approved (spec's `gates.plan.mode == "auto"`). Phase 5 inserts the approval check
   *before* emitting `work_start`. FSM gains `plan_gate --work_start--> working`.
2. **FSM additions:** `working --work_done--> merging`, `working --work_failed--> failed`.
3. **Synchronous driving within `Tick`.** `driveWorkers` blocks on the whole working phase (spawn → wait-all →
   advance), mirroring `drivePlanner`'s blocking style. No cross-tick PID reconciliation in Phase 3 (that is
   Phase 8). One run's working phase is processed to completion before the next.
4. **`max_workers` is a daemon-level config (`--max-workers`, default 4)**, NOT a schema column yet. The spec
   wants it per-run defaulted from project; deferring that to avoid a migration. Phase 5 moves it into config.
5. **Worker subprocess is configured like the planner:** `WorkerCmdFunc(taskDescription)` factory +
   `--worker-cmd` / `--worker-env` flags. Task description goes on stdin (mirrors planner spec-on-stdin).
6. **Worker terminal predicate (per spec "done predicate"):**
   - `report_done` AND exit 0 → `done`
   - `report_blocked` → `failed` (reason recorded as event/log)
   - any other outcome (nonzero exit, or exit 0 without `report_done`) → `failed`
7. **Worker worktrees/branches are RETAINED** (not removed) — they are the merger's inputs in Phase 4, and the
   spec forbids deleting failed worktrees ("until `wrap prune`"). Contrast `drivePlanner`, which removes the
   plan worktree after persisting the plan.
8. **Worker branch base = `HEAD`** (same base the planner used; planner makes no commits so plan branch == HEAD).
9. **Failure propagation:** a task whose dependency ended `failed` is marked `failed` without spawning
   (transitive). Scheduler enforces this purely.
10. **`git worktree add` is serialized** via a mutex in `driveWorkers` (git ref/index locks collide under
    parallel adds); only the quick plumbing serializes — the worker subprocesses still run in parallel.

## Tasks

1. **FSM transitions** — add `plan_gate→working`, `working→merging`, `working→failed`; table-driven tests.
2. **workerrpc done/blocked** — `DoneParams{Summary}`, `BlockedParams{Reason}`, `AsDone`, `AsBlocked` + tests.
3. **fake-claude actions** — `{"kind":"done","summary":...}`, `{"kind":"blocked","reason":...}` + tests.
4. **store workers** — `Worker` struct, `InsertWorker`, `FinishWorker`, `ListWorkersByRun` + tests.
5. **orchestrator/tasks.go** — `Task{ID,Title,DependsOn}`, `parseTasks` (validate: unique ids, deps exist,
   acyclic) + pure tests.
6. **orchestrator/scheduler.go** — `schedule(ctx, tasks, maxConcurrent, runFunc) map[string]status` honoring
   the DAG, the cap, and failure propagation; pure tests with an injected `runFunc`.
7. **orchestrator/workers.go** — `driveWorkers`: worktree + supervisor per task, interpret outcome, persist
   worker rows, advance FSM; `Tick` now also polls `plan_gate`. Unit test with fake-claude.
8. **wrapd wiring** — `--max-workers`, `--worker-cmd`, `--worker-env`; pass `WorkerCmd`/`MaxWorkers` to Config.
9. **integration test** — end-to-end `pending → … → merging` with fake-claude planner + worker scripts.
10. **docs** — update CLAUDE.md (Phase 3 packages/conventions) and this plan's checkboxes.
