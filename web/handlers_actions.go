package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opencodeweb/event"
	"opencodeweb/github"
	"opencodeweb/jobs"
	"opencodeweb/models"
	"opencodeweb/projects"
	"opencodeweb/store"
)

func parseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

// formRepo validates the owner/repo form values.
func formRepo(r *http.Request) (owner, repo string, ok bool) {
	owner = strings.TrimSpace(r.FormValue("owner"))
	repo = strings.TrimSpace(r.FormValue("repo"))
	return owner, repo, github.ValidName(owner) && github.ValidName(repo)
}

func (s *Server) handleSyncRepos(w http.ResponseWriter, r *http.Request, u *store.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	repos, err := s.gh.ListRepos(ctx)
	if err != nil {
		s.log.Error("syncing github repos", "err", err)
		http.Redirect(w, r, "/projects?error="+urlEncode("GitHub sync failed: "+err.Error()), http.StatusFound)
		return
	}
	if err := s.store.ReplaceRepoCache(ctx, s.gh.Login(), repos); err != nil {
		s.log.Error("storing github repos", "err", err)
		http.Redirect(w, r, "/projects?error="+urlEncode("Could not store repos"), http.StatusFound)
		return
	}
	s.store.SetSetting(ctx, "repos_synced_at", strconv.FormatInt(time.Now().Unix(), 10))
	http.Redirect(w, r, "/projects", http.StatusFound)
}

func (s *Server) handleClone(w http.ResponseWriter, r *http.Request, u *store.User) {
	owner, repo, ok := formRepo(r)
	if !ok {
		http.Error(w, "invalid repository name", http.StatusBadRequest)
		return
	}
	model := r.FormValue("model")
	agent := r.FormValue("agent")
	role := r.FormValue("role")

	job := s.jobs.Start(jobs.KindClone, owner, repo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer cancel()
		err := s.projects.Clone(ctx, owner, repo, model, agent, role, job.Log)
		if err != nil {
			s.log.Error("clone failed", "repo", owner+"/"+repo, "err", err)
			s.store.SetSetting(ctx, "last_error", err.Error())
		}
		job.Finish(err)
	}()

	// The dialog follows the job over SSE; plain form posts land on the job page.
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusAccepted, map[string]string{"job": job.ID})
		return
	}
	http.Redirect(w, r, "/jobs/"+job.ID, http.StatusFound)
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request, u *store.User) {
	owner, repo, ok := formRepo(r)
	if !ok {
		http.Error(w, "invalid repository name", http.StatusBadRequest)
		return
	}
	job := s.jobs.Start(jobs.KindPull, owner, repo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := s.projects.Pull(ctx, owner, repo, job.Log)
		if err != nil {
			s.log.Error("pull failed", "repo", owner+"/"+repo, "err", err)
			s.store.SetSetting(ctx, "last_error", err.Error())
		}
		job.Finish(err)
	}()

	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusAccepted, map[string]string{"job": job.ID})
		return
	}
	http.Redirect(w, r, "/jobs/"+job.ID, http.StatusFound)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request, u *store.User) {
	owner, repo, ok := formRepo(r)
	if !ok {
		http.Error(w, "invalid repository name", http.StatusBadRequest)
		return
	}
	if err := s.projects.Remove(r.Context(), owner, repo); err != nil {
		s.log.Error("remove failed", "repo", owner+"/"+repo, "err", err)
		http.Redirect(w, r, "/projects/"+owner+"/"+repo, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/projects", http.StatusFound)
}

func (s *Server) handleProjectConfig(w http.ResponseWriter, r *http.Request, u *store.User) {
	owner, repo, ok := formRepo(r)
	if !ok {
		http.Error(w, "invalid repository name", http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(r.FormValue("model"))
	agent := strings.TrimSpace(r.FormValue("agent"))
	role := strings.TrimSpace(r.FormValue("role"))

	if err := s.store.SetProjectConfig(r.Context(), owner, repo, model, agent, role); err != nil {
		s.log.Error("saving project config", "err", err)
	}
	if err := s.projects.WriteConfig(r.Context(), owner, repo, model, agent, role); err != nil {
		s.log.Error("writing project opencode config", "err", err)
	}
	http.Redirect(w, r, "/projects/"+owner+"/"+repo, http.StatusFound)
}

type projectPage struct {
	Project  store.Project
	Status   projects.Status
	Detail   string
	Busy     bool
	Sessions []SessionView
}

// SessionView is a past session shown on the project page.
type SessionView struct {
	ID      string
	Title   string
	Updated int64
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request, u *store.User) {
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	if !github.ValidName(owner) || !github.ValidName(repo) {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.Project(r.Context(), owner, repo)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if p == nil {
		http.Redirect(w, r, "/projects", http.StatusFound)
		return
	}

	st, detail := s.projects.Status(r.Context(), owner, repo)
	page := projectPage{Project: *p, Status: st, Detail: detail, Busy: s.projects.IsBusy(owner, repo)}

	if st != projects.StatusNotLoaded {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		sess, err := s.runner.ListSessions(ctx, p.Path, 10)
		cancel()
		if err == nil {
			for _, x := range sess {
				page.Sessions = append(page.Sessions, SessionView{ID: x.ID, Title: x.Title, Updated: x.Updated})
			}
		}
	}
	s.render(w, r, "project", p.Name(), page)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request, u *store.User) {
	owner, repo, ok := formRepo(r)
	if !ok {
		http.Error(w, "invalid repository", http.StatusBadRequest)
		return
	}
	p, err := s.store.Project(r.Context(), owner, repo)
	if err != nil || p == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		http.Redirect(w, r, "/projects/"+owner+"/"+repo, http.StatusFound)
		return
	}
	sessionID := r.FormValue("session")

	// The agent must resolve by id: fall back to the file name the app writes
	// when the stored value is empty.
	agent := p.Agent
	if p.Role != "" && agent == "" {
		agent = "project"
	}

	// Per-project model wins; otherwise fall back to the configured chain
	// (paid model first, free model when credit runs out).
	chain := s.effectiveChain(r.Context())
	if p.Model != "" {
		chain = models.Chain{Primary: p.Model, Fallbacks: s.cfg.ModelChain.All()}
	}
	job, err := s.runner.Start(r.Context(), owner, repo, p.Path, prompt, sessionID, p.Model, agent, chain)
	if err != nil {
		s.log.Error("starting run", "err", err)
		http.Error(w, "could not start opencode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/run/"+job.ID, http.StatusFound)
}

// effectiveChain returns the configured chain, with the newest free OpenRouter
// model substituted as the last resort so it keeps working as free models
// rotate. The result is cached for an hour.
func (s *Server) effectiveChain(ctx context.Context) models.Chain {
	chain := s.cfg.ModelChain
	if s.cfg.OpenRouterKey == "" {
		return chain
	}

	s.freeMu.Lock()
	free, fetched := s.freeModel, s.freeFetched
	s.freeMu.Unlock()
	if fetched.IsZero() || time.Since(fetched) > time.Hour {
		if m := models.FreeModel(ctx, s.cfg.OpenRouterKey); m != "" {
			free = m
		}
		s.freeMu.Lock()
		s.freeModel, s.freeFetched = free, time.Now()
		s.freeMu.Unlock()
	}
	if free == "" {
		return chain
	}

	// Append the discovered model unless it is already in the chain.
	for _, id := range chain.All() {
		if id == free {
			return chain
		}
	}
	out := models.Chain{Primary: chain.Primary, Fallbacks: append([]string(nil), chain.Fallbacks...)}
	out.Fallbacks = append(out.Fallbacks, free)
	return out
}

func (s *Server) handleRunPage(w http.ResponseWriter, r *http.Request, u *store.User) {
	job := s.runner.Job(r.PathValue("id"))
	if job == nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "run", "Running", newRunData(job))
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request, u *store.User) {
	job := s.runner.Job(r.PathValue("id"))
	if job == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	history, ch, unsub := job.Subscribe()
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(ev event.Event) {
		fmt.Fprintf(w, "data: %s\n\n", eventJSON(ev))
		flusher.Flush()
	}
	for _, ev := range history {
		send(ev)
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			send(ev)
			if ev.Type == "done" {
				return
			}
		}
	}
}
