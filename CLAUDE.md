# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

`wrap` is a Go-based orchestrator that wraps the `claude` Code CLI to run multi-agent software-development swarms. A user submits a project (via CLI, spec file, or GitHub issue); the orchestrator plans the work, spawns parallel `claude` workers in isolated git worktrees, merges their output, and emits a result. The internal product name is `wrap` (binaries: `wrap` CLI, `wrapd` daemon); the GitHub repo is `Lithial/ManageBot`. Use `wrap` in code and docs unless referring to the repo URL.

**Substrate:** wraps `claude -p` subprocesses, not the SDK or raw API. Workers must remain debuggable as standalone `claude -p` sessions.

**Status:** Phase 1 (skeleton) is merged. The full design lives in `docs/superpowers/specs/2026-05-26-claude-swarm-wrapper-design.md`; each phase gets its own plan under `docs/superpowers/plans/`. Read the spec before extending architecture; read the relevant phase plan before starting implementation work.

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

### Adapter-pattern intake

CLI is the only adapter today (`intake/cli.go`). Specfile and GitHub adapters are planned. All adapters produce a `SubmitRunRequest` and call `RunSubmitter.SubmitRun`. When adding an adapter, do not bypass the API by talking to the store directly — go through the socket like every other client.

### What is NOT in scope (per spec)

Multi-tenant/multi-user, remote workers, recursive worker spawning, agentic orchestration of supervision (the FSM is deterministic Go), cross-project plan portability. Don't add abstraction for any of these without spec revision.

## Development workflow

This repo uses the `superpowers` skill set heavily. The user prefers extensible/adapter designs over narrow MVPs — when offering options, lead with the flexible one. TDD with frequent commits is the norm; phase plans are structured as bite-sized test-first tasks.
