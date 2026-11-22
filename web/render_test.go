package web

import (
	"bytes"
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
	"opencodeweb/oconfig"
	"opencodeweb/opencode"
	"opencodeweb/projects"
	"opencodeweb/runner"
	"opencodeweb/service"
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
		OpenCodeModel:      "opencode/muse-spark-1.3",
		SkillsSrc:          filepath.Join(dir, "skills"),
		ProviderKeys:       map[string]string{"openrouter": "sk-test"},
	}
	os.MkdirAll(cfg.ProjectsDir, 0o755)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// No OpenCode server in unit tests: the client provider returns nil, which
	// the handlers treat as "server still starting".
	rm := runner.New(func() *opencode.Client { return nil }, time.Minute)
	svc := service.New(service.Settings{
		Bin: cfg.OpenCodeBin, ConfigDir: cfg.ConfigDir, StateDir: cfg.StateDir,
		Home: dir, Log: log,
	})
	ocfg := &oconfig.Config{
		ConfigDir: cfg.ConfigDir, StateDir: cfg.StateDir, Home: dir,
		Model: cfg.OpenCodeModel, Log: log,
	}
	srv, err := New(cfg, log, st, auth.New(cfg, st),
		projects.New(cfg.ProjectsDir, "", st), rm, svc, ocfg)
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
	// login, projects, project, run, job, mobile, settings
	if n := len(mustTemplates(t)); n != 7 {
		t.Fatalf("expected 7 page templates, got %d", n)
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
		"pull button":  `js-pull`,
		"clone button": `js-clone`,
		"search box":   `id="q"`,
		"filter chips": `data-filter="not_loaded"`,
		"clone dialog": `id="clone-dialog"`,
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
	jd := runData{ID: "x", Owner: "o", Repo: "r", Prompt: "hello",
		SessionID: "ses_123", StartedAt: time.Now()}
	out := renderTemplate(t, srv, "run", pageData{Title: "Run", Data: jd})
	// The container carries the run id; app.js builds the SSE URL from it.
	for _, want := range []string{
		`id="stream"`, `data-run="x"`, `id="cancel"`, "ses_123", "Continue on phone",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("run page missing %q", want)
		}
	}
}

func TestMobilePageRenders(t *testing.T) {
	srv := newTestServer(t)
	u := &store.User{Email: "lios80466@gmail.com"}
	page := mobilePage{
		PairURL:    "https://code.example.dev/auth/connect/abc123",
		QR:         template.HTML(qrHTML("https://code.example.dev/auth/connect/abc123")),
		ServerAddr: "127.0.0.1:4096",
		AppURL:     "https://opencode.ai/mobile",
		Ready:      true,
	}
	out := renderTemplate(t, srv, "mobile", pageData{Title: "Mobile", User: u, Data: page})
	for _, want := range []string{
		"OpenCode Mobile",
		"/auth/connect/abc123",
		"<svg",
		`action="/mobile/pair"`,
		"TestFlight",
		"opencode pair --remote",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mobile page missing %q", want)
		}
	}
}

func TestProjectPageShowsPushAndSkills(t *testing.T) {
	srv := newTestServer(t)
	u := &store.User{Email: "lios80466@gmail.com"}
	page := projectPage{
		Project:   store.Project{Owner: "Mboukhal", Repo: "demo", Path: "/data/projects/Mboukhal/demo"},
		Status:    projects.StatusUpToDate,
		Skills:    []string{"analytics", "backtest-expert"},
		KeyExists: true,
		KeyPath:   "/data/ssh/id_ed25519",
	}
	out := renderTemplate(t, srv, "project", pageData{Title: "demo", User: u, Data: page})
	for _, want := range []string{
		`action="/projects/push"`, "Commit &amp; push", "Pull",
		"backtest-expert", "/mobile",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("project page missing %q", want)
		}
	}
}

func TestTranslate(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		session string
		want    bool
		typ     string
		text    string
	}{
		{
			name:    "text delta start",
			raw:     `{"type":"session.text.delta","data":{"sessionID":"ses_1","ordinal":0,"delta":"Hel"}}`,
			session: "ses_1", want: true, typ: "text", text: "Hel",
		},
		{
			name:    "text delta continues",
			raw:     `{"type":"session.text.delta","data":{"sessionID":"ses_1","ordinal":3,"delta":"lo"}}`,
			session: "ses_1", want: true, typ: "text", text: "lo",
		},
		{
			name:    "other session ignored",
			raw:     `{"type":"session.text.delta","data":{"sessionID":"ses_2","ordinal":0,"delta":"x"}}`,
			session: "ses_1", want: false,
		},
		{
			name:    "full text is not duplicated after deltas",
			raw:     `{"type":"session.text.ended","data":{"sessionID":"ses_1","ordinal":0,"text":"Hello"}}`,
			session: "ses_1", want: false,
		},
		{
			name:    "tool call",
			raw:     `{"type":"session.tool.called","data":{"sessionID":"ses_1","id":"t1","input":{"command":"ls"},"native":{"name":"shell"}}}`,
			session: "ses_1", want: true, typ: "tool",
		},
		{
			name:    "a failed step is reported but does not end the turn",
			raw:     `{"type":"session.step.failed","data":{"sessionID":"ses_1","error":{"type":"provider.auth","message":"User not found."}}}`,
			session: "ses_1", want: true, typ: "error",
		},
		{
			name:    "a failed turn ends the run",
			raw:     `{"type":"session.execution.failed","data":{"sessionID":"ses_1","error":{"type":"provider.auth","message":"User not found."}}}`,
			session: "ses_1", want: true, typ: "done",
		},
		{
			name:    "finished run",
			raw:     `{"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`,
			session: "ses_1", want: true, typ: "done",
		},
		{
			name:    "unrelated event",
			raw:     `{"type":"project.updated","data":{"id":"p1"}}`,
			session: "ses_1", want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := opencode.Translate([]byte(tt.raw), tt.session)
			if ok != tt.want {
				t.Fatalf("ok = %v, want %v", ok, tt.want)
			}
			if !tt.want {
				return
			}
			if got.Type != tt.typ {
				t.Errorf("type = %q, want %q", got.Type, tt.typ)
			}
			if tt.text != "" && got.Text != tt.text {
				t.Errorf("text = %q, want %q", got.Text, tt.text)
			}
		})
	}
}

// TestPairOriginWarning guards the failure where the pairing link pointed at
// the web app instead of the OpenCode server.
func TestPairOriginWarning(t *testing.T) {
	srv := newTestServer(t)
	srv.cfg.PublicBaseURL = "https://code.mboukhal.dev"

	if !srv.cfg.PairOriginIsApp() {
		t.Fatal("expected the default origin to be flagged as the web app")
	}

	u := &store.User{Email: "lios80466@gmail.com"}
	page := mobilePage{
		PairWarning: "Phones cannot pair through https://code.mboukhal.dev",
		Ready:       true,
	}
	out := renderTemplate(t, srv, "mobile", pageData{Title: "Mobile", User: u, Data: page})
	if !strings.Contains(out, "alert-error") || !strings.Contains(out, "Phones cannot pair through") {
		t.Error("mobile page did not surface the pairing origin warning")
	}

	// Once the pairing host is set separately, no warning and the origin is used.
	srv.cfg.MobilePairURL = "https://opencode.mboukhal.dev"
	if srv.cfg.PairOriginIsApp() {
		t.Error("separate pairing origin still flagged as the app")
	}
	if got := srv.cfg.PairOrigin(); got != "https://opencode.mboukhal.dev" {
		t.Errorf("PairOrigin = %q", got)
	}
}
