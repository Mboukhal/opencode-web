package runner

import (
	"context"
	"encoding/json"
	"fmt"

	"os/exec"
	"time"
)

// Session is a past OpenCode session for a project.
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Updated   int64  `json:"updated"`
	Created   int64  `json:"created"`
	Directory string `json:"directory"`
}

// ListSessions returns recent sessions created in dir.
func (m *Manager) ListSessions(ctx context.Context, dir string, limit int) ([]Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, m.bin, "session", "list", "--standalone", "--format", "json", "-n", fmt.Sprint(limit))
	cmd.Dir = dir
	cmd.Env = m.childEnv(dir)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing opencode sessions: %w", err)
	}
	var all []Session
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("decoding sessions: %w", err)
	}
	var mine []Session
	for _, s := range all {
		if s.Directory == "" || s.Directory == dir {
			mine = append(mine, s)
		}
	}
	return mine, nil
}
