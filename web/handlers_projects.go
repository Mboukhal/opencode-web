package web

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"opencodeweb/projects"
	"opencodeweb/store"
)

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if u := s.currentUser(r); u != nil {
		http.Redirect(w, r, "/projects", http.StatusFound)
		return
	}
	s.render(w, r, "login", "Sign in", nil)
}

// repoView is one row in the project list.
type repoView struct {
	Owner       string
	Repo        string
	Description string
	Language    string
	PushedAt    string
	Loaded      bool
	Private     bool
	Status      projects.Status
	Detail      string
	Busy        bool
}

// Label is the human-readable state shown in the list.
func (v repoView) StateLabel() string {
	switch {
	case v.Busy:
		return "working"
	case !v.Loaded:
		return "not loaded"
	case v.Status == projects.StatusBehind:
		return "update available"
	case v.Status == projects.StatusDiverged:
		return "diverged"
	case v.Status == projects.StatusDirty:
		return "local changes"
	case v.Status == projects.StatusAhead:
		return "ahead"
	case v.Status == projects.StatusError:
		return "error"
	default:
		return "loaded"
	}
}

type projectsPage struct {
	Repos      []repoView
	Logins     []string
	LastSynced time.Time
	SeedEmail  string
	OpenCodeOK bool
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request, u *store.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	repos, err := s.buildRepoViews(ctx)
	if err != nil {
		s.log.Error("building repo list", "err", err)
	}

	lastSynced := time.Time{}
	if v, _ := s.store.GetSetting(r.Context(), "repos_synced_at"); v != "" {
		if n, err := parseInt64(v); err == nil {
			lastSynced = time.Unix(n, 0)
		}
	}

	s.render(w, r, "projects", "Projects", projectsPage{
		Repos:      repos,
		Logins:     []string{s.gh.Login()},
		LastSynced: lastSynced,
		SeedEmail:  u.Email,
	})
}

func (s *Server) buildRepoViews(ctx context.Context) ([]repoView, error) {
	cached, err := s.store.RepoCache(ctx, s.gh.Login())
	if err != nil {
		return nil, err
	}
	loaded, err := s.store.Projects(ctx)
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]store.Project, len(loaded))
	for _, p := range loaded {
		byKey[p.Owner+"/"+p.Repo] = p
	}

	views := make([]repoView, 0, len(cached)+len(loaded))
	seen := make(map[string]bool, len(cached))

	for _, c := range cached {
		key := c.Owner + "/" + c.Repo
		seen[key] = true
		v := repoView{
			Owner: c.Owner, Repo: c.Repo, Description: c.Description,
			Language: c.Language, PushedAt: c.PushedAt, Private: c.Private,
			Status: projects.StatusNotLoaded,
		}
		if p, ok := byKey[key]; ok {
			v.Loaded = true
			v.Status, v.Detail = s.projects.Status(ctx, p.Owner, p.Repo)
			v.Busy = s.projects.IsBusy(p.Owner, p.Repo)
		}
		views = append(views, v)
	}

	// Clones that are not in the public listing (private repos cloned by name).
	for _, p := range loaded {
		key := p.Owner + "/" + p.Repo
		if seen[key] {
			continue
		}
		v := repoView{Owner: p.Owner, Repo: p.Repo, Loaded: true, Private: true}
		v.Status, v.Detail = s.projects.Status(ctx, p.Owner, p.Repo)
		v.Busy = s.projects.IsBusy(p.Owner, p.Repo)
		views = append(views, v)
	}
	return views, nil
}
