package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Lithial/ManageBot/internal/ids"
)

// Gate is one approval checkpoint for a run. Status is one of pending |
// approved | rejected | auto-approved. ResolvedBy/ResolvedAt are empty/0 until
// the gate is resolved.
type Gate struct {
	ID          string
	RunID       string
	Kind        string
	Status      string
	PayloadJSON string
	ResolvedBy  string
	ResolvedAt  int64
	Action      string
	CreatedAt   int64
}

// InsertGate persists a gate (status defaults to "pending") and returns its id.
func (s *Store) InsertGate(ctx context.Context, g Gate) (string, error) {
	id := g.ID
	if id == "" {
		id = ids.New()
	}
	if g.Status == "" {
		g.Status = "pending"
	}
	now := time.Now().Unix()
	var resolvedBy sql.NullString
	if g.ResolvedBy != "" {
		resolvedBy = sql.NullString{String: g.ResolvedBy, Valid: true}
	}
	var resolvedAt sql.NullInt64
	if g.ResolvedAt != 0 {
		resolvedAt = sql.NullInt64{Int64: g.ResolvedAt, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gates (id, run_id, kind, status, payload_json, resolved_by, resolved_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, g.RunID, g.Kind, g.Status, g.PayloadJSON, resolvedBy, resolvedAt, now)
	if err != nil {
		return "", fmt.Errorf("insert gate: %w", err)
	}
	return id, nil
}

const gateColumns = `id, run_id, kind, status, payload_json, COALESCE(resolved_by, ''), COALESCE(resolved_at, 0), COALESCE(action, ''), created_at`

func scanGate(scanner interface{ Scan(dest ...any) error }) (Gate, error) {
	var g Gate
	if err := scanner.Scan(&g.ID, &g.RunID, &g.Kind, &g.Status, &g.PayloadJSON, &g.ResolvedBy, &g.ResolvedAt, &g.Action, &g.CreatedAt); err != nil {
		return Gate{}, err
	}
	return g, nil
}

// PendingGateByRun returns the run's current pending gate (newest if more than
// one), or ErrNotFound when no gate is awaiting resolution.
func (s *Store) PendingGateByRun(ctx context.Context, runID string) (Gate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+gateColumns+` FROM gates WHERE run_id = ? AND status = 'pending' ORDER BY rowid DESC LIMIT 1`, runID)
	g, err := scanGate(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Gate{}, fmt.Errorf("pending gate for run %q: %w", runID, ErrNotFound)
		}
		return Gate{}, fmt.Errorf("pending gate for run %q: %w", runID, err)
	}
	return g, nil
}

// LatestGateByKind returns the most recently created gate of the given kind for
// a run (rowid breaks same-second ties), or ErrNotFound if none exists.
func (s *Store) LatestGateByKind(ctx context.Context, runID, kind string) (Gate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+gateColumns+` FROM gates WHERE run_id = ? AND kind = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, runID, kind)
	g, err := scanGate(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Gate{}, fmt.Errorf("latest %q gate for run %q: %w", kind, runID, ErrNotFound)
		}
		return Gate{}, fmt.Errorf("latest %q gate for run %q: %w", kind, runID, err)
	}
	return g, nil
}

// ErrGateNotPending is returned by ResolveGate when the gate exists but has
// already been resolved — the optimistic-lock signal for concurrent resolution.
var ErrGateNotPending = errors.New("gate not pending")

// ResolveGate atomically sets a still-pending gate's status, resolver, and
// resolved_at. The `status='pending'` guard makes concurrent resolution safe:
// the first writer wins, a later one gets ErrGateNotPending. Returns ErrNotFound
// if no gate with that id exists at all.
func (s *Store) ResolveGate(ctx context.Context, id, status, resolvedBy, action string) error {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE gates SET status = ?, resolved_by = ?, resolved_at = ?, action = ? WHERE id = ? AND status = 'pending'
	`, status, resolvedBy, now, action, id)
	if err != nil {
		return fmt.Errorf("resolve gate: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("resolve gate rows: %w", err)
	}
	if n == 0 {
		// Classify: missing id vs already-resolved.
		var cur string
		switch err := s.db.QueryRowContext(ctx, `SELECT status FROM gates WHERE id = ?`, id).Scan(&cur); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("resolve gate %q: %w", id, ErrNotFound)
		case err != nil:
			return fmt.Errorf("resolve gate %q classify: %w", id, err)
		default:
			return fmt.Errorf("resolve gate %q (status=%s): %w", id, cur, ErrGateNotPending)
		}
	}
	return nil
}

// ListGatesByRun returns a run's gates in creation order.
func (s *Store) ListGatesByRun(ctx context.Context, runID string) ([]Gate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+gateColumns+` FROM gates WHERE run_id = ? ORDER BY rowid ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list gates by run: %w", err)
	}
	defer rows.Close()
	var out []Gate
	for rows.Next() {
		g, err := scanGate(rows)
		if err != nil {
			return nil, fmt.Errorf("scan gate: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}
