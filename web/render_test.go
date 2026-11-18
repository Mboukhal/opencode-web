package web

import (
	"bytes"
	"context"
	"html/template"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opencodeweb/auth"
	"opencodeweb/config"
	"opencodeweb/ocjson"
	"opencodeweb/projects"
	"opencodeweb/runner"
	"opencodeweb/store"
)

// newTestServer builds a Server backed by a temp SQLite file.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		PublicBaseURL:      "http://localhost:8080",
		GoogleClientID:     "id",
		GoogleClientSecret: "secret",
		AllowedEmails:      []string{"lios80466@gmail.com"},
		DataDir:            dir,
		ProjectsDir:        filepath.Join(dir, "projects"),
		ConfigDir:          filepath.Join(dir, "config"),
		StateDir:           filepath.Join(dir, "state"),
		DBPath:             filepath.Join(dir, "app.db"),
		OpenCodeTimeout:    time.Minute,
	}
	os.MkdirAll(cfg.ProjectsDir, 0o755)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(cfg, log, st, auth.New(cfg, st),
		projects.New(cfg.ProjectsDir, "", st),
		runner.New("/bin/true", cfg.ConfigDir, cfg.StateDir, "m", "", dir, time.Minute, nil))
	if err != nil {
		t.Fatalf("building server: %v", err)
	}
	return srv
}

// renderTemplate executes a page template with the layout into a string.
func renderTemplate(t *testing.T, srv *Server, name string, data pageData) string {
	t.Helper()
	tmpl, ok := srv.tmpl[name]
	if !ok {
		t.Fatalf("template %q not found", name)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("rendering %q: %v", name, err)
	}
	return buf.String()
}

func TestTemplatesParse(t *testing.T) {
	if len(mustTemplates(t)) != 4 {
		t.Fatalf("expected 4 page templates")
	}
}

func mustTemplates(t *testing.T) map[string]*template.Template {
	t.Helper()
	m, err := parseTemplates()
	if err != nil {
		t.Fatalf("parsing templates: %v", err)
	}
	return m
}

func TestLoginRenders(t *testing.T) {
	srv := newTestServer(t)
	out := renderTemplate(t, srv, "login", pageData{Title: "Sign in", SeedEmail: "lios80466@gmail.com"})
	for _, want := range []string{"Continue with Google", "/auth/google", "lios80466@gmail.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("login page missing %q", want)
		}
	}
}

func TestProjectsRendersStates(t *testing.T) {
	srv := newTestServer(t)
	u := &store.User{Email: "lios80466@gmail.com", Name: "M"}
	page := projectsPage{
		Repos: []repoView{
			{Owner: "Mboukhal", Repo: "loaded-repo", Loaded: true, Status: projects.StatusUpToDate},
			{Owner: "Mboukhal", Repo: "stale-repo", Loaded: true, Status: projects.StatusBehind, Detail: "2 commit(s) behind"},
			{Owner: "Mboukhal", Repo: "fresh-repo", Status: projects.StatusNotLoaded},
		},
		Logins: []string{"Mboukhal"},
	}
	out := renderTemplate(t, srv, "projects", pageData{Title: "Projects", User: u, Data: page})

	checks := map[string]string{
		"loaded state": "pill-up_to_date",
		"behind state": "update available",
		"not loaded":   "not loaded",
		"pull button":  `action="/projects/pull"`,
		"clone button": `action="/projects/clone"`,
		"repo link":    "/projects/Mboukhal/loaded-repo",
		"manual clone": "Clone a private repository by name",
	}
	for label, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("%s: output missing %q", label, want)
		}
	}
}

func TestProjectPageRenders(t *testing.T) {
	srv := newTestServer(t)
	u := &store.User{Email: "lios80466@gmail.com"}
	page := projectPage{
		Project:  store.Project{Owner: "Mboukhal", Repo: "demo", Path: "/data/projects/Mboukhal/demo", Role: "senior Go engineer"},
		Status:   projects.StatusUpToDate,
		Sessions: []SessionView{{ID: "ses_1", Title: "Fix bridge", Updated: time.Now().UnixMilli()}},
	}
	out := renderTemplate(t, srv, "project", pageData{Title: "demo", User: u, Data: page})
	for _, want := range []string{`action="/run"`, "senior Go engineer", "Fix bridge", "Remove local clone"} {
		if !strings.Contains(out, want) {
			t.Errorf("project page missing %q", want)
		}
	}
}

func TestRunPageRenders(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	job, err := srv.runner.Start(context.Background(), "o", "r", dir, "hello", "", "", "")
	if err != nil && !strings.Contains(err.Error(), "starting") {
		t.Fatalf("starting job: %v", err)
	}
	jd := runData{ID: "x"}
	if job != nil {
		jd = runData{ID: job.ID, Owner: "o", Repo: "r", Prompt: "hello", StartedAt: job.StartedAt()}
	}
	out := renderTemplate(t, srv, "run", pageData{Title: "Run", Data: jd})
	// The container carries the run id; app.js builds the SSE URL from it.
	for _, want := range []string{`id="stream"`, `data-run="` + jd.ID + `"`} {
		if !strings.Contains(out, want) {
			t.Errorf("run page missing %q", want)
		}
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    bool
		typ     string
		session string
	}{
		{"text", `{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"OK"}}`, true, "text", "ses_1"},
		{"tool", `{"type":"tool_use","sessionID":"ses_1","part":{"type":"tool","tool":"shell","state":{"status":"completed","input":{"command":"ls"}}}}`, true, "tool", "ses_1"},
		{"error", `{"type":"error","sessionID":"ses_1","error":{"type":"provider.auth","message":"bad key"}}`, true, "error", "ses_1"},
		{"log line", `timestamp=2026 level=INFO message=x`, false, "", ""},
		{"empty text", `{"type":"text","sessionID":"ses_1","part":{"text":"  "}}`, false, "", "ses_1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ocjson.Parse(tt.line)
			if ok != tt.want {
				t.Fatalf("ok = %v, want %v", ok, tt.want)
			}
			if !tt.want {
				return
			}
			if got.Event.Type != tt.typ {
				t.Errorf("type = %q, want %q", got.Event.Type, tt.typ)
			}
			if got.SessionID != tt.session {
				t.Errorf("session = %q, want %q", got.SessionID, tt.session)
			}
		})
	}
}
