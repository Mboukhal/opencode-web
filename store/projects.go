package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Project is a repository cloned into the projects directory.
type Project struct {
	ID         int64
	Owner      string
	Repo       string
	Path       string
	Model      string
	Agent      string
	Role       string
	ClonedAt   time.Time
	LastPullAt *time.Time
	LastPushAt *time.Time
}

// Name is the local directory name.
func (p Project) Name() string { return p.Owner + "/" + p.Repo }

// AddProject registers a cloned repository.
func (s *Store) AddProject(ctx context.Context, p Project) (*Project, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (owner, repo, path, model, agent, role, cloned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, repo) DO UPDATE SET path = excluded.path`,
		p.Owner, p.Repo, p.Path, p.Model, p.Agent, p.Role, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("adding project: %w", err)
	}
	return s.Project(ctx, p.Owner, p.Repo)
}

// Project looks up one project.
func (s *Store) Project(ctx context.Context, owner, repo string) (*Project, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, owner, repo, path, model, agent, role, cloned_at, last_pull_at, last_push_at
		FROM projects WHERE owner = ? AND repo = ?`, owner, repo)
	return scanProject(row)
}

// Projects lists all projects, newest first.
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner, repo, path, model, agent, role, cloned_at, last_pull_at, last_push_at
		FROM projects ORDER BY owner COLLATE NOCASE, repo COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProjectRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// SetProjectConfig stores per-project model/agent/role.
func (s *Store) SetProjectConfig(ctx context.Context, owner, repo, model, agent, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET model = ?, agent = ?, role = ? WHERE owner = ? AND repo = ?`,
		model, agent, role, owner, repo)
	return err
}

// MarkPushed records a successful push.
func (s *Store) MarkPushed(ctx context.Context, owner, repo string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET last_push_at = ? WHERE owner = ? AND repo = ?`,
		time.Now().Unix(), owner, repo)
	return err
}

// MarkPulled records a successful pull.
func (s *Store) MarkPulled(ctx context.Context, owner, repo string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET last_pull_at = ? WHERE owner = ? AND repo = ?`,
		time.Now().Unix(), owner, repo)
	return err
}

// DeleteProject forgets a project row (the clone on disk is removed by the caller).
func (s *Store) DeleteProject(ctx context.Context, owner, repo string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE owner = ? AND repo = ?`, owner, repo)
	return err
}

func scanProject(row *sql.Row) (*Project, error) {
	var p Project
	var cloned int64
	var pulled, pushed sql.NullInt64
	err := row.Scan(&p.ID, &p.Owner, &p.Repo, &p.Path, &p.Model, &p.Agent, &p.Role, &cloned, &pulled, &pushed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanning project: %w", err)
	}
	finishProject(&p, cloned, pulled, pushed)
	return &p, nil
}

func scanProjectRows(rows *sql.Rows) (*Project, error) {
	var p Project
	var cloned int64
	var pulled, pushed sql.NullInt64
	err := rows.Scan(&p.ID, &p.Owner, &p.Repo, &p.Path, &p.Model, &p.Agent, &p.Role, &cloned, &pulled, &pushed)
	if err != nil {
		return nil, fmt.Errorf("scanning project rows: %w", err)
	}
	finishProject(&p, cloned, pulled, pushed)
	return &p, nil
}

func finishProject(p *Project, cloned int64, pulled, pushed sql.NullInt64) {
	p.ClonedAt = time.Unix(cloned, 0)
	if pulled.Valid {
		t := time.Unix(pulled.Int64, 0)
		p.LastPullAt = &t
	}
	if pushed.Valid {
		t := time.Unix(pushed.Int64, 0)
		p.LastPushAt = &t
	}
}

// CachedRepo is a GitHub repo row from the last sync.
type CachedRepo struct {
	Owner       string
	Repo        string
	Private     bool
	Description string
	Language    string
	SizeKB      int64
	PushedAt    string
	Archived    bool
}

// ReplaceRepoCache swaps the cached repo list for an owner in one transaction.
func (s *Store) ReplaceRepoCache(ctx context.Context, owner string, repos []CachedRepo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM repo_cache WHERE owner = ?`, owner); err != nil {
		return fmt.Errorf("clearing repo cache: %w", err)
	}
	now := time.Now().Unix()
	for _, r := range repos {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO repo_cache (owner, repo, private, description, language, size_kb, pushed_at, archived, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			owner, r.Repo, boolInt(r.Private), r.Description, r.Language, r.SizeKB, r.PushedAt, boolInt(r.Archived), now)
		if err != nil {
			return fmt.Errorf("caching repo %s: %w", r.Repo, err)
		}
	}
	return tx.Commit()
}

// RepoCache lists cached repos for an owner.
func (s *Store) RepoCache(ctx context.Context, owner string) ([]CachedRepo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT owner, repo, private, description, language, size_kb, pushed_at, archived
		FROM repo_cache WHERE owner = ? ORDER BY pushed_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CachedRepo
	for rows.Next() {
		var r CachedRepo
		var priv, arch int
		if err := rows.Scan(&r.Owner, &r.Repo, &priv, &r.Description, &r.Language, &r.SizeKB, &r.PushedAt, &arch); err != nil {
			return nil, err
		}
		r.Private, r.Archived = priv == 1, arch == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSetting returns a setting value or "" when unset.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSetting writes a setting value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
