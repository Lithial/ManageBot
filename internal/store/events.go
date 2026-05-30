package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Event is one row in the append-only forensic/emission log. WorkerID is the
// empty string when the event is not scoped to a worker (stored as NULL).
type Event struct {
	ID          int64
	RunID       string
	WorkerID    string
	Kind        string
	PayloadJSON string
	TS          int64
}

// InsertEvent appends an event (ts defaults to now) and returns its
// autoincrement id.
func (s *Store) InsertEvent(ctx context.Context, e Event) (int64, error) {
	ts := e.TS
	if ts == 0 {
		ts = time.Now().Unix()
	}
	var workerID sql.NullString
	if e.WorkerID != "" {
		workerID = sql.NullString{String: e.WorkerID, Valid: true}
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO events (run_id, worker_id, kind, payload_json, ts)
		VALUES (?, ?, ?, ?, ?)
	`, e.RunID, workerID, e.Kind, e.PayloadJSON, ts)
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert event id: %w", err)
	}
	return id, nil
}

const eventColumns = `id, run_id, COALESCE(worker_id, ''), kind, payload_json, ts`

func scanEvent(scanner interface{ Scan(dest ...any) error }) (Event, error) {
	var e Event
	if err := scanner.Scan(&e.ID, &e.RunID, &e.WorkerID, &e.Kind, &e.PayloadJSON, &e.TS); err != nil {
		return Event{}, err
	}
	return e, nil
}

// ListEventsByRun returns a run's events in insertion order (id ascending).
func (s *Store) ListEventsByRun(ctx context.Context, runID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventColumns+` FROM events WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list events by run: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// LatestEventByKind returns the most recent event of the given kind for a run,
// or ErrNotFound if none exists.
func (s *Store) LatestEventByKind(ctx context.Context, runID, kind string) (Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+eventColumns+` FROM events WHERE run_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, runID, kind)
	e, err := scanEvent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Event{}, fmt.Errorf("latest %q event for run %q: %w", kind, runID, ErrNotFound)
		}
		return Event{}, fmt.Errorf("latest %q event for run %q: %w", kind, runID, err)
	}
	return e, nil
}
