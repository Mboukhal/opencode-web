// Package projects manages the on-disk clones: cloning with the deploy key,
// checking whether a clone is behind its remote, pulling, and writing the
// per-project OpenCode config (model + agent role).
package projects

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opencodeweb/store"
)

// Status describes the sync state of one repository.
type Status string

const (
	StatusNotLoaded Status = "not_loaded"
	StatusUpToDate  Status = "up_to_date"
	StatusBehind    Status = "behind" // remote has commits we do not have
	StatusAhead     Status = "ahead"  // local has commits not on remote
	StatusDiverged  Status = "diverged"
	StatusDirty     Status = "dirty" // uncommitted changes
	StatusError     Status = "error"
	StatusCloning   Status = "cloning"
)

// Manager owns the projects directory.
type Manager struct {
	root    string
	keyPath string
	store   *store.Store

	mu    sync.Mutex
	busy  map[string]bool
	gitEn []string // extra env vars for git
}

// New creates a Manager. keyPath may be "" to use the default ssh agent.
func New(root, keyPath string, st *store.Store) *Manager {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if keyPath != "" {
		env = append(env,
			"GIT_SSH_COMMAND=ssh -i "+keyPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o BatchMode=yes",
		)
	}
	return &Manager{root: root, keyPath: keyPath, store: st, busy: make(map[string]bool), gitEn: env}
}

// Dir is the local path for a repository.
func (m *Manager) Dir(owner, repo string) string { return filepath.Join(m.root, owner, repo) }

// IsBusy reports whether an operation is running for this repo.
func (m *Manager) IsBusy(owner, repo string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy[owner+"/"+repo]
}

func (m *Manager) claim(owner, repo string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := owner + "/" + repo
	if m.busy[k] {
		return false
	}
	m.busy[k] = true
	return true
}

func (m *Manager) release(owner, repo string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.busy, owner+"/"+repo)
}

// Clone clones owner/repo into the projects directory and registers it.
func (m *Manager) Clone(ctx context.Context, owner, repo, model, agent, role string) error {
	if !m.claim(owner, repo) {
		return fmt.Errorf("an operation is already running for %s/%s", owner, repo)
	}
	defer m.release(owner, repo)

	dir := m.Dir(owner, repo)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return fmt.Errorf("%s/%s is already cloned", owner, repo)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("creating projects dir: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	remote := fmt.Sprintf("git@github.com:%s/%s.git", owner, repo)
	if _, err := m.git(ctx, m.root, "clone", "--recurse-submodules", remote, dir); err != nil {
		return err
	}

	if _, err := m.store.AddProject(ctx, store.Project{
		Owner: owner, Repo: repo, Path: dir, Model: model, Agent: agent, Role: role,
	}); err != nil {
		return err
	}
	return m.WriteConfig(ctx, owner, repo, model, agent, role)
}

// Pull fetches and fast-forwards the clone. It refuses to touch a dirty tree.
func (m *Manager) Pull(ctx context.Context, owner, repo string) error {
	if !m.claim(owner, repo) {
		return fmt.Errorf("an operation is already running for %s/%s", owner, repo)
	}
	defer m.release(owner, repo)

	dir := m.Dir(owner, repo)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return fmt.Errorf("%s/%s is not cloned", owner, repo)
	}
	if out, err := m.git(ctx, dir, "status", "--porcelain"); err != nil {
		return err
	} else if strings.TrimSpace(out) != "" {
		return fmt.Errorf("working tree has uncommitted changes; commit or stash before pulling")
	}
	if _, err := m.git(ctx, dir, "pull", "--ff-only", "--recurse-submodules"); err != nil {
		return err
	}
	return m.store.MarkPulled(ctx, owner, repo)
}

// Status inspects a clone against its remote without modifying it.
func (m *Manager) Status(ctx context.Context, owner, repo string) (Status, string) {
	dir := m.Dir(owner, repo)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return StatusNotLoaded, ""
	}
	if m.IsBusy(owner, repo) {
		return StatusCloning, ""
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if out, err := m.git(ctx, dir, "status", "--porcelain"); err != nil {
		return StatusError, err.Error()
	} else if strings.TrimSpace(out) != "" {
		return StatusDirty, ""
	}

	if _, err := m.git(ctx, dir, "fetch", "--quiet", "--prune", "origin"); err != nil {
		return StatusError, "fetch failed (check the deploy key)"
	}

	local, err := m.git(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return StatusError, "no upstream branch"
	}
	local = strings.TrimSpace(local)

	counts, err := m.git(ctx, dir, "rev-list", "--left-right", "--count", local+"...@{u}")
	if err != nil {
		return StatusError, err.Error()
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		return StatusError, "unexpected rev-list output"
	}
	var ahead, behind int
	fmt.Sscan(fields[0], &ahead)
	fmt.Sscan(fields[1], &behind)

	switch {
	case ahead > 0 && behind > 0:
		return StatusDiverged, fmt.Sprintf("%d ahead, %d behind", ahead, behind)
	case behind > 0:
		return StatusBehind, fmt.Sprintf("%d commit(s) behind", behind)
	case ahead > 0:
		return StatusAhead, fmt.Sprintf("%d commit(s) ahead", ahead)
	default:
		return StatusUpToDate, ""
	}
}

// WriteConfig writes .opencode/opencode.json and .opencode/agents/<id>.md in the
// project so sessions started there inherit the model and agent role.
func (m *Manager) WriteConfig(ctx context.Context, owner, repo, model, agent, role string) error {
	dir := m.Dir(owner, repo)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("project dir missing: %w", err)
	}

	cfgDir := filepath.Join(dir, ".opencode")
	if err := os.MkdirAll(filepath.Join(cfgDir, "agents"), 0o755); err != nil {
		return err
	}

	// OpenCode resolves --agent by id, so the markdown definition must exist
	// before config references it.
	agentID := agent
	if role != "" {
		if agentID == "" {
			agentID = "project"
		}
		desc := role
		if i := strings.IndexByte(desc, '\n'); i > 0 {
			desc = desc[:i]
		}
		if len(desc) > 120 {
			desc = desc[:120]
		}
		md := fmt.Sprintf("---\ndescription: %s\nmode: primary\n---\n\n%s\n", desc, role)
		if err := os.WriteFile(filepath.Join(cfgDir, "agents", agentID+".md"), []byte(md), 0o644); err != nil {
			return fmt.Errorf("writing agent file: %w", err)
		}
	}

	var b bytes.Buffer
	b.WriteString("{\n  \"$schema\": \"https://opencode.ai/config.json\"")
	if model != "" {
		fmt.Fprintf(&b, ",\n  \"model\": %q", model)
	}
	if role != "" {
		fmt.Fprintf(&b, ",\n  \"default_agent\": %q", agentID)
	}
	b.WriteString("\n}\n")

	if err := os.WriteFile(filepath.Join(cfgDir, "opencode.json"), b.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing opencode.json: %w", err)
	}
	return nil
}

// Remove deletes a clone and its database row.
func (m *Manager) Remove(ctx context.Context, owner, repo string) error {
	if !m.claim(owner, repo) {
		return fmt.Errorf("an operation is already running for %s/%s", owner, repo)
	}
	defer m.release(owner, repo)

	dir := m.Dir(owner, repo)
	if !strings.HasPrefix(filepath.Clean(dir)+string(os.PathSeparator), filepath.Clean(m.root)+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to remove path outside projects dir")
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing clone: %w", err)
	}
	return m.store.DeleteProject(ctx, owner, repo)
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), m.gitEn...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", args[0], firstLines(msg, 3))
	}
	return out.String(), nil
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " / ")
}
