# wrap Phase 5: Gate Engine + Plan/Merge Gates

> Compact plan. Base: `main` (Phase 4 merged). TDD per task, commit per task, push + PR at the end.

**Goal:** Replace the Phase 3/4 `AutoAdvanceGates` boolean scaffold with a real gate engine driven by each
run's `gates_json`. The plan gate (`plan_gate → working`) and merge gate (`merge_gate → done`) each evaluate
their policy: `auto` advances immediately (recording an `auto-approved` gate); `require_approval` creates a
pending `gates` row and **holds** the run until a human resolves it via the API/CLI. Approval advances;
rejection fails the run.

**Scope decisions:**
- Resolution surface: **API + CLI** (`POST /runs/{id}/approve|reject`, `wrap approve|reject <run-id>`).
- **Plan + merge gates only.** The always-on `worker_blocked`/`merge_conflict` gates (which pause mid-phase)
  are **deferred** — they conflict with the synchronous worker/merger model; `report_blocked → failed` stays.
- `worker_done` gate policy key is parsed but not enforced (it is `auto` by default; we never pause on a
  worker completing).

## Key design decisions

1. **`internal/gates` is a pure policy package.** `Parse(gatesJSON) → Policy`; `Policy.Mode(kind)` returns
   `auto`/`require_approval`, **defaulting missing keys to `require_approval`** (safe: never auto-approve the
   unspecified). No I/O.
2. **The API only resolves gates; the orchestrator owns all phase transitions.** `approve`/`reject` flip the
   pending `gates` row; the next `Tick` observes the resolved gate and advances the FSM. Single writer of
   `runs.phase` stays the orchestrator.
3. **`approve`/`reject` act on the run's current pending gate** (no gate-id needed in the CLI). 400/409 if none
   pending. `resolved_by` defaults to `cli`.
4. **Gate rows are created idempotently per (run, kind).** `drivePlanGate`/`driveMergeGate` look up the latest
   gate of that kind; create one only if absent. `auto` → `auto-approved` immediately; `require_approval` →
   `pending` + a `gate_requested` event.
5. **FSM:** add `plan_gate --gate_reject--> failed` and `merge_gate --gate_reject--> failed`. Approval still
   uses the existing `work_start` (plan) / `gate_approve` (merge) transitions, emitted by the orchestrator.
6. **`AutoAdvanceGates` and `--auto-advance-gates` are deleted.** Orchestrator always evaluates the gates; tests
   that drove straight through now use an `auto` `gates_json`.

## Tasks

1. **FSM** — `plan_gate→failed` and `merge_gate→failed` on `gate_reject`; table tests.
2. **internal/gates** — `Policy`, `Parse`, `Mode` (default `require_approval`); pure tests.
3. **store gates** — `Gate` struct, `InsertGate`, `LatestGateByKind`, `PendingGateByRun`, `ResolveGate`,
   `ListGatesByRun` + tests.
4. **orchestrator** — `drivePlanGate` + `driveMergeGate` (policy eval, create/observe gate, advance/hold/fail);
   remove `AutoAdvanceGates`; restructure `Tick`; emit `gate_requested`. Migrate orchestrator tests to `auto`
   gates_json; add require_approval + reject unit tests.
5. **API** — `POST /runs/{id}/approve`, `POST /runs/{id}/reject` (resolve pending gate); expose
   `pending_gate_kind`/`pending_gate_id` on `GET /runs/{id}`. Handler tests.
6. **client + CLI** — `Client.Approve/Reject`; `wrap approve|reject <run-id>`.
7. **wrapd** — drop `--auto-advance-gates`.
8. **integration** — approval-flow happy path (plan_gate→approve→merge_gate→approve→done); auto-gates
   straight-through; plan reject → failed.
9. **docs** — CLAUDE.md (delete AutoAdvanceGates notes, document the gate engine) + this plan.
