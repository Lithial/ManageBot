# wrap — A Claude Swarm Orchestrator for Software Projects

**Status:** Design (brainstorming complete, implementation plan pending)
**Date:** 2026-05-26
**Owner:** jameshessell@gmail.com

## Summary

`wrap` is a Go-based CLI + daemon that wraps the `claude` Code CLI to run multi-agent swarms against software projects. A single user submits a project (via interactive CLI, spec file, or GitHub issue); the orchestrator plans the work, spawns parallel `claude` workers in isolated git worktrees, merges their output, and emits the result (local branch, PR, etc.). A TUI shows the swarm in real time.

The system is built around a long-lived daemon (`wrapd`) that owns all state in SQLite. Clients (CLI, TUI, intake adapters) communicate over a Unix socket. Workers communicate with the daemon over MCP, calling typed tools to report progress, request approval, or signal completion.

## Goals

- Run real software-development swarms end-to-end: plan → parallel work → merge → emit.
- Wrap the `claude` CLI as the worker substrate (not the SDK; not the raw API). Workers are real, debuggable `claude -p` processes.
- Isolate parallel workers using git worktrees (no shared-file races).
- Allow configurable human-in-the-loop approval gates per project (fully autonomous on one end, gated-at-every-phase on the other).
- Provide a TUI for live observation (worker status, logs, gate approvals).
- Be resumable across daemon restarts.

## Non-goals

- **Multi-tenant / multi-user.** Single local user, single machine.
- **Remote / distributed workers.** Worktrees only, for now. Design preserves the interface so containers/remote sandboxes could drop in later.
- **Recursive decomposition.** Workers cannot spawn workers in the initial design. May revisit (see Future Work).
- **Agentic orchestration of supervision itself.** The daemon's supervision logic is deterministic Go code, not a Claude session. (The *planner* and *merger* are Claude sessions — but spawning, gating, and merging *control flow* are Go.)
- **Cross-project portability of plans.** Each run is project-scoped.
- **A general-purpose agent platform.** Software development only; intake adapters are software-shaped.

## Substrate decisions

| Decision           | Choice                              | Reason                                                                             |
| ------------------ | ----------------------------------- | ---------------------------------------------------------------------------------- |
| Project domain     | Software dev (PRs, features, bugs)  | Most well-trodden territory; clearest success criteria                             |
| Wrap target        | `claude` Code CLI                   | Gets tools, permissions, hooks, MCP for free; workers are debuggable as plain sessions |
| Swarm shape        | Hybrid: planner → parallel workers → merger | Matches how real software is shipped; cleanly maps to FSM phases                   |
| Worker isolation   | Git worktrees per worker            | Cheap, same-machine, shared object DB, standard git plumbing                       |
| Intake             | Adapter pattern (CLI / specfile / GitHub) | Single internal `Project` type fed by multiple front-ends                          |
| HITL gates         | Configurable per project            | Different projects need different oversight; not one-size-fits-all                 |
| Observability      | TUI dashboard (Bubble Tea)          | Terminal-native, fast, no browser dependency                                       |
| Implementation lang | Go                                  | Single-binary distribution, strong subprocess supervision, Bubble Tea for TUI      |

## Architecture

### Process model

```
┌──────────────────────────────────────────────────────────────────┐
│  wrap CLI / TUI / GH-webhook-listener  (clients)                 │
│  - `wrap run <spec.md>`                                          │
│  - `wrap attach <run-id>`     ── all talk to ──┐                 │
│  - `wrap tui`                                  │                 │
└────────────────────────────────────────────────┼─────────────────┘
                                                 ▼
                          ┌──────────────────────────────────────┐
                          │  wrapd  (the daemon, Go)             │
                          │  • Unix socket: $XDG_RUNTIME_DIR/wrap.sock
                          │  • SQLite:      ~/.wrap/wrap.db      │
                          │  • Worktree manager (git plumbing)   │
                          │  • Worker supervisor (os/exec)       │
                          │  • Gate engine (per-project policy)  │
                          │  • Phase state machine               │
                          └────────────┬─────────────────────────┘
                                       │ spawns + supervises
              ┌────────────────────────┼────────────────────────┐
              ▼                        ▼                        ▼
       ┌──────────────┐         ┌──────────────┐         ┌──────────────┐
       │  worker 1    │         │  worker 2    │   ...   │  worker N    │
       │  cwd: wt-1/  │         │  cwd: wt-2/  │         │  cwd: wt-N/  │
       │  claude -p   │         │  claude -p   │         │  claude -p   │
       │   --mcp wrap │         │   --mcp wrap │         │   --mcp wrap │
       └──────┬───────┘         └──────┬───────┘         └──────┬───────┘
              │                        │                        │
              └─── MCP over stdio ─────┴────────────────────────┘
                       (workers as MCP clients of wrapd)
```

### Components of `wrapd`

| Component             | Responsibility                                                                            |
| --------------------- | ----------------------------------------------------------------------------------------- |
| **API server**        | Unix-socket JSON-RPC for clients; MCP server endpoint for workers                         |
| **State store**       | SQLite — projects, runs, phases, workers, events, plans, gates                            |
| **Worktree manager**  | `git worktree add/remove` per worker on `wrap/<run>/<wid>` branches                       |
| **Supervisor**        | `os/exec` lifecycle: spawn, watch, kill, restart with budget, reap zombies                |
| **Phase FSM**         | `pending → planning → plan_gate → working → merging → merge_gate → done/failed/killed`    |
| **Gate engine**       | Evaluates per-project gate policy; blocks transitions; surfaces approval prompts          |
| **Intake adapters**   | `cli`, `specfile`, `github` — each produces a canonical `Project` and submits via API     |

### Why a daemon

Long-running supervision plus multiple clients (CLI, TUI, attach-later) plus the desire to survive terminal disconnects. Anything else means re-implementing this every command.

### Why MCP for worker↔daemon communication

A Claude worker can call `wrap.report_progress`, `wrap.report_done`, `wrap.report_blocked` as **typed tools** instead of emitting parseable freeform text. The daemon's MCP server exposes a small, fixed schema that's easy to evolve.

## Data model

### SQLite schema (canonical)

```sql
CREATE TABLE projects (
  id                   TEXT PRIMARY KEY,
  name                 TEXT NOT NULL UNIQUE,
  repo_path            TEXT NOT NULL,
  default_gates_json   TEXT NOT NULL,
  verification_command TEXT,                     -- shell command run by merger to validate the merged branch (e.g. 'make test'). NULL = no automated verification.
  created_at           INTEGER NOT NULL
);

CREATE TABLE runs (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL REFERENCES projects(id),
  intake_kind  TEXT NOT NULL,                    -- 'cli' | 'specfile' | 'github'
  intake_ref   TEXT,
  spec_md      TEXT NOT NULL,
  gates_json   TEXT NOT NULL,
  phase        TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE plans (
  id           TEXT PRIMARY KEY,
  run_id       TEXT NOT NULL REFERENCES runs(id),
  plan_md      TEXT NOT NULL,
  tasks_json   TEXT NOT NULL,                    -- [{id, title, description, files_hint, depends_on}]
  approved_at  INTEGER,
  created_at   INTEGER NOT NULL
);

CREATE TABLE workers (
  id            TEXT PRIMARY KEY,
  run_id        TEXT NOT NULL REFERENCES runs(id),
  task_id       TEXT NOT NULL,
  branch        TEXT NOT NULL,                   -- wrap/<run>/<wid>
  worktree_path TEXT NOT NULL,
  pid           INTEGER,
  status        TEXT NOT NULL,                   -- 'pending'|'running'|'done'|'failed'|'killed'
  exit_code     INTEGER,
  started_at    INTEGER,
  ended_at      INTEGER
);

CREATE TABLE events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id       TEXT NOT NULL REFERENCES runs(id),
  worker_id    TEXT REFERENCES workers(id),
  kind         TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  ts           INTEGER NOT NULL
);

CREATE TABLE gates (
  id           TEXT PRIMARY KEY,
  run_id       TEXT NOT NULL REFERENCES runs(id),
  kind         TEXT NOT NULL,                    -- 'plan' | 'merge' | 'worker_done' | 'worker_blocked' | 'merge_conflict' | 'custom'
  status       TEXT NOT NULL,                    -- 'pending' | 'approved' | 'rejected' | 'auto-approved'
  payload_json TEXT NOT NULL,
  resolved_by  TEXT,
  resolved_at  INTEGER,
  created_at   INTEGER NOT NULL
);
```

### Phase state machine

```
                ┌────────────────────────────────────────────────────────┐
                │                                                        ▼
   pending ──► planning ──► plan_gate ──► working ──► merging ──► merge_gate ──► done
      │                       │                                      │
      │                       │ rejected                             │ rejected
      │                       ▼                                      ▼
      └─────────────────► failed ◄─────── any worker fatal ◄─────────┘
                           ▲
                           │ user `wrap kill <run>` from any state
                          killed
```

- **pending**: row created by an intake adapter; not yet picked up.
- **planning**: planner Claude session running (single `claude -p` in a worktree against the spec).
- **plan_gate**: blocks until a `gates` row of kind=`plan` resolves (auto-resolved if policy allows).
- **working**: workers spawned in parallel under `max_workers` cap; transitions to `merging` when **all** worker rows are terminal AND at least one succeeded.
- **merging**: merger Claude session runs in its own worktree, taking surviving worker branches as inputs, producing a single merged branch on `wrap/<run>/merge`.
- **merge_gate**: blocks until merge gate resolves.
- **done**: merged branch retained; intake adapter notified for emission.
- **failed/killed**: terminal; worktrees and events retained for inspection.

### Gate policy

`gates_json` per run, defaulted from project:

```json
{
  "plan":        { "mode": "require_approval" },
  "worker_done": { "mode": "auto" },
  "merge":       { "mode": "require_approval" },
  "custom":      []
}
```

Modes: `require_approval` | `auto`.

Note: the policy keys above (`plan`, `worker_done`, `merge`) configure *automatic* gate kinds. Gate kinds `worker_blocked` and `merge_conflict` are always created when their triggering condition fires (a worker calling `wrap.report_blocked` or a merger reporting a conflict); they cannot be auto-approved because they exist precisely because something needs human judgment.

### Worker MCP surface

The complete tool set workers can call:

| Tool                          | Purpose                                                        |
| ----------------------------- | -------------------------------------------------------------- |
| `wrap.report_progress(msg)`   | Free-text status line shown in the TUI                         |
| `wrap.report_done(summary)`   | Worker declares its task complete; transitions worker to `done` |
| `wrap.report_blocked(reason)` | Worker is stuck and needs human help; opens a gate              |
| `wrap.read_task()`            | Returns the task description for this worker                    |
| `wrap.list_sibling_tasks()`   | Returns titles of sibling workers' tasks (read-only)            |

Deliberately small. Workers do not spawn other workers or trigger merges — those are daemon-level decisions.

## Runtime behavior

### End-to-end happy path

1. **Intake.** User runs `wrap run spec.md` (or a GitHub webhook fires). The relevant adapter normalizes the input and POSTs to `wrapd`. The daemon inserts a `projects` row if needed and a `runs` row in `phase=pending`. Emits `run_created`.
2. **Planning.** FSM transitions `pending → planning`. Daemon spawns a planner worker: branch `wrap/<run>/plan`, worktree under `~/.wrap/runs/<run>/plan/`, command `claude -p --mcp wrap --append-system-prompt <planner.md>`, spec on stdin. The planner calls `wrap.report_done(plan_md, tasks_json)`. The daemon inserts a `plans` row; FSM moves to `plan_gate`.
3. **Plan gate.** If `gates.plan.mode == "auto"`, the daemon auto-resolves and advances. Otherwise it creates a pending `gates` row, emits `gate_requested`, and the TUI surfaces an approval prompt. On approve, FSM advances to `working`.
4. **Working.** Daemon reads `plan.tasks_json`, topologically sorts by `depends_on`. Loop: while any task is pending and `running < max_workers`, pick the next ready task, create a worktree on a fresh `wrap/<run>/<wid>` branch from the plan's base, insert a `workers` row, and spawn `claude -p --mcp wrap --append-system-prompt <worker.md>` with the task description on stdin. Wait until every worker reaches a terminal state. If at least one worker is `done`, FSM moves to `merging`; if zero, FSM moves to `failed`.
5. **Merging.** Daemon spawns one merger worker: branch `wrap/<run>/merge`, given the list of successful worker branches and their summaries, full `wrap.*` MCP surface plus shell git. The merger rebases/merges worker branches in, resolves what it can, asks the human via `wrap.report_blocked` for anything it can't, runs the project's `verification_command` if one is set (skips this step if NULL), and calls `wrap.report_done(merge_summary)`. FSM moves to `merge_gate`.
6. **Merge gate.** Same shape as the plan gate. On approve, FSM advances to `done`.
7. **Emission.** Each intake adapter registers an "emission handler" with the daemon when it submits a run. On `merge_gate → done` the daemon dispatches an `emission_requested` event to the handler over the same Unix socket (long-poll subscription, scoped to that run). CLI adapter prints the branch name; specfile adapter writes a `DONE` sidecar next to the spec; GitHub adapter pushes the branch and opens a PR with the merge summary. If the adapter is not currently connected (e.g. CLI exited), the emission is queued and retried on `wrap emit <run-id>`.

### Concurrency model

- `max_workers` (default: 4) caps simultaneous worker processes per run.
- The supervisor maintains a ready-queue based on the plan's `depends_on` DAG.
- Tasks with unsatisfied dependencies stay pending until predecessors hit `done`.
- A failed worker propagates: any task transitively depending on it is marked `failed` without being spawned; the merger sees only the surviving subgraph.

### The "done" predicate

A worker is **terminal** if and only if one of:

1. It called `wrap.report_done(...)` AND the process subsequently exits 0 → status `done`.
2. It called `wrap.report_blocked(...)` → status `failed` with the reason recorded.
3. Process exited non-zero without calling either → status `failed` with the exit code recorded.
4. User killed it from the TUI → status `killed`.

The working phase completes when every worker row is in `{done, failed, killed}`. Merging proceeds only if at least one is `done`.

### Daemon never parses worker output for meaning

Stdout is captured for TUI tailing and forensics, but state transitions only happen via MCP calls or process exit. This keeps the supervisor's logic clean and the contract typed.

### Resumability

All state lives in SQLite plus on-disk worktrees, so `wrapd` can crash and restart without losing a run. On startup, `wrapd`:

1. Loads any runs not in `{done, failed}`.
2. Reconciles supervisor state: workers with `status=running` but no matching PID are marked `failed` with reason `daemon_restart` (their context is gone; resuming would be unsafe).
3. The phase FSM re-evaluates and either advances or waits on the same gates as before.

`wrap attach <run-id>` after a restart shows the run exactly where it left off.

## Failure modes

| Class                                 | Detect                                              | Contain                                              | Recover                                                 | Report                                            |
| ------------------------------------- | --------------------------------------------------- | ---------------------------------------------------- | ------------------------------------------------------- | ------------------------------------------------- |
| Worker crashes (non-zero exit)        | `cmd.Wait()` non-zero                                | mark `failed`                                        | retry up to `worker_retry_budget` (default 1)            | `worker_failed` event + last 100 log lines        |
| Worker hangs (no progress)            | no MCP call AND no stdout for `worker_idle_timeout` | SIGTERM → SIGKILL after 30s grace                    | retry budget applies                                    | `worker_timeout` with last-activity timestamp     |
| Worker context exhausted              | exit code or "context full" stderr pattern          | mark `failed:context_exhausted`                      | no auto-retry; escalate as gate                          | `worker_context_exhausted`                        |
| Worker calls `report_blocked`         | MCP call arrives                                    | keep process alive briefly, then exit                | open gate `kind=worker_blocked`; human decides           | `worker_blocked` with reason                      |
| MCP socket disconnect (worker side)   | MCP server detects client EOF                       | worker no longer reporting                            | falls through to crash/timeout handling                  | `worker_mcp_lost`                                 |
| Daemon crash                          | n/a (process gone)                                  | OS reaps children                                     | systemd/launchd restart; reconciler marks orphans `failed:daemon_restart` | `daemon_recovered` startup event                  |
| Git operation fails                   | git non-zero                                         | abort spawn                                          | mark task `failed` with git error                        | `worktree_failed`                                 |
| Merge conflict in merging phase       | merger calls `wrap.report_blocked:merge_conflict`    | merger kept alive for inspection                     | gate `merge_conflict`; options: abort, drop branch, human takeover | `merge_conflict` with file list              |
| Verification fails after merge        | merger's test command non-zero                       | merge_summary records failure; do not enter merge_gate | back to merging with failure as input                    | `verification_failed`                             |
| Intake-adapter emission fails         | adapter returns error                                | run stays `done` (work fine); emission marked failed | manual `wrap emit <run>` retries                         | `emission_failed`                                 |
| SQLite write failure                  | sql error from write path                            | refuse transition; FSM holds                         | retry with backoff                                       | log + TUI banner: degraded                        |
| Concurrent client mutations           | optimistic lock on `runs.updated_at`                 | reject second mutation with 409                      | client refetches and retries if appropriate              | transparent to TUI                                |

### Hard guarantees

- **No silent stalls.** Every running worker has a deadline; every transition either advances within a bounded time or surfaces a gate. After `run_stale_timeout` (default 1h) with no activity, the daemon emits `stale_run_detected`.
- **No data loss on the work side.** Failed/killed worker worktrees are not deleted. They remain under `~/.wrap/runs/<run>/<wid>/` until explicit `wrap prune`. Branches likewise.
- **No surprise destructive operations.** Merger uses `--no-ff` merges by default. Worktrees are removed via `git worktree remove`, not `rm -rf`. Branches stay on the local repo until `wrap prune`.

### Cancellation semantics

`wrap kill <run-id>`:

1. FSM moves to terminal `killed`.
2. All workers get SIGTERM, SIGKILL after 30s grace.
3. Pending gates resolved as `rejected` with `resolved_by=killed_by_user`.
4. Worktrees and branches preserved for forensics.

`wrap prune <run-id>` is the destructive cleanup: only runs on terminal runs, removes worktrees, deletes `wrap/<run>/*` branches.

### Resource limits (all per-project overrideable)

| Limit                          | Default   | Why                                                    |
| ------------------------------ | --------- | ------------------------------------------------------ |
| `max_workers` (per run)        | 4         | Avoids hammering rate limits                           |
| `max_concurrent_runs` (daemon) | 2         | Same, at the daemon level                              |
| `worker_idle_timeout`          | 10 min    | Catches hangs without killing legitimately-thinking workers |
| `worker_max_runtime`           | 2 hours   | Hard ceiling                                            |
| `run_stale_timeout`            | 1 hour    | Emits `stale_run_detected` if no activity at all       |
| `worker_retry_budget`          | 1         | One retry on crash; beyond that, escalate              |

## Testing strategy

### Tier 1: unit tests (fast, no I/O)

FSM transition function, gate-policy evaluation, dependency-graph topological sort, "done" predicate, JSON parsing. Table-driven `go test`. Every transition has positive and negative tests; every retry/timeout branch is exercised.

### Tier 2: daemon integration tests (medium, real SQLite + real subprocesses)

Real `wrapd` against a temp SQLite file and a temp git repo. Replace `claude -p` with a **fake-claude shim** — a small Go binary built in test setup that reads a script from `FAKE_CLAUDE_SCRIPT`, performs scripted MCP calls (`wrap.report_progress`, `wrap.report_done`), and exits with the scripted code. Tests the full daemon flow with zero Anthropic API calls. Deterministic, runs in seconds.

Scenarios:

| Scenario                          | Asserts                                                            |
| --------------------------------- | ------------------------------------------------------------------ |
| Happy path, all workers succeed   | FSM reaches `done`; merger branch exists; emission fires           |
| One worker fails, two succeed     | merger runs on surviving 2; failed task surfaces in events         |
| All workers fail                  | FSM reaches `failed`; merger does NOT spawn                        |
| Plan gate rejected                | FSM moves to `failed`; no workers spawn                            |
| Worker timeout                    | SIGTERM sent; worker marked failed with `timeout`; retry honored   |
| Daemon restart mid-working        | orphan workers marked `failed:daemon_restart`; FSM picks up        |
| Concurrent client mutations       | second mutation gets 409; state stays consistent                   |
| Merge conflict                    | merger blocks; gate created; `wrap kill` resolves cleanly          |

### Tier 3: end-to-end smoke (slow, real `claude`, opt-in)

A single nightly smoke test running **one** real run against a known-trivial fixture (e.g. add `--version` flag) with `max_workers=2`. Asserts the run reaches `done` and the merged branch contains the expected change. Gated behind `//go:build e2e` and `WRAP_E2E_API_KEY`. Catches regressions the shim cannot.

### Deliberately untested

- Planner plan-quality (Claude behavior, not `wrap` behavior).
- Merger conflict-resolution correctness (same reason).
- TUI pixel output (test the Bubble Tea model; never snapshot rendered output).

### Test infrastructure (day-one deliverables)

- `cmd/fake-claude/` — the scripted shim binary.
- `internal/testutil/daemon.go` — `StartTestDaemon(t)` returning a configured `wrapd` and cleanup.
- `internal/testutil/repo.go` — `MakeTestRepo(t)` for ephemeral git fixtures.
- `Makefile` target `test-integration` that builds the shim, runs the integration tests, tears down.

## Future work (explicit non-goals today)

- **Recursive decomposition.** Workers requesting their own sub-runs. Requires nested merge handling, a tree-aware TUI, and revised concurrency accounting. Door left unlocked: `wrap.request_subplan` is the natural MCP-tool seam.
- **Remote / containerized workers.** Worker supervisor interface should stay thin enough to swap `os/exec` for a remote-process driver later.
- **Multi-user / hosted mode.** Would require auth, per-user state isolation, and a non-Unix-socket transport.
- **Plan-quality and merge-quality evals.** Separate concern; live in a separate eval harness if built.

## Open questions for the implementation plan

None blocking. Implementation plan (next step via `superpowers:writing-plans`) will sequence:

1. Skeleton: `wrapd` + Unix socket + SQLite + intake CLI adapter + fake-claude shim.
2. FSM + happy-path planner phase (no gates yet).
3. Worker phase with worktrees + concurrency cap.
4. Merger phase + basic emission.
5. Gate engine + plan/merge gates.
6. TUI on top of the existing API.
7. GitHub and specfile intake adapters.
8. Failure-mode coverage (timeouts, retries, daemon-restart reconciliation).
9. E2E smoke test against the real `claude` binary.
