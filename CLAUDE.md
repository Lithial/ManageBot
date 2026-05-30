# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

`wrap` is a Go-based orchestrator that wraps the `claude` Code CLI to run multi-agent software-development swarms. A user submits a project (via CLI, spec file, or GitHub issue); the orchestrator plans the work, spawns parallel `claude` workers in isolated git worktrees, merges their output, and emits a result. The internal product name is `wrap` (binaries: `wrap` CLI, `wrapd` daemon); the GitHub repo is `Lithial/ManageBot`. Use `wrap` in code and docs unless referring to the repo URL.

**Substrate:** wraps `claude -p` subprocesses, not the SDK or raw API. Workers must remain debuggable as standalone `claude -p` sessions.

**Status:** Phase 1 (skeleton) is merged. The full design lives in `docs/superpowers/specs/2026-05-26-claude-swarm-wrapper-design.md`; each phase gets its own plan under `docs/superpowers/plans/`. Read the spec before extending architecture; read the relevant phase plan before starting implementation work.

Phase 2 (FSM + planner phase) is on branch `phase-2-fsm-and-planner`: introduces `internal/fsm` (pure phase transitions), `internal/worktree` (git plumbing), `internal/workerrpc` (NDJSON protocol mirroring the planned MCP tool surface), `internal/supervisor` (one-shot subprocess + RPC collection), and `internal/orchestrator` (polling loop that drives `pending → planning → plan_gate`). The planner subprocess is configurable via `wrapd --planner-cmd`; integration tests point it at `fake-claude` with `--planner-env FAKE_CLAUDE_SCRIPT=...` and `--tick-interval 100ms`.

Phase 3 (worker phase) is on branch `phase-3-worker-phase` (built atop phase 2): the orchestrator now drives `plan_gate → working → merging | failed`. New pieces: `internal/orchestrator/tasks.go` (parse + validate the plan's `tasks_json` DAG — unique ids, deps exist, acyclic), `scheduler.go` (pure DAG scheduler honoring a concurrency cap with transitive failure propagation, injectable `runTaskFunc`), `workers.go` (`driveWorkers`: a worktree + worker subprocess per task, outcome → terminal status, persisted `workers` rows), and `internal/store/workers.go`. Worker subprocess is configured via `wrapd --worker-cmd`/`--worker-env`/`--max-workers`; the happy path **terminates at `merging`** (the merger is Phase 4). See `docs/superpowers/plans/2026-05-30-phase-3-worker-phase.md`.

Phase 4 (merger phase + basic emission) is on branch `phase-4-merger` (built atop phase 3): the orchestrator now drives `merging → merge_gate → done`. New pieces: `internal/store/events.go` (the `events` table — `InsertEvent`/`ListEventsByRun`/`LatestEventByKind` — first real use of the forensic/emission log), `internal/orchestrator/merger.go` (`driveMerger`: gather surviving worker branches + summaries, spawn the merger in a retained `wrap/<run>/merge` worktree, record a `merge_done` event, advance to `merge_gate`; `driveMergeGate`: auto `merge_gate → done` + `run_done` event). Worker outcomes are now recorded as `worker_done`/`worker_blocked`/`worker_failed` events. The merger subprocess is configured via `wrapd --merger-cmd`/`--merger-env`; `GET /runs/{id}` exposes `merge_branch`/`merge_summary` (sourced from the `merge_done` event). Emission is **minimal** (events + API); the spec's step-7 adapter long-poll subscription is deferred. See `docs/superpowers/plans/2026-05-31-phase-4-merger.md`.

## Commands

```bash
# Build all three binaries into ./bin/
make build

# Build one binary
make wrap          # CLI client
make wrapd         # daemon
make fake-claude   # scripted shim used by integration tests

# Tests
make test-unit                            # go test ./...
make test-integration                     # builds binaries first, then runs tagged tests
go test ./internal/store/ -run TestName   # single unit test
go test -tags=integration ./test/integration/... -run TestName -v   # single integration test
go test ./... -race                       # race detector

# Clean built binaries
make clean
```

Go toolchain is pinned to **1.25.0** via `.tool-versions` (asdf). `go vet ./...` on an empty module returns exit 1 — use `go list -m` to sanity-check the module instead.

## Architecture

### Process model

`wrapd` is a long-lived daemon owning all state. Clients (`wrap` CLI, future TUI, intake adapters) talk to it over a Unix socket at `$XDG_RUNTIME_DIR/wrap.sock` (falls back to `$TMPDIR/wrap.sock`). State dir defaults to `~/.wrap/` (override with `WRAP_STATE_DIR`); SQLite lives at `<state-dir>/wrap.db`. Future workers (`claude -p` subprocesses) will run in `git worktree`-isolated dirs and report progress over MCP back to the daemon.

### Package layering (strict — preserve when extending)

- `cmd/*/main.go` — wiring only, no business logic.
- `internal/store/` — owns all SQL and the `*sql.DB`. **No other package imports `database/sql` directly.** Schema is embedded from `schema.sql` via `go:embed` and applied on `Open()`.
- `internal/api/` — `http.Server` over `net.UnixListener`. Handlers are thin: delegate persistence to `store`, use `intake` DTOs for wire serialization.
- `internal/intake/` — canonical DTOs (`SubmitRunRequest`, etc.) **and** the `RunSubmitter` interface. The API and the CLI adapter both use these types so the contract lives in one place.
- `internal/client/` — HTTP client over the Unix socket; mirrors `api/` from the caller side. `*client.Client` implicitly satisfies `intake.RunSubmitter`.
- `internal/ids/` — ULID helper (`ids.New()`); use for all primary keys.
- `internal/testutil/` — test infrastructure (see below).

### Two integration-test strategies

- `testutil.StartInProcessServer(t)` — spins up a real `api.Server` + `store.Store` inside the test process. Use for tests that exercise daemon logic without needing a separate binary.
- `testutil.StartTestDaemon(t, wrapdBinary)` — spawns the actual `wrapd` binary. Use for true end-to-end tests (e.g. CLI → real daemon → DB). Requires `make wrapd` to have run; `testutil.LocateBinary("wrapd")` walks up to find `./bin/`.

The two helpers are intentionally distinct; don't unify them.

### Project conventions worth knowing

- **Error sentinels at boundaries.** Store exports `store.ErrNotFound`; callers use `errors.Is(err, store.ErrNotFound)` rather than `sql.ErrNoRows`. This keeps `database/sql` confined to the store package.
- **`store.Project.VerificationCommand` is plain `string`**, not `sql.NullString` — emptiness, not nullability, is the semantic.
- **`api.Server.Ready() <-chan struct{}`** — fires once the Unix socket is bound. `wrapd`'s startup banner waits on this so "listening on..." only prints when the socket is actually accept-ready. Preserve this when adding startup work.
- **Default gates JSON** is embedded as a literal in `api/handlers.go` (`findOrCreateProject`). When the gate engine lands, that literal should move into a typed config.
- **`fake-claude`** is env-driven (`FAKE_CLAUDE_EXIT_CODE`, `FAKE_CLAUDE_SLEEP_MS`, `FAKE_CLAUDE_STDOUT`, `FAKE_CLAUDE_STDERR`). Later phases will extend it to emit scripted MCP tool calls — keep it env-driven, no flags.
- **`--planner-env` values cannot contain commas.** The wrapd flag parses comma-separated `K=V` pairs; values with commas will be silently truncated. If future test fixtures need comma-bearing env values, switch the flag to a repeated `--planner-env` pattern.
- **Worker-RPC over NDJSON, not real MCP yet.** `internal/workerrpc` uses method names (`report_progress`, `report_plan`, `report_done`, `report_blocked`) chosen to match the eventual MCP tool surface. When real MCP lands (Phase 9), swap the transport, keep the method names. `DecodeAll` returns a `malformed` count — non-zero means a worker protocol bug; the orchestrator logs it.
- **`--planner-cmd` is bare path only in Phase 2.** No args support. Phase 9 will introduce a richer template with `--mcp wrap --append-system-prompt <planner.md>` args.
- **Process-group kill for worker subprocesses.** `internal/supervisor` sets `SysProcAttr.Setpgid = true` and kills via `syscall.Kill(-pid, SIGKILL)` so shell-wrapper grandchildren don't leak pipe handles. POSIX-only; the project intentionally has no Windows target.
- **Orchestrator writes plan BEFORE phase update.** In `drivePlanner`, `InsertPlan` runs before `UpdateRunPhase("plan_gate")`. Polling clients that condition on `phase == plan_gate` can rely on `plan_md` being present — there is no observable window where the phase has advanced but the plan is missing.
- **`AutoAdvanceGates` is a Phase 3 scaffold, not the gate policy.** There is no gate engine until Phase 5, so the orchestrator only drives `plan_gate → working` when `--auto-advance-gates` is set; default off keeps runs resting at `plan_gate` (matching Phase 2 and the spec's `plan: require_approval` default). Phase 5 replaces this boolean with real per-run gate evaluation — delete it then.
- **Worker worktrees/branches are RETAINED on every path.** `driveWorkers` never calls `worktree.Remove` — surviving worker branches are the merger's inputs (Phase 4), and the spec forbids deleting failed worktrees until `wrap prune`. This is the deliberate opposite of `drivePlanner`, which removes its worktree after persisting the plan.
- **Worker terminal predicate lives in `interpretWorkerOutcome`.** `report_done` AND exit 0 → `done`; `report_blocked` → `failed` (blocked wins even if `report_done` was also emitted — it asked for human judgment); anything else (nonzero exit, or exit 0 without `report_done`) → `failed`. The daemon never parses worker stdout for meaning beyond these RPC methods.
- **`max_workers` is a daemon flag (`--max-workers`, default 4), NOT a schema column yet.** The spec wants it per-run defaulted from project; deferred to avoid a migration. Phase 5 should move it into per-run config alongside gates.
- **`git worktree add` is serialized** in `driveWorkers` via a mutex (repo-wide ref/index locks collide under parallel adds). Only the quick plumbing serializes; worker subprocesses still run concurrently under the cap.
- **`store.Worker.ExitCode` is `*int64`**, not `int64` — exit code 0 (success) must be distinguishable from "not yet finished" (NULL). Contrast the `0 = unset` sentinel used for `PID`/`StartedAt`/`EndedAt`, where 0 is never a real value.
- **No `merges` table — the merge result lives in the `events` log.** Per the canonical schema (no merges table), `driveMerger` records a `merge_done` event (payload `{branch, summary}`); `GET /runs/{id}` reads the latest one for `merge_branch`/`merge_summary`. The merged branch is always `wrap/<run>/merge` (derivable). Worker outcomes are likewise events (`worker_done`/`worker_blocked`/`worker_failed`); the merger reads `worker_done` summaries to build its context.
- **`merging` is driven unconditionally; only the gate crossings are behind `AutoAdvanceGates`.** Merging is automatic work, so `Tick` always calls `driveMerger`, which self-guards when `MergerCmd == nil` (run rests at `merging`). The `plan_gate → working` and `merge_gate → done` crossings are the gated steps. So `--merger-cmd` defaults to `claude` (like planner/worker), but a run still won't leave `merging` without `--auto-advance-gates` reaching it first.
- **The daemon never runs `verification_command`.** Per "daemon never parses output for meaning," the merger *subprocess* runs verification and only reports done if it passes; the daemon passes the command in the merge context (stdin) and trusts the merger's `report_done`/exit code. `driveMerger` reuses `interpretWorkerOutcome` — the merger's done-predicate is identical to a worker's.
- **`testutil.StartInProcessServerWithStore`** returns the backing `*store.Store` alongside the socket, so handler tests can seed state (events, etc.) that has no API write path. `StartInProcessServer` delegates to it. Still distinct from `StartTestDaemon` (external binary) — don't unify those.

### Adapter-pattern intake

CLI is the only adapter today (`intake/cli.go`). Specfile and GitHub adapters are planned. All adapters produce a `SubmitRunRequest` and call `RunSubmitter.SubmitRun`. When adding an adapter, do not bypass the API by talking to the store directly — go through the socket like every other client.

### What is NOT in scope (per spec)

Multi-tenant/multi-user, remote workers, recursive worker spawning, agentic orchestration of supervision (the FSM is deterministic Go), cross-project plan portability. Don't add abstraction for any of these without spec revision.

## Development workflow

This repo uses the `superpowers` skill set heavily. The user prefers extensible/adapter designs over narrow MVPs — when offering options, lead with the flexible one. TDD with frequent commits is the norm; phase plans are structured as bite-sized test-first tasks.
