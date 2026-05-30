# wrap Phase 7: GitHub + Specfile Intake Adapters (+ pull emission)

> Compact plan. Base: `main` (Phase 6 merged). TDD per task, commit per task, push + PR at the end.

**Goal:** Two new intake adapters that produce a `SubmitRunRequest` and go through the socket like the CLI
adapter, plus a pull-based `wrap emit <run-id>` that performs the right emission per `intake_kind`.

- **Specfile adapter** (`wrap submit <spec.md>`): a spec file with optional `---` frontmatter
  (`project`/`repo`/`verification_command`); body is the spec. `intake_kind=specfile`,
  `intake_ref`=abs spec path. Emission writes a `<spec>.DONE` sidecar.
- **GitHub adapter** (`wrap github <issue-ref>`): fetch an issue's title/body via the `gh` CLI →
  `SubmitRunRequest` (`intake_kind=github`, `intake_ref`=issue URL, `spec_md`="# title\n\nbody"). Emission
  pushes the merge branch and opens a PR via `gh`.
- **Emission** (`wrap emit <run-id>`): read the run from the API; if `done`, dispatch by `intake_kind`:
  cli→print branch, specfile→write sidecar, github→push + PR. Pull-based; the spec's long-poll auto-dispatch
  is **deferred**.

## Key design decisions

1. **Adapter logic is pure + interface-driven, in `internal/intake`.** `GitHubAdapter` depends on an
   `IssueFetcher` interface; emission depends on small `EmitDeps` (sidecar writer, push+PR func, stdout).
   `cmd/wrap` supplies the real `gh`/`git` subprocess impls. Tests use fakes — no network, no `gh` needed.
2. **GitHub via the `gh` CLI**, not a Go GitHub library — reuses the user's auth, zero new deps.
3. **Frontmatter is minimal flat `key: value`** between leading `---` fences — no YAML dependency. Unknown
   keys are ignored; no frontmatter ⇒ whole file is the spec body.
4. **Emission is pull-based** via `wrap emit`; `--repo` (default cwd) gives the repo for the github push. Only
   the daemon-side addition is exposing `intake_kind`/`intake_ref` on `GET /runs/{id}` so `emit` knows what to do.
5. **All adapters go through `RunSubmitter.SubmitRun`** (the socket) — never the store directly (layering rule).

## Tasks

1. **API** — add `intake_kind`/`intake_ref` to `GetRunResponse` + handler; handler test.
2. **intake/specfile** — `parseFrontmatter` + `SpecfileAdapter.SubmitFromFile`; pure tests (frontmatter,
   no-frontmatter, field mapping).
3. **intake/github** — `Issue`, `IssueFetcher`, `GitHubAdapter.SubmitFromIssue`; tests with a fake fetcher.
4. **intake/emit** — `Emit(run, EmitDeps)` dispatch by intake_kind (+ not-done guard); tests with fakes for
   all three kinds.
5. **cmd/wrap** — `wrap submit`, `wrap github`, `wrap emit` wiring real `gh`/`git` subprocess impls.
6. **docs** — CLAUDE.md (adapters, emission, gh/frontmatter decisions) + this plan.
