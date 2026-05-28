# wrap Phase 1: Skeleton Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the foundational binaries (`wrap` CLI, `wrapd` daemon, `fake-claude` test shim) with SQLite-backed state and a Unix-socket HTTP API that can accept and persist a run request end-to-end — but does not yet execute the run.

**Architecture:** `wrapd` is a long-lived Go process that listens on a Unix socket and exposes a small HTTP+JSON API backed by SQLite (modernc.org/sqlite, pure-Go). The `wrap` CLI is a thin HTTP client over the same socket; its first subcommand (`wrap run <spec>`) creates a run row via `POST /runs`. The `fake-claude` shim is a tiny env-driven binary that will substitute for the real `claude -p` in later phases' integration tests; Phase 1 only needs it to exist and be invokable.

**Tech Stack:** Go 1.22+, `modernc.org/sqlite` (pure-Go SQLite — no CGO, easy cross-compile), `github.com/oklog/ulid/v2` (run/worker IDs), `net/http` over a `net.UnixListener`, Go stdlib `flag` for CLI parsing, Go stdlib `testing` for tests. No web framework, no CLI framework, no ORM.

**Spec reference:** `docs/superpowers/specs/2026-05-26-claude-swarm-wrapper-design.md`. This plan covers Phase 1 of the "Open questions for the implementation plan" list at the end of that spec. Phases 2–9 will get their own plans.

---

## File structure produced by this plan

```
/home/lithial/coding/wrap/
├── go.mod
├── go.sum
├── Makefile
├── .gitignore
├── cmd/
│   ├── wrap/main.go              # CLI binary entrypoint
│   ├── wrapd/main.go             # Daemon binary entrypoint
│   └── fake-claude/main.go       # Test shim binary entrypoint
├── internal/
│   ├── ids/
│   │   └── ids.go                # ULID helper
│   ├── store/
│   │   ├── schema.sql            # Canonical schema (from spec)
│   │   ├── schema.go             # go:embed schema.sql + ApplySchema()
│   │   ├── store.go              # Open(), Close()
│   │   ├── store_test.go
│   │   ├── runs.go               # Project + Run insert helpers
│   │   └── runs_test.go
│   ├── api/
│   │   ├── server.go             # http.Server over net.UnixListener
│   │   ├── server_test.go
│   │   ├── handlers.go           # /healthz, POST /runs
│   │   └── handlers_test.go
│   ├── client/
│   │   ├── client.go             # HTTP client over Unix socket
│   │   └── client_test.go
│   ├── intake/
│   │   ├── intake.go             # Project, Run types (shared API DTOs)
│   │   ├── cli.go                # CLI adapter: spec file → API call
│   │   └── cli_test.go
│   └── testutil/
│       ├── daemon.go             # StartTestDaemon helper
│       └── shim.go               # Path resolution for fake-claude binary
└── test/
    └── integration/
        └── skeleton_test.go      # End-to-end smoke for Phase 1
```

**File-responsibility rules being followed:**

- `internal/store/` owns all SQL and the `*sql.DB`. No other package imports `database/sql` directly.
- `internal/api/` owns the HTTP request/response handling. Handlers are thin — they delegate persistence to `store` and serialization to `intake` DTOs.
- `internal/client/` mirrors `internal/api/` from the caller's side. Its tests run against `httptest.NewServer` with a Unix-socket listener.
- `internal/intake/` defines the canonical DTOs that flow over the wire (the API uses these types directly so the contract is one place, not two).
- `cmd/*/main.go` are wiring only — no business logic.

---

## Task 1: Project bootstrap (go.mod, .gitignore, Makefile)

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `Makefile`

- [ ] **Step 1: Initialize the Go module**

Run:
```bash
cd /home/lithial/coding/wrap
go mod init github.com/Lithial/ManageBot
```

Expected: creates `go.mod` with module path `github.com/Lithial/ManageBot` and current Go version.

- [ ] **Step 2: Create `.gitignore`**

Create `/home/lithial/coding/wrap/.gitignore`:

```gitignore
# Built binaries
/bin/
/wrap
/wrapd
/fake-claude

# Local daemon state
/.wrap/
*.db
*.db-journal
*.db-wal
*.db-shm

# Unix sockets
*.sock

# Go test artifacts
/coverage.out
/coverage.html

# Editor leftovers
.DS_Store
*.swp
*~
```

- [ ] **Step 3: Create the `Makefile`**

Create `/home/lithial/coding/wrap/Makefile`:

```makefile
GO ?= go
BIN_DIR := bin

.PHONY: all build wrap wrapd fake-claude test test-unit test-integration clean

all: build

build: wrap wrapd fake-claude

wrap:
	$(GO) build -o $(BIN_DIR)/wrap ./cmd/wrap

wrapd:
	$(GO) build -o $(BIN_DIR)/wrapd ./cmd/wrapd

fake-claude:
	$(GO) build -o $(BIN_DIR)/fake-claude ./cmd/fake-claude

test: test-unit

test-unit:
	$(GO) test ./...

test-integration: fake-claude wrapd wrap
	$(GO) test -tags=integration ./test/integration/...

clean:
	rm -rf $(BIN_DIR)
```

- [ ] **Step 4: Verify the module compiles (nothing to build yet, but `go vet` should pass)**

Run:
```bash
cd /home/lithial/coding/wrap
go vet ./...
```

Expected: no output, exit 0 (module is valid; no packages yet).

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add go.mod .gitignore Makefile
git commit -m "chore: bootstrap Go module, .gitignore, Makefile"
```

---

## Task 2: SQLite store package — schema and open/close

**Files:**
- Create: `internal/store/schema.sql`
- Create: `internal/store/schema.go`
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`

**Why this task exists:** Every other component needs a working `*sql.DB` with the canonical schema applied. We test by opening a temp DB and asserting all six tables exist.

- [ ] **Step 1: Add the SQLite dependency**

Run:
```bash
cd /home/lithial/coding/wrap
go get modernc.org/sqlite@latest
```

Expected: dependency added to `go.mod`/`go.sum`.

- [ ] **Step 2: Write the failing test**

Create `/home/lithial/coding/wrap/internal/store/store_test.go`:

```go
package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func TestOpenAppliesSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrap.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	expected := []string{"projects", "runs", "plans", "workers", "events", "gates"}
	for _, table := range expected {
		var name string
		err := s.DB().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrap.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = s1.Close()

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/store/...
```

Expected: build failure — `package github.com/Lithial/ManageBot/internal/store is not in std`.

- [ ] **Step 4: Create the schema SQL file**

Create `/home/lithial/coding/wrap/internal/store/schema.sql` with the canonical schema from the spec:

```sql
CREATE TABLE IF NOT EXISTS projects (
  id                   TEXT PRIMARY KEY,
  name                 TEXT NOT NULL UNIQUE,
  repo_path            TEXT NOT NULL,
  default_gates_json   TEXT NOT NULL,
  verification_command TEXT,
  created_at           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL REFERENCES projects(id),
  intake_kind  TEXT NOT NULL,
  intake_ref   TEXT,
  spec_md      TEXT NOT NULL,
  gates_json   TEXT NOT NULL,
  phase        TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS plans (
  id           TEXT PRIMARY KEY,
  run_id       TEXT NOT NULL REFERENCES runs(id),
  plan_md      TEXT NOT NULL,
  tasks_json   TEXT NOT NULL,
  approved_at  INTEGER,
  created_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS workers (
  id            TEXT PRIMARY KEY,
  run_id        TEXT NOT NULL REFERENCES runs(id),
  task_id       TEXT NOT NULL,
  branch        TEXT NOT NULL,
  worktree_path TEXT NOT NULL,
  pid           INTEGER,
  status        TEXT NOT NULL,
  exit_code     INTEGER,
  started_at    INTEGER,
  ended_at      INTEGER
);

CREATE TABLE IF NOT EXISTS events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id       TEXT NOT NULL REFERENCES runs(id),
  worker_id    TEXT REFERENCES workers(id),
  kind         TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  ts           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS gates (
  id           TEXT PRIMARY KEY,
  run_id       TEXT NOT NULL REFERENCES runs(id),
  kind         TEXT NOT NULL,
  status       TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  resolved_by  TEXT,
  resolved_at  INTEGER,
  created_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_run_ts ON events(run_id, ts);
CREATE INDEX IF NOT EXISTS idx_workers_run ON workers(run_id);
CREATE INDEX IF NOT EXISTS idx_gates_run_status ON gates(run_id, status);
```

- [ ] **Step 5: Create the schema-embed helper**

Create `/home/lithial/coding/wrap/internal/store/schema.go`:

```go
package store

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schemaSQL string

func applySchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, schemaSQL)
	return err
}
```

- [ ] **Step 6: Create the Store wrapper**

Create `/home/lithial/coding/wrap/internal/store/store.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := applySchema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("applySchema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB { return s.db }
func (s *Store) Close() error { return s.db.Close() }
```

- [ ] **Step 7: Run the test to verify it passes**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/store/...
```

Expected: `ok  github.com/Lithial/ManageBot/internal/store`.

- [ ] **Step 8: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/store go.mod go.sum
git commit -m "feat(store): SQLite store with canonical schema and tests"
```

---

## Task 3: Store — Project and Run insert helpers

**Files:**
- Create: `internal/store/runs.go`
- Create: `internal/store/runs_test.go`
- Create: `internal/ids/ids.go`

**Why this task exists:** The API handler in Task 5 needs to create projects and runs without writing raw SQL. We isolate persistence here so handlers stay thin.

- [ ] **Step 1: Add the ULID dependency**

Run:
```bash
cd /home/lithial/coding/wrap
go get github.com/oklog/ulid/v2@latest
```

- [ ] **Step 2: Create the ID helper**

Create `/home/lithial/coding/wrap/internal/ids/ids.go`:

```go
// Package ids generates ULIDs for use as primary keys across the wrap data model.
package ids

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

func New() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}
```

- [ ] **Step 3: Write the failing test**

Create `/home/lithial/coding/wrap/internal/store/runs_test.go`:

```go
package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func TestInsertProjectAndRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrap.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	p := store.Project{
		Name:             "demo",
		RepoPath:         "/tmp/demo-repo",
		DefaultGatesJSON: `{"plan":{"mode":"auto"}}`,
	}
	pid, err := s.InsertProject(ctx, p)
	if err != nil {
		t.Fatalf("InsertProject: %v", err)
	}
	if pid == "" {
		t.Fatal("InsertProject returned empty id")
	}

	r := store.Run{
		ProjectID:  pid,
		IntakeKind: "cli",
		IntakeRef:  "/tmp/demo-spec.md",
		SpecMD:     "# demo spec",
		GatesJSON:  `{"plan":{"mode":"auto"}}`,
		Phase:      "pending",
	}
	rid, err := s.InsertRun(ctx, r)
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if rid == "" {
		t.Fatal("InsertRun returned empty id")
	}

	// Verify the run row exists with the expected fields.
	got, err := s.GetRun(ctx, rid)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ProjectID != pid {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, pid)
	}
	if got.Phase != "pending" {
		t.Errorf("Phase = %q, want %q", got.Phase, "pending")
	}
}

func TestInsertProjectDuplicateNameFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrap.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	p := store.Project{Name: "demo", RepoPath: "/tmp/r", DefaultGatesJSON: "{}"}
	if _, err := s.InsertProject(ctx, p); err != nil {
		t.Fatalf("first InsertProject: %v", err)
	}
	if _, err := s.InsertProject(ctx, p); err == nil {
		t.Fatal("expected duplicate-name error, got nil")
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/store/...
```

Expected: build failure — `undefined: store.Project`, `undefined: store.InsertProject`, etc.

- [ ] **Step 5: Implement the helpers**

Create `/home/lithial/coding/wrap/internal/store/runs.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Lithial/ManageBot/internal/ids"
)

type Project struct {
	ID                  string
	Name                string
	RepoPath            string
	DefaultGatesJSON    string
	VerificationCommand sql.NullString
	CreatedAt           int64
}

type Run struct {
	ID         string
	ProjectID  string
	IntakeKind string
	IntakeRef  string
	SpecMD     string
	GatesJSON  string
	Phase      string
	CreatedAt  int64
	UpdatedAt  int64
}

func (s *Store) InsertProject(ctx context.Context, p Project) (string, error) {
	id := p.ID
	if id == "" {
		id = ids.New()
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, name, repo_path, default_gates_json, verification_command, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, p.Name, p.RepoPath, p.DefaultGatesJSON, p.VerificationCommand, now)
	if err != nil {
		return "", fmt.Errorf("insert project: %w", err)
	}
	return id, nil
}

func (s *Store) InsertRun(ctx context.Context, r Run) (string, error) {
	id := r.ID
	if id == "" {
		id = ids.New()
	}
	now := time.Now().Unix()
	if r.Phase == "" {
		r.Phase = "pending"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (id, project_id, intake_kind, intake_ref, spec_md, gates_json, phase, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, r.ProjectID, r.IntakeKind, r.IntakeRef, r.SpecMD, r.GatesJSON, r.Phase, now, now)
	if err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}
	return id, nil
}

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, intake_kind, COALESCE(intake_ref, ''), spec_md, gates_json, phase, created_at, updated_at
		FROM runs WHERE id = ?
	`, id).Scan(&r.ID, &r.ProjectID, &r.IntakeKind, &r.IntakeRef, &r.SpecMD, &r.GatesJSON, &r.Phase, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("get run %q: %w", id, err)
	}
	return r, nil
}

func (s *Store) ProjectByName(ctx context.Context, name string) (Project, error) {
	var p Project
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, repo_path, default_gates_json, verification_command, created_at
		FROM projects WHERE name = ?
	`, name).Scan(&p.ID, &p.Name, &p.RepoPath, &p.DefaultGatesJSON, &p.VerificationCommand, &p.CreatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("project by name %q: %w", name, err)
	}
	return p, nil
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/store/...
```

Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/store internal/ids go.mod go.sum
git commit -m "feat(store): Project and Run insert helpers with ULID IDs"
```

---

## Task 4: Intake DTOs (shared types)

**Files:**
- Create: `internal/intake/intake.go`

**Why this task exists:** The API server (Task 5) and the client (Task 6) both need to agree on the wire shape. Defining the DTOs once and importing from both avoids duplication.

- [ ] **Step 1: Create the DTOs**

Create `/home/lithial/coding/wrap/internal/intake/intake.go`:

```go
// Package intake defines the canonical request/response types that flow
// between intake adapters, the daemon API, and the daemon's storage layer.
package intake

// SubmitRunRequest is the body of POST /runs.
type SubmitRunRequest struct {
	ProjectName         string `json:"project_name"`
	RepoPath            string `json:"repo_path"`
	IntakeKind          string `json:"intake_kind"` // "cli" | "specfile" | "github"
	IntakeRef           string `json:"intake_ref,omitempty"`
	SpecMD              string `json:"spec_md"`
	GatesJSON           string `json:"gates_json,omitempty"`            // optional; daemon falls back to project default
	VerificationCommand string `json:"verification_command,omitempty"`  // optional; only used when project is being created
}

// SubmitRunResponse is the body of a successful POST /runs.
type SubmitRunResponse struct {
	RunID     string `json:"run_id"`
	ProjectID string `json:"project_id"`
	Phase     string `json:"phase"`
}

// ErrorResponse is the body of any non-2xx response.
type ErrorResponse struct {
	Error string `json:"error"`
}
```

- [ ] **Step 2: Verify compilation**

Run:
```bash
cd /home/lithial/coding/wrap
go build ./internal/intake/...
```

Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/intake
git commit -m "feat(intake): shared DTOs for POST /runs"
```

---

## Task 5: API server — HTTP over Unix socket, with /healthz and POST /runs

**Files:**
- Create: `internal/api/server.go`
- Create: `internal/api/server_test.go`
- Create: `internal/api/handlers.go`
- Create: `internal/api/handlers_test.go`

**Why this task exists:** The daemon's external surface. Tests use real Unix sockets in `t.TempDir()` so we exercise the actual transport.

- [ ] **Step 1: Write the failing tests**

Create `/home/lithial/coding/wrap/internal/api/server_test.go`:

```go
package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/api"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

func startServer(t *testing.T) (*http.Client, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "wrap.sock")
	dbPath := filepath.Join(t.TempDir(), "wrap.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := api.NewServer(s, sock)
	go func() {
		_ = srv.Serve()
	}()
	t.Cleanup(func() { _ = srv.Close() })

	// Wait for the socket to appear (up to 1s).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
	return client, sock
}

func TestHealthz(t *testing.T) {
	client, _ := startServer(t)

	resp, err := client.Get("http://wrap/healthz")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestSubmitRunCreatesProjectAndRun(t *testing.T) {
	client, _ := startServer(t)

	body := intake.SubmitRunRequest{
		ProjectName: "demo",
		RepoPath:    "/tmp/demo-repo",
		IntakeKind:  "cli",
		IntakeRef:   "/tmp/spec.md",
		SpecMD:      "# demo",
	}
	buf, _ := json.Marshal(body)

	resp, err := client.Post("http://wrap/runs", "application/json", readerOf(buf))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}

	var out intake.SubmitRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RunID == "" {
		t.Error("RunID empty")
	}
	if out.ProjectID == "" {
		t.Error("ProjectID empty")
	}
	if out.Phase != "pending" {
		t.Errorf("Phase = %q, want %q", out.Phase, "pending")
	}
}

func TestSubmitRunReusesExistingProject(t *testing.T) {
	client, _ := startServer(t)

	body := intake.SubmitRunRequest{
		ProjectName: "demo",
		RepoPath:    "/tmp/demo-repo",
		IntakeKind:  "cli",
		SpecMD:      "# first",
	}
	buf, _ := json.Marshal(body)
	resp1, err := client.Post("http://wrap/runs", "application/json", readerOf(buf))
	if err != nil {
		t.Fatalf("first Post: %v", err)
	}
	defer resp1.Body.Close()
	var out1 intake.SubmitRunResponse
	_ = json.NewDecoder(resp1.Body).Decode(&out1)

	body.SpecMD = "# second"
	buf, _ = json.Marshal(body)
	resp2, err := client.Post("http://wrap/runs", "application/json", readerOf(buf))
	if err != nil {
		t.Fatalf("second Post: %v", err)
	}
	defer resp2.Body.Close()
	var out2 intake.SubmitRunResponse
	_ = json.NewDecoder(resp2.Body).Decode(&out2)

	if out1.ProjectID != out2.ProjectID {
		t.Errorf("project IDs differ: %q vs %q", out1.ProjectID, out2.ProjectID)
	}
	if out1.RunID == out2.RunID {
		t.Errorf("run IDs equal but should differ")
	}
}
```

Add a tiny helper for readability in `internal/api/server_test.go` (or inline it):

```go
import "bytes"

func readerOf(b []byte) *bytes.Reader { return bytes.NewReader(b) }
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/api/...
```

Expected: build failure — `undefined: api.NewServer`, etc.

- [ ] **Step 3: Implement the server**

Create `/home/lithial/coding/wrap/internal/api/server.go`:

```go
// Package api hosts wrapd's HTTP API over a Unix-socket listener.
package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Lithial/ManageBot/internal/store"
)

type Server struct {
	store      *store.Store
	socketPath string
	httpSrv    *http.Server
	listener   net.Listener
}

func NewServer(s *store.Store, socketPath string) *Server {
	srv := &Server{store: s, socketPath: socketPath}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	srv.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv
}

func (s *Server) Serve() error {
	// Best-effort: remove a stale socket if one exists.
	_ = os.Remove(s.socketPath)

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	// Restrict socket to the current user.
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = l.Close()
		return err
	}
	s.listener = l
	if err := s.httpSrv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.httpSrv.Shutdown(ctx)
	_ = os.Remove(s.socketPath)
	return err
}
```

- [ ] **Step 4: Implement the handlers**

Create `/home/lithial/coding/wrap/internal/api/handlers.go`:

```go
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /runs", s.handleSubmitRun)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleSubmitRun(w http.ResponseWriter, r *http.Request) {
	var req intake.SubmitRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ProjectName == "" || req.RepoPath == "" || req.IntakeKind == "" || req.SpecMD == "" {
		writeError(w, http.StatusBadRequest, "project_name, repo_path, intake_kind, spec_md are required")
		return
	}

	ctx := r.Context()
	pid, err := s.findOrCreateProject(ctx, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	gates := req.GatesJSON
	if gates == "" {
		// Pull the project's default gates.
		p, err := s.store.ProjectByName(ctx, req.ProjectName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		gates = p.DefaultGatesJSON
	}

	rid, err := s.store.InsertRun(ctx, store.Run{
		ProjectID:  pid,
		IntakeKind: req.IntakeKind,
		IntakeRef:  req.IntakeRef,
		SpecMD:     req.SpecMD,
		GatesJSON:  gates,
		Phase:      "pending",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, intake.SubmitRunResponse{
		RunID:     rid,
		ProjectID: pid,
		Phase:     "pending",
	})
}

func (s *Server) findOrCreateProject(ctx context.Context, req intake.SubmitRunRequest) (string, error) {
	p, err := s.store.ProjectByName(ctx, req.ProjectName)
	if err == nil {
		return p.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		// Real DB error, not "row not found" — propagate.
		return "", err
	}
	defaultGates := `{"plan":{"mode":"require_approval"},"worker_done":{"mode":"auto"},"merge":{"mode":"require_approval"},"custom":[]}`
	verCmd := sql.NullString{}
	if req.VerificationCommand != "" {
		verCmd = sql.NullString{String: req.VerificationCommand, Valid: true}
	}
	return s.store.InsertProject(ctx, store.Project{
		Name:                req.ProjectName,
		RepoPath:            req.RepoPath,
		DefaultGatesJSON:    defaultGates,
		VerificationCommand: verCmd,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, intake.ErrorResponse{Error: msg})
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/api/...
```

Expected: all three tests pass.

- [ ] **Step 6: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/api
git commit -m "feat(api): HTTP server over Unix socket with /healthz and POST /runs"
```

---

## Task 6: Client package — Unix-socket HTTP client

**Files:**
- Create: `internal/client/client.go`
- Create: `internal/client/client_test.go`

**Why this task exists:** The `wrap` CLI and (later) the TUI both need a clean Go API for talking to `wrapd`. Wrapping the HTTP plumbing once keeps callers free of `net/http` noise.

- [ ] **Step 1: Write the failing test**

Create `/home/lithial/coding/wrap/internal/client/client_test.go`:

```go
package client_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/api"
	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

func startTestDaemon(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "wrap.sock")
	dbPath := filepath.Join(t.TempDir(), "wrap.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := api.NewServer(s, sock)
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", sock); err == nil {
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon socket never came up")
	return ""
}

func TestClientSubmitRun(t *testing.T) {
	sock := startTestDaemon(t)
	c := client.New(sock)

	resp, err := c.SubmitRun(context.Background(), intake.SubmitRunRequest{
		ProjectName: "demo",
		RepoPath:    "/tmp/demo",
		IntakeKind:  "cli",
		SpecMD:      "# spec",
	})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}
	if resp.RunID == "" {
		t.Error("RunID empty")
	}
	if resp.Phase != "pending" {
		t.Errorf("Phase = %q, want %q", resp.Phase, "pending")
	}
}

func TestClientHealthz(t *testing.T) {
	sock := startTestDaemon(t)
	c := client.New(sock)
	if err := c.Healthz(context.Background()); err != nil {
		t.Errorf("Healthz: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/client/...
```

Expected: build failure — `undefined: client.New`, etc.

- [ ] **Step 3: Implement the client**

Create `/home/lithial/coding/wrap/internal/client/client.go`:

```go
// Package client is the Go-level client for the wrapd HTTP API over Unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/Lithial/ManageBot/internal/intake"
)

type Client struct {
	http       *http.Client
	socketPath string
}

func New(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

func (c *Client) Healthz(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://wrap/healthz", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("healthz: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) SubmitRun(ctx context.Context, req intake.SubmitRunRequest) (intake.SubmitRunResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return intake.SubmitRunResponse{}, err
	}
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://wrap/runs", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return intake.SubmitRunResponse{}, fmt.Errorf("submit run: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return intake.SubmitRunResponse{}, fmt.Errorf("submit run: status %d: %s", resp.StatusCode, raw)
	}
	var out intake.SubmitRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return intake.SubmitRunResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/client/...
```

Expected: both tests pass.

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/client
git commit -m "feat(client): Unix-socket HTTP client with SubmitRun and Healthz"
```

---

## Task 7: Intake — CLI adapter

**Files:**
- Create: `internal/intake/cli.go`
- Create: `internal/intake/cli_test.go`

**Why this task exists:** Encapsulates the "read a spec file, derive project metadata, submit via client" logic so the CLI `main.go` is pure wiring.

- [ ] **Step 1: Write the failing test**

Create `/home/lithial/coding/wrap/internal/intake/cli_test.go`:

```go
package intake_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/api"
	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
	"github.com/Lithial/ManageBot/internal/store"
)

func startTestDaemon(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "wrap.sock")
	dbPath := filepath.Join(t.TempDir(), "wrap.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := api.NewServer(s, sock)
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", sock); err == nil {
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket never came up")
	return ""
}

func TestCLIAdapterSubmitsSpecFile(t *testing.T) {
	sock := startTestDaemon(t)
	c := client.New(sock)

	// Create a repo dir with a spec file inside.
	repo := t.TempDir()
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# my spec\n\nDo a thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := intake.NewCLIAdapter(c)
	resp, err := adapter.SubmitFromSpec(context.Background(), specPath, repo)
	if err != nil {
		t.Fatalf("SubmitFromSpec: %v", err)
	}
	if resp.RunID == "" {
		t.Error("RunID empty")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/intake/...
```

Expected: build failure — `undefined: intake.NewCLIAdapter`.

- [ ] **Step 3: Implement the CLI adapter**

Create `/home/lithial/coding/wrap/internal/intake/cli.go`:

```go
package intake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lithial/ManageBot/internal/client"
)

// CLIAdapter implements the `wrap run <spec>` intake path.
type CLIAdapter struct {
	client *client.Client
}

func NewCLIAdapter(c *client.Client) *CLIAdapter {
	return &CLIAdapter{client: c}
}

// SubmitFromSpec reads the spec markdown at specPath and submits a new run
// against the project rooted at repoPath. Returns the daemon's response.
func (a *CLIAdapter) SubmitFromSpec(ctx context.Context, specPath, repoPath string) (SubmitRunResponse, error) {
	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("abs spec path: %w", err)
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("abs repo path: %w", err)
	}
	specBytes, err := os.ReadFile(absSpec)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("read spec: %w", err)
	}
	projectName := filepath.Base(absRepo)
	return a.client.SubmitRun(ctx, SubmitRunRequest{
		ProjectName: projectName,
		RepoPath:    absRepo,
		IntakeKind:  "cli",
		IntakeRef:   absSpec,
		SpecMD:      string(specBytes),
	})
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run:
```bash
cd /home/lithial/coding/wrap
go test ./internal/intake/...
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/intake
git commit -m "feat(intake): CLI adapter that reads a spec file and submits a run"
```

---

## Task 8: `wrap` CLI binary entrypoint

**Files:**
- Create: `cmd/wrap/main.go`

**Why this task exists:** The user-facing CLI. Pure wiring of flags → client → adapter → printed result. No tests at this layer; we cover it end-to-end in Task 11.

- [ ] **Step 1: Create `cmd/wrap/main.go`**

Create `/home/lithial/coding/wrap/cmd/wrap/main.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lithial/ManageBot/internal/client"
	"github.com/Lithial/ManageBot/internal/intake"
)

func defaultSocketPath() string {
	if v := os.Getenv("WRAP_SOCKET"); v != "" {
		return v
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "wrap.sock")
	}
	return filepath.Join(os.TempDir(), "wrap.sock")
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wrap: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: wrap <command> [args...]\ncommands: run")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(rest)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	socket := fs.String("socket", defaultSocketPath(), "wrapd Unix socket path")
	repo := fs.String("repo", "", "repo path (defaults to current working directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: wrap run [--socket PATH] [--repo PATH] <spec.md>")
	}
	specPath := fs.Arg(0)

	repoPath := *repo
	if repoPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		repoPath = cwd
	}

	c := client.New(*socket)
	adapter := intake.NewCLIAdapter(c)
	resp, err := adapter.SubmitFromSpec(context.Background(), specPath, repoPath)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	return nil
}
```

- [ ] **Step 2: Build to verify it compiles**

Run:
```bash
cd /home/lithial/coding/wrap
make wrap
```

Expected: `bin/wrap` exists.

- [ ] **Step 3: Smoke-test the help/usage output**

Run:
```bash
cd /home/lithial/coding/wrap
./bin/wrap 2>&1 || true
```

Expected: `wrap: usage: wrap <command> [args...]` printed to stderr; non-zero exit.

- [ ] **Step 4: Commit**

```bash
cd /home/lithial/coding/wrap
git add cmd/wrap
git commit -m "feat(cmd/wrap): CLI binary with 'run' subcommand"
```

---

## Task 9: `wrapd` daemon binary entrypoint

**Files:**
- Create: `cmd/wrapd/main.go`

**Why this task exists:** Wires the store and API server, picks a socket path, handles graceful shutdown.

- [ ] **Step 1: Create `cmd/wrapd/main.go`**

Create `/home/lithial/coding/wrap/cmd/wrapd/main.go`:

```go
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Lithial/ManageBot/internal/api"
	"github.com/Lithial/ManageBot/internal/store"
)

func defaultStateDir() string {
	if v := os.Getenv("WRAP_STATE_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "wrap")
	}
	return filepath.Join(home, ".wrap")
}

func defaultSocketPath() string {
	if v := os.Getenv("WRAP_SOCKET"); v != "" {
		return v
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "wrap.sock")
	}
	return filepath.Join(os.TempDir(), "wrap.sock")
}

func main() {
	stateDir := flag.String("state-dir", defaultStateDir(), "directory for wrapd state (DB, worktrees)")
	socket := flag.String("socket", defaultSocketPath(), "Unix socket path to listen on")
	flag.Parse()

	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		log.Fatalf("mkdir state dir: %v", err)
	}
	dbPath := filepath.Join(*stateDir, "wrap.db")

	s, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	srv := api.NewServer(s, *socket)
	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("wrapd: listening on %s, state in %s\n", *socket, *stateDir)
		errCh <- srv.Serve()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		fmt.Printf("wrapd: caught %s, shutting down\n", s)
	case err := <-errCh:
		if err != nil {
			log.Fatalf("serve: %v", err)
		}
	}
	if err := srv.Close(); err != nil {
		log.Printf("wrapd: shutdown error: %v", err)
	}
}
```

- [ ] **Step 2: Build to verify it compiles**

Run:
```bash
cd /home/lithial/coding/wrap
make wrapd
```

Expected: `bin/wrapd` exists.

- [ ] **Step 3: Smoke-test that it starts and binds the socket**

Run:
```bash
cd /home/lithial/coding/wrap
WRAP_STATE_DIR=$(mktemp -d) WRAP_SOCKET=$(mktemp -u --suffix=.sock) ./bin/wrapd &
WRAPD_PID=$!
sleep 0.3
echo "wrapd PID: $WRAPD_PID"
ls -la /tmp/*.sock 2>/dev/null || true
kill $WRAPD_PID
wait $WRAPD_PID 2>/dev/null || true
```

Expected: the daemon starts, prints its listening message, then exits cleanly on `kill`.

- [ ] **Step 4: Commit**

```bash
cd /home/lithial/coding/wrap
git add cmd/wrapd
git commit -m "feat(cmd/wrapd): daemon binary with SQLite store and Unix-socket API"
```

---

## Task 10: `fake-claude` test shim binary

**Files:**
- Create: `cmd/fake-claude/main.go`

**Why this task exists:** The integration test harness for later phases needs a deterministic stand-in for `claude -p`. Phase 1 only requires the binary to exist and obey a minimal env-driven contract; later phases will extend the shim's MCP-call scripting.

- [ ] **Step 1: Create the shim**

Create `/home/lithial/coding/wrap/cmd/fake-claude/main.go`:

```go
// fake-claude is an env-driven stand-in for `claude -p` used in wrap's
// integration tests. Phase 1 supports the bare minimum required for tests
// that spawn the binary as a subprocess; later phases will extend it to
// emit scripted MCP tool calls.
//
// Environment variables:
//   FAKE_CLAUDE_EXIT_CODE   integer exit code to use (default 0)
//   FAKE_CLAUDE_SLEEP_MS    milliseconds to sleep before exiting (default 0)
//   FAKE_CLAUDE_STDOUT      string to print to stdout before exiting
//   FAKE_CLAUDE_STDERR      string to print to stderr before exiting
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	if s := os.Getenv("FAKE_CLAUDE_SLEEP_MS"); s != "" {
		if ms, err := strconv.Atoi(s); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	if s := os.Getenv("FAKE_CLAUDE_STDOUT"); s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := os.Getenv("FAKE_CLAUDE_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	code := 0
	if s := os.Getenv("FAKE_CLAUDE_EXIT_CODE"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			code = n
		}
	}
	os.Exit(code)
}
```

- [ ] **Step 2: Build to verify it compiles**

Run:
```bash
cd /home/lithial/coding/wrap
make fake-claude
```

Expected: `bin/fake-claude` exists.

- [ ] **Step 3: Smoke-test the env contract**

Run:
```bash
cd /home/lithial/coding/wrap
FAKE_CLAUDE_STDOUT="hello" FAKE_CLAUDE_EXIT_CODE=3 ./bin/fake-claude; echo "exit=$?"
```

Expected output: `helloexit=3`.

- [ ] **Step 4: Commit**

```bash
cd /home/lithial/coding/wrap
git add cmd/fake-claude
git commit -m "feat(cmd/fake-claude): env-driven shim for integration tests"
```

---

## Task 11: End-to-end integration test (`test/integration/skeleton_test.go`)

**Files:**
- Create: `internal/testutil/daemon.go`
- Create: `internal/testutil/shim.go`
- Create: `test/integration/skeleton_test.go`

**Why this task exists:** Closes the loop on Phase 1 — proves that starting the real `wrapd` binary, calling the real `wrap` binary against it, persists a run row in the real DB. This is the first deliverable from the spec's "Test infrastructure deliverables" list.

- [ ] **Step 1: Create the daemon test helper**

Create `/home/lithial/coding/wrap/internal/testutil/daemon.go`:

```go
// Package testutil provides helpers for integration tests that need to
// spawn the real wrapd binary against an ephemeral state directory and
// Unix socket.
package testutil

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type Daemon struct {
	SocketPath string
	StateDir   string
	cmd        *exec.Cmd
}

// StartTestDaemon spawns the wrapd binary in a temp state dir, waits for the
// socket to become available, and registers a cleanup that kills the process.
// `wrapdBinary` should be the absolute path to a built wrapd binary; tests
// typically pass the result of LocateBinary("wrapd").
func StartTestDaemon(t *testing.T, wrapdBinary string) *Daemon {
	t.Helper()

	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "wrap.sock")

	cmd := exec.Command(wrapdBinary, "--state-dir", stateDir, "--socket", sock)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapd: %v", err)
	}

	d := &Daemon{SocketPath: sock, StateDir: stateDir, cmd: cmd}
	t.Cleanup(func() { d.Stop() })

	if err := d.waitForSocket(2 * time.Second); err != nil {
		d.Stop()
		t.Fatalf("wait for socket: %v", err)
	}
	return d
}

func (d *Daemon) waitForSocket(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("unix", d.SocketPath, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return context.DeadlineExceeded
}

func (d *Daemon) Stop() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _, _ = d.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = d.cmd.Process.Kill()
		<-done
	}
}
```

- [ ] **Step 2: Create the binary-locator helper**

Create `/home/lithial/coding/wrap/internal/testutil/shim.go`:

```go
package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// LocateBinary returns the absolute path to a binary in the repo's bin/
// directory. It walks up from the test file's location to find the repo
// root (identified by the presence of go.mod), then returns repoRoot/bin/<name>.
// Returns an error if the binary doesn't exist; the integration-test Makefile
// target is responsible for building all binaries before tests run.
func LocateBinary(name string) (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot determine caller for LocateBinary")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			path := filepath.Join(dir, "bin", name)
			if _, err := os.Stat(path); err != nil {
				return "", err
			}
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}
```

- [ ] **Step 3: Write the integration test**

Create `/home/lithial/coding/wrap/test/integration/skeleton_test.go`:

```go
//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestSkeletonEndToEnd(t *testing.T) {
	wrapdBin, err := testutil.LocateBinary("wrapd")
	if err != nil {
		t.Fatalf("locate wrapd: %v (did you run `make wrapd`?)", err)
	}
	wrapBin, err := testutil.LocateBinary("wrap")
	if err != nil {
		t.Fatalf("locate wrap: %v (did you run `make wrap`?)", err)
	}

	d := testutil.StartTestDaemon(t, wrapdBin)

	// Create a fake "repo" directory with a spec file.
	repo := t.TempDir()
	specPath := filepath.Join(repo, "spec.md")
	if err := os.WriteFile(specPath, []byte("# integration spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Invoke the wrap CLI against the test daemon.
	cmd := exec.Command(wrapBin, "run", "--socket", d.SocketPath, "--repo", repo, specPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrap run: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), `"run_id":`) {
		t.Fatalf("output missing run_id: %s", out)
	}
	if !strings.Contains(string(out), `"phase": "pending"`) {
		t.Fatalf("output missing pending phase: %s", out)
	}

	// Open the daemon's DB directly and verify the row exists.
	dbPath := filepath.Join(d.StateDir, "wrap.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs WHERE phase = 'pending'`,
	).Scan(&count); err != nil {
		t.Fatalf("query runs: %v", err)
	}
	if count != 1 {
		t.Errorf("runs count = %d, want 1", count)
	}

	var projectCount int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM projects WHERE name = ?`, filepath.Base(repo),
	).Scan(&projectCount); err != nil {
		t.Fatalf("query projects: %v", err)
	}
	if projectCount != 1 {
		t.Errorf("projects count = %d, want 1", projectCount)
	}
}
```

- [ ] **Step 4: Run the integration test via Makefile**

Run:
```bash
cd /home/lithial/coding/wrap
make test-integration
```

Expected: builds all three binaries, runs the integration test, prints `ok  github.com/Lithial/ManageBot/test/integration`.

- [ ] **Step 5: Run the full unit test suite once more to confirm nothing regressed**

Run:
```bash
cd /home/lithial/coding/wrap
make test-unit
```

Expected: all packages pass.

- [ ] **Step 6: Commit**

```bash
cd /home/lithial/coding/wrap
git add internal/testutil test/integration
git commit -m "test: end-to-end integration test for Phase 1 skeleton"
```

---

## Definition of done for Phase 1

After all 11 tasks complete, you should be able to:

1. Run `make build` and get three binaries in `bin/`.
2. Run `bin/wrapd --state-dir /tmp/wrap-state --socket /tmp/wrap.sock` in one terminal.
3. In another terminal, run `bin/wrap run --socket /tmp/wrap.sock --repo $PWD some-spec.md` and see a JSON response with `run_id`, `project_id`, `phase: "pending"`.
4. Run `make test-integration` and see it pass.

What you should explicitly **not** be able to do yet:

- Have anything actually happen in response to a submitted run (no FSM transitions, no planner spawn, no worker spawn). That's Phase 2's plan.

## What this plan deliberately does not include

- **MCP server in wrapd.** Added in Phase 2 alongside the planner.
- **FSM transition code.** Phase 2.
- **Spawning any `claude -p` (real or fake) from wrapd.** Phase 2/3.
- **Worktree management.** Phase 3.
- **Gate engine, TUI, GitHub adapter, merger, emission, daemon restart reconciler.** Phases 4–8.
- **Real `claude` E2E test.** Phase 9.
- **`internal/testutil/repo.go` (the `MakeTestRepo` helper).** The spec lists this as a test infrastructure deliverable, but no Phase 1 test creates a git repo — the integration test uses a plain temp directory. Deferred to Phase 3, where worktree management makes a real git repo necessary.

If any of those feel necessary to make Phase 1 useful, raise it — but the recommendation is to ship this slim and learn before committing to Phase 2's shape.
