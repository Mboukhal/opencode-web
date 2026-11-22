package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"opencodeweb/store"
)

// git runs a git command in dir and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupRepo creates a bare origin, a clone registered as a project, and points
// the clone's origin at the bare repo so push works without network access.
func setupRepo(t *testing.T) (mgr *Manager, dir, bare string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "origin.git")
	git(t, root, "init", "--bare", "-q", bare)

	st, err := store.Open(filepath.Join(root, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(filepath.Join(root, "projects"), "", st)
	dir = m.Dir("owner", "repo")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "clone", "-q", bare, dir)
	git(t, dir, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	git(t, dir, "push", "-q", "-u", "origin", "main")

	if _, err := st.AddProject(context.Background(), store.Project{
		Owner: "owner", Repo: "repo", Path: dir,
	}); err != nil {
		t.Fatal(err)
	}
	return m, dir, bare
}

func TestPushCommitsAndPushes(t *testing.T) {
	m, dir, _ := setupRepo(t)

	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("from phone\n"), 0o644)

	var logs []string
	err := m.Push(context.Background(), "owner", "repo", "add new file", true, func(s string) {
		logs = append(logs, s)
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	// The commit must exist on the remote branch now.
	log := git(t, dir, "log", "--oneline", "-1", "origin/main")
	if !strings.Contains(log, "add new file") {
		t.Errorf("remote tip = %q, want the new commit", log)
	}
	if !strings.Contains(strings.Join(logs, "|"), "pushed") {
		t.Errorf("progress did not report the push: %v", logs)
	}

	p, err := m.store.Project(context.Background(), "owner", "repo")
	if err != nil || p == nil {
		t.Fatalf("reloading project: %v", err)
	}
	if p.LastPushAt == nil {
		t.Error("LastPushAt was not recorded")
	}
}

func TestPushRefusesDirtyTreeWithoutCommitFlag(t *testing.T) {
	m, dir, _ := setupRepo(t)
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644)

	err := m.Push(context.Background(), "owner", "repo", "", false, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v, want a dirty-tree refusal", err)
	}
}

func TestPushRefusesToOverwriteRemote(t *testing.T) {
	m, _, bare := setupRepo(t)

	// Someone else pushes a commit we do not have.
	other := filepath.Join(t.TempDir(), "other")
	git(t, t.TempDir(), "clone", "-q", bare, other)
	git(t, other, "checkout", "-q", "-B", "main", "origin/main")
	os.WriteFile(filepath.Join(other, "remote.txt"), []byte("theirs"), 0o644)
	git(t, other, "add", "-A")
	git(t, other, "commit", "-q", "-m", "remote change")
	git(t, other, "push", "-q", "origin", "main")

	// Clean, but behind: push must refuse rather than force.
	if err := m.Push(context.Background(), "owner", "repo", "", false, func(string) {}); err == nil {
		t.Fatal("push succeeded although the branch is behind")
	}
}

func TestInspectKeyReportsMissingKey(t *testing.T) {
	m := New(t.TempDir(), filepath.Join(t.TempDir(), "missing"), nil)
	k := m.InspectKey()
	if k.Exists {
		t.Error("missing key reported as present")
	}
	if k.Path == "" {
		t.Error("key path not reported")
	}
}
