// Package web is the HTTP layer: routing, handlers, templates and static assets.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"opencodeweb/auth"
	"opencodeweb/config"
	"opencodeweb/github"
	"opencodeweb/jobs"
	"opencodeweb/projects"
	"opencodeweb/runner"
	"opencodeweb/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Server holds every dependency the handlers need.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	store    *store.Store
	auth     *auth.Manager
	projects *projects.Manager
	runner   *runner.Manager
	jobs     *jobs.Manager
	gh       *github.Client
	tmpl     map[string]*template.Template

	freeMu      sync.Mutex
	freeModel   string
	freeFetched time.Time
}

// New wires the HTTP layer.
func New(cfg *config.Config, log *slog.Logger, st *store.Store, am *auth.Manager,
	pm *projects.Manager, rm *runner.Manager) (*Server, error) {

	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, log: log, store: st, auth: am, projects: pm, runner: rm, jobs: jobs.New(),
		gh:   github.New(githubLogin(cfg)),
		tmpl: tmpl,
	}, nil
}

func githubLogin(cfg *config.Config) string {
	for _, o := range cfg.GitHubAllowedOrg {
		if strings.HasPrefix(o, "login:") {
			return strings.TrimPrefix(o, "login:")
		}
	}
	return "Mboukhal"
}

// Handler returns the root handler with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleHome)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/auth/google", http.StatusFound)
	})
	mux.HandleFunc("GET /auth/google", s.auth.StartAuth)
	mux.HandleFunc("GET /auth/google/callback", s.auth.FinishAuth)
	mux.HandleFunc("POST /logout", s.auth.Logout)

	mux.Handle("GET /projects", s.requireUser(s.handleProjects))
	mux.Handle("POST /projects/sync", s.requireUser(s.handleSyncRepos))
	mux.Handle("POST /projects/clone", s.requireUser(s.handleClone))
	mux.Handle("POST /projects/pull", s.requireUser(s.handlePull))
	mux.Handle("POST /projects/remove", s.requireUser(s.handleRemove))
	mux.Handle("POST /projects/config", s.requireUser(s.handleProjectConfig))
	mux.Handle("GET /projects/{owner}/{repo}", s.requireUser(s.handleProject))
	mux.Handle("GET /jobs/{id}", s.requireUser(s.handleJobPage))
	mux.Handle("GET /jobs/{id}/events", s.requireUser(s.handleJobEvents))
	mux.Handle("GET /api/projects", s.requireUser(s.handleProjectsAPI))

	mux.Handle("POST /run", s.requireUser(s.handleRun))
	mux.Handle("GET /run/{id}", s.requireUser(s.handleRunPage))
	mux.Handle("GET /run/{id}/events", s.requireUser(s.handleRunEvents))

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(mustSub(assets, "static"))))

	return s.withCommon(mux)
}

func (s *Server) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: https://lh3.googleusercontent.com https://avatars.githubusercontent.com; "+
				"style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).Round(time.Millisecond))
	})
}

// requireUser rejects unauthenticated requests and injects the user.
func (s *Server) requireUser(next func(http.ResponseWriter, *http.Request, *store.User)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.auth.CurrentUser(r)
		if err != nil {
			s.log.Error("resolving session", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if u == nil {
			if r.Header.Get("Accept") == "application/json" || strings.HasPrefix(r.URL.Path, "/run/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "signed out"})
				return
			}
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		next(w, r, u)
	})
}

// CurrentUser is exposed for templates that embed the user.
func (s *Server) currentUser(r *http.Request) *store.User {
	u, _ := s.auth.CurrentUser(r)
	return u
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

type pageData struct {
	Title     string
	User      *store.User
	BaseURL   string
	Flash     string
	Error     string
	Data      any
	SeedEmail string
	Nav       string
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title string, data any) {
	t, ok := s.tmpl[name]
	if !ok {
		s.log.Error("missing template", "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d := pageData{Title: title, User: s.currentUser(r), BaseURL: s.cfg.PublicBaseURL,
		Data: data, SeedEmail: s.cfg.SeedEmail()}
	if err := t.ExecuteTemplate(w, "layout", d); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Error("rendering template", "name", name, "err", err)
	}
}

func parseTemplates() (map[string]*template.Template, error) {
	entries, err := assets.ReadDir("templates")
	if err != nil {
		return nil, fmt.Errorf("reading templates: %w", err)
	}
	funcs := template.FuncMap{
		"shortEmail": func(s string) string {
			if i := strings.IndexByte(s, '@'); i > 0 {
				return s[:i]
			}
			return s
		},
		"since": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Format("2 Jan 2006")
		},
		"ago": func(ts int64) string {
			if ts == 0 {
				return "—"
			}
			d := time.Since(time.UnixMilli(ts))
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%dm ago", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%dh ago", int(d.Hours()))
			default:
				return fmt.Sprintf("%dd ago", int(d.Hours()/24))
			}
		},
	}
	out := make(map[string]*template.Template)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "layout.html" || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		t, err := template.New(e.Name()).Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", e.Name(), err)
		}
		out[strings.TrimSuffix(e.Name(), ".html")] = t
	}
	return out, nil
}
