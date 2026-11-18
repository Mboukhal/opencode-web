// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable the server needs. Zero values are filled with
// sane defaults by Load.
type Config struct {
	Addr string

	// PublicBaseURL is the externally reachable origin, e.g. https://code.mboukhal.dev.
	// Used to build the Google OAuth redirect URI.
	PublicBaseURL string

	GoogleClientID     string
	GoogleClientSecret string

	// AllowedEmails is the sign-in allow-list. The first entry is also the
	// seed user created on first boot.
	AllowedEmails []string

	DataDir     string // root of all persistent state
	ProjectsDir string // where repos are cloned
	ConfigDir   string // OpenCode config dir (skills, agents, opencode.json)
	StateDir    string // OpenCode data/state dir
	DBPath      string
	SSHKeyPath  string

	OpenCodeBin      string
	OpenCodeModel    string
	OpenCodeAgent    string
	OpenCodeTimeout  time.Duration
	GitHubAllowedOrg []string

	// OpenCodeEnv is passed to every opencode CLI child (provider keys, etc).
	OpenCodeEnv map[string]string
}

// Load reads configuration from the process environment.
func Load() (*Config, error) {
	dataDir := env("DATA_DIR", "/data")
	c := &Config{
		Addr:               env("ADDR", ":8080"),
		PublicBaseURL:      strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		AllowedEmails:      splitList(env("ALLOWED_EMAILS", "lios80466@gmail.com")),
		DataDir:            dataDir,
		ProjectsDir:        env("PROJECTS_DIR", dataDir+"/projects"),
		ConfigDir:          env("OPENCODE_CONFIG_DIR", dataDir+"/config"),
		StateDir:           env("OPENCODE_STATE_DIR", dataDir+"/state"),
		DBPath:             env("DB_PATH", dataDir+"/app/app.db"),
		SSHKeyPath:         env("SSH_KEY_PATH", dataDir+"/ssh/id_ed25519"),
		OpenCodeBin:        env("OPENCODE_BIN", "/usr/local/bin/opencode"),
		OpenCodeModel:      env("OPENCODE_MODEL", "opencode/muse-spark-1.3"),
		OpenCodeAgent:      env("OPENCODE_AGENT", ""),
		OpenCodeTimeout:    envDuration("OPENCODE_TIMEOUT", 30*time.Minute),
		GitHubAllowedOrg:   splitList(os.Getenv("GITHUB_ORGS")),
		OpenCodeEnv:        parseEnv(os.Getenv("OPENCODE_ENV")),
	}

	if len(c.AllowedEmails) == 0 {
		return nil, fmt.Errorf("config: ALLOWED_EMAILS must list at least one email")
	}
	if c.GoogleClientID == "" || c.GoogleClientSecret == "" {
		return nil, fmt.Errorf("config: GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET are required")
	}
	return c, nil
}

// SeedEmail is the single user created on first boot.
func (c *Config) SeedEmail() string { return c.AllowedEmails[0] }

// RedirectURL is the OAuth callback Google will hit.
func (c *Config) RedirectURL() string { return c.PublicBaseURL + "/auth/google/callback" }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func parseEnv(s string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ';' }) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && k != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Int parses an int env var, returning def on any failure.
func Int(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
