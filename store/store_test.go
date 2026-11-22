package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrationAddsPushColumn covers databases created before push support:
// Open must add the column rather than fail on the ALTER.
func TestMigrationAddsPushColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// Build a database with the pre-push schema.
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		owner TEXT NOT NULL, repo TEXT NOT NULL, path TEXT NOT NULL,
		model TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL DEFAULT '', cloned_at INTEGER NOT NULL,
		last_pull_at INTEGER, UNIQUE(owner, repo))`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO projects (owner, repo, path, cloned_at) VALUES ('o','r','/p',1)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open on old schema: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.MarkPushed(ctx, "o", "r"); err != nil {
		t.Fatalf("MarkPushed: %v", err)
	}
	p, err := st.Project(ctx, "o", "r")
	if err != nil || p == nil {
		t.Fatalf("Project: %v", err)
	}
	if p.LastPushAt == nil {
		t.Fatal("LastPushAt not set after migration")
	}
	if time.Since(*p.LastPushAt) > time.Minute {
		t.Errorf("LastPushAt = %v, want recent", p.LastPushAt)
	}
}

// TestOpenIsRepeatable ensures the migration is safe to run on a fresh schema
// where the column already exists.
func TestOpenIsRepeatable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	for i := 0; i < 3; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		st.Close()
	}
}
