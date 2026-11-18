package web

import (
	"fmt"
	"net/http"
	"time"

	"opencodeweb/jobs"
	"opencodeweb/store"
)

// jobView is the template view of a clone/pull job.
type jobView struct {
	ID      string
	Kind    string
	Owner   string
	Repo    string
	State   string
	Error   string
	Elapsed string
}

func newJobView(j *jobs.Job) jobView {
	return jobView{
		ID: j.ID, Kind: string(j.Kind), Owner: j.Owner, Repo: j.Repo,
		State: string(j.State()), Error: j.Err(),
		Elapsed: j.Elapsed().Round(time.Second).String(),
	}
}

func (s *Server) handleJobPage(w http.ResponseWriter, r *http.Request, u *store.User) {
	j := s.jobs.Get(r.PathValue("id"))
	if j == nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "job", "Clone", newJobView(j))
}

func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request, u *store.User) {
	j := s.jobs.Get(r.PathValue("id"))
	if j == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	history, ch, unsub := j.Subscribe()
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(ev jobs.Event) {
		fmt.Fprintf(w, "data: %s\n\n", eventJSON(ev))
		flusher.Flush()
	}
	for _, ev := range history {
		send(ev)
	}
	if j.State() != jobs.StateRunning {
		return
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
			if ev.Type == "state" {
				return
			}
		}
	}
}

// repoJSON is one row of the projects API, used by the client-side filter.
type repoJSON struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Status      string `json:"status"`
	Label       string `json:"label"`
	Loaded      bool   `json:"loaded"`
	Private     bool   `json:"private"`
	Detail      string `json:"detail"`
	JobID       string `json:"jobId,omitempty"`
}

func (s *Server) handleProjectsAPI(w http.ResponseWriter, r *http.Request, u *store.User) {
	views, err := s.buildRepoViews(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]repoJSON, 0, len(views))
	for _, v := range views {
		row := repoJSON{
			Owner: v.Owner, Repo: v.Repo, Description: v.Description, Language: v.Language,
			Status: string(v.Status), Label: v.StateLabel(), Loaded: v.Loaded,
			Private: v.Private, Detail: v.Detail,
		}
		if j := s.jobs.ActiveFor(v.Owner, v.Repo); j != nil {
			row.JobID = j.ID
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}
