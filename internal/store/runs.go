package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Lithial/ManageBot/internal/ids"
)

// ErrNotFound is returned when a requested entity does not exist in the store.
var ErrNotFound = errors.New("not found")

// ErrRunNotTerminal is returned by destructive run operations (e.g. prune) that
// require the run to be in a terminal phase (done | failed | killed) first.
var ErrRunNotTerminal = errors.New("run is not terminal")

// scanProject scans a single projects row into a Project value, converting
// the nullable verification_command column into a plain string.
func scanProject(row *sql.Row) (Project, error) {
	var p Project
	var verCmd sql.NullString
	if err := row.Scan(&p.ID, &p.Name, &p.RepoPath, &p.DefaultGatesJSON, &verCmd, &p.CreatedAt); err != nil {
		return Project{}, err
	}
	p.VerificationCommand = verCmd.String
	return p, nil
}

const projectColumns = `id, name, repo_path, default_gates_json, verification_command, created_at`

// scanRun scans one row from a *sql.Rows or *sql.Row into a Run value.
// COALESCE on intake_ref is applied at the SELECT site, not here.
func scanRun(scanner interface {
	Scan(dest ...any) error
}) (Run, error) {
	var r Run
	if err := scanner.Scan(&r.ID, &r.ProjectID, &r.IntakeKind, &r.IntakeRef, &r.SpecMD, &r.GatesJSON, &r.Phase, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Run{}, err
	}
	return r, nil
}

const runColumns = `id, project_id, intake_kind, COALESCE(intake_ref, ''), spec_md, gates_json, phase, created_at, updated_at`

type Project struct {
	ID                  string
	Name                string
	RepoPath            string
	DefaultGatesJSON    string
	VerificationCommand string
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
	var verCmd sql.NullString
	if p.VerificationCommand != "" {
		verCmd = sql.NullString{String: p.VerificationCommand, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, name, repo_path, default_gates_json, verification_command, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, p.Name, p.RepoPath, p.DefaultGatesJSON, verCmd, now)
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
	row := s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id)
	r, err := scanRun(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, fmt.Errorf("get run %q: %w", id, ErrNotFound)
		}
		return Run{}, fmt.Errorf("get run %q: %w", id, err)
	}
	return r, nil
}

func (s *Store) ProjectByName(ctx context.Context, name string) (Project, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE name = ?`, name)
	p, err := scanProject(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, fmt.Errorf("project by name %q: %w", name, ErrNotFound)
		}
		return Project{}, fmt.Errorf("project by name %q: %w", name, err)
	}
	return p, nil
}

// UpdateRunPhase sets the phase of run `id` and bumps updated_at. Returns
// ErrNotFound if no row matches.
func (s *Store) UpdateRunPhase(ctx context.Context, id string, phase string) error {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE runs SET phase = ?, updated_at = ? WHERE id = ?
	`, phase, now, id)
	if err != nil {
		return fmt.Errorf("update run phase: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update run phase rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update run %q phase: %w", id, ErrNotFound)
	}
	return nil
}

// ListRunsByPhase returns all runs currently in the given phase, ordered by
// created_at ascending so the oldest pending run is picked up first.
func (s *Store) ListRunsByPhase(ctx context.Context, phase string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE phase = ? ORDER BY created_at ASC`, phase)
	if err != nil {
		return nil, fmt.Errorf("list runs by phase: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// ListRuns returns all runs newest-first (created_at desc, rowid desc to break
// same-second ties). Used by read clients like the TUI dashboard.
func (s *Store) ListRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs ORDER BY created_at DESC, rowid DESC`)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// GetProject returns a project by id. Companion to ProjectByName.
func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, id)
	p, err := scanProject(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, fmt.Errorf("get project %q: %w", id, ErrNotFound)
		}
		return Project{}, fmt.Errorf("get project %q: %w", id, err)
	}
	return p, nil
}
