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
