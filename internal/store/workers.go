package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Lithial/ManageBot/internal/ids"
)

// Worker is one task's worker subprocess row. PID, StartedAt, and EndedAt use
// 0 as the "unset" sentinel (a real PID/timestamp is never 0). ExitCode is a
// pointer because exit code 0 is a meaningful value distinct from "not yet
// finished" (NULL) — emptiness cannot stand in for it.
type Worker struct {
	ID           string
	RunID        string
	TaskID       string
	Branch       string
	WorktreePath string
	PID          int64
	Status       string
	ExitCode     *int64
	StartedAt    int64
	EndedAt      int64
}

// InsertWorker persists a new worker row in status "running" with started_at
// set to now, and returns its id.
func (s *Store) InsertWorker(ctx context.Context, w Worker) (string, error) {
	id := w.ID
	if id == "" {
		id = ids.New()
	}
	now := time.Now().Unix()
	var pid sql.NullInt64
	if w.PID != 0 {
		pid = sql.NullInt64{Int64: w.PID, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workers (id, run_id, task_id, branch, worktree_path, pid, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?, 'running', ?)
	`, id, w.RunID, w.TaskID, w.Branch, w.WorktreePath, pid, now)
	if err != nil {
		return "", fmt.Errorf("insert worker: %w", err)
	}
	return id, nil
}

// FinishWorker records a worker's terminal status, its exit code, and ended_at.
// Returns ErrNotFound if no row matches.
func (s *Store) FinishWorker(ctx context.Context, id, status string, exitCode int) error {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE workers SET status = ?, exit_code = ?, ended_at = ? WHERE id = ?
	`, status, exitCode, now, id)
	if err != nil {
		return fmt.Errorf("finish worker: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish worker rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("finish worker %q: %w", id, ErrNotFound)
	}
	return nil
}

// ListWorkersByRun returns all worker rows for a run, ordered by started_at
// ascending (insertion order).
func (s *Store) ListWorkersByRun(ctx context.Context, runID string) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, task_id, branch, worktree_path, pid, status, exit_code, started_at, ended_at
		FROM workers WHERE run_id = ? ORDER BY started_at ASC, id ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("list workers by run: %w", err)
	}
	defer rows.Close()
	var out []Worker
	for rows.Next() {
		var w Worker
		var pid, exitCode, startedAt, endedAt sql.NullInt64
		if err := rows.Scan(&w.ID, &w.RunID, &w.TaskID, &w.Branch, &w.WorktreePath, &pid, &w.Status, &exitCode, &startedAt, &endedAt); err != nil {
			return nil, fmt.Errorf("scan worker: %w", err)
		}
		w.PID = pid.Int64
		w.StartedAt = startedAt.Int64
		w.EndedAt = endedAt.Int64
		if exitCode.Valid {
			v := exitCode.Int64
			w.ExitCode = &v
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}
