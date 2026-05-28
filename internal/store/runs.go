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
	var r Run
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, intake_kind, COALESCE(intake_ref, ''), spec_md, gates_json, phase, created_at, updated_at
		FROM runs WHERE id = ?
	`, id).Scan(&r.ID, &r.ProjectID, &r.IntakeKind, &r.IntakeRef, &r.SpecMD, &r.GatesJSON, &r.Phase, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, fmt.Errorf("get run %q: %w", id, ErrNotFound)
		}
		return Run{}, fmt.Errorf("get run %q: %w", id, err)
	}
	return r, nil
}

func (s *Store) ProjectByName(ctx context.Context, name string) (Project, error) {
	var p Project
	var verCmd sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, repo_path, default_gates_json, verification_command, created_at
		FROM projects WHERE name = ?
	`, name).Scan(&p.ID, &p.Name, &p.RepoPath, &p.DefaultGatesJSON, &verCmd, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, fmt.Errorf("project by name %q: %w", name, ErrNotFound)
		}
		return Project{}, fmt.Errorf("project by name %q: %w", name, err)
	}
	p.VerificationCommand = verCmd.String
	return p, nil
}
