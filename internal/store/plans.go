package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Lithial/ManageBot/internal/ids"
)

type Plan struct {
	ID         string
	RunID      string
	PlanMD     string
	TasksJSON  string
	ApprovedAt int64 // 0 = not approved
	CreatedAt  int64
}

// InsertPlan persists a plan and returns its id.
func (s *Store) InsertPlan(ctx context.Context, p Plan) (string, error) {
	id := p.ID
	if id == "" {
		id = ids.New()
	}
	now := time.Now().Unix()
	var approved sql.NullInt64
	if p.ApprovedAt != 0 {
		approved = sql.NullInt64{Int64: p.ApprovedAt, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO plans (id, run_id, plan_md, tasks_json, approved_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, p.RunID, p.PlanMD, p.TasksJSON, approved, now)
	if err != nil {
		return "", fmt.Errorf("insert plan: %w", err)
	}
	return id, nil
}

// GetPlanByRun returns the single plan for run `runID`. Returns ErrNotFound
// if no plan has been persisted yet. (Phase 2 produces at most one plan per run.)
func (s *Store) GetPlanByRun(ctx context.Context, runID string) (Plan, error) {
	var p Plan
	var approved sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, plan_md, tasks_json, approved_at, created_at
		FROM plans WHERE run_id = ? ORDER BY created_at DESC LIMIT 1
	`, runID).Scan(&p.ID, &p.RunID, &p.PlanMD, &p.TasksJSON, &approved, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Plan{}, fmt.Errorf("get plan for run %q: %w", runID, ErrNotFound)
		}
		return Plan{}, fmt.Errorf("get plan for run %q: %w", runID, err)
	}
	p.ApprovedAt = approved.Int64
	return p, nil
}
