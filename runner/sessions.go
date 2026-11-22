package runner

import (
	"context"
	"errors"

	"opencodeweb/opencode"
)

// Session is a past OpenCode session for a project.
type Session struct {
	ID      string
	Title   string
	Updated int64 // Unix milliseconds
	Created int64
}

// ListSessions returns recent sessions created in dir, newest first.
func (m *Manager) ListSessions(ctx context.Context, dir string, limit int) ([]Session, error) {
	client := m.Client()
	if client == nil {
		return nil, errors.New("the OpenCode server is not running yet")
	}
	list, err := client.ListSessions(ctx, dir, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(list))
	for _, s := range list {
		// The server filters by directory, but older builds may ignore the
		// filter, so keep the check.
		if d := s.Location.Directory; d != "" && d != dir {
			continue
		}
		out = append(out, Session{
			ID: s.ID, Title: sessionTitle(s), Updated: s.Updated(), Created: s.Time.Created,
		})
	}
	return out, nil
}

func sessionTitle(s opencode.Session) string {
	if s.Title != "" {
		return s.Title
	}
	return "Untitled session"
}
