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
	// Used to build the Google OAuth redirect URI and the mobile pairing link.
	PublicBaseURL string

	// MobilePairURL is the origin phones use to reach the OpenCode server for
	// pairing, e.g. https://opencode.mboukhal.dev. It must route to the OpenCode
	// server, NOT to this web app: the phone redeems /auth/connect/<code> there.
	// Empty falls back to PublicBaseURL, which only works when the OpenCode
	// server is published on the same origin (single-host deployments).
	MobilePairURL string

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

	// OpenCode server (shared service) settings.
	OpenCodeServiceAddr     string // attach to an existing server instead of starting one
	OpenCodeServicePassword string
	OpenCodeServiceHost     string // hostname the server binds to
	OpenCodeServicePort     int    // 0 lets OpenCode choose

	// SkillsSrc is a directory of local skills deployed into the config dir.
	SkillsSrc string

	// OpenCodeEnv is passed to the OpenCode server and every child process
	// (provider keys, etc).
	OpenCodeEnv map[string]string

	// ProviderKeys maps an OpenCode integration ID (e.g. "openrouter") to an
	// API key. They are stored as OpenCode credentials on boot.
	ProviderKeys map[string]string

	// Git author identity used for commits made from the phone.
	GitAuthorName  string
	GitAuthorEmail string

	// Security headers applied to every response.
	//
	// ForceHTTPS redirects http->https when the server is reached directly
	// (behind a TLS-terminating proxy, which sets X-Forwarded-Proto, the
	// redirect is skipped so it cannot loop).
	ForceHTTPS bool
	// HSTSMaxAge is the Strict-Transport-Security max-age in seconds (0 disables).
	HSTSMaxAge int
	// ExtraCSPOrigins is appended to the Content-Security-Policy so a deployment
	// can allow, for example, a font or image host.
	ExtraCSPOrigins string
}

// Load reads configuration from the process environment.
func Load() (*Config, error) {
	dataDir := env("DATA_DIR", "/data")
	c := &Config{
		Addr:                    env("ADDR", ":8080"),
		PublicBaseURL:           strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		MobilePairURL:           strings.TrimRight(os.Getenv("MOBILE_PAIR_URL"), "/"),
		GoogleClientID:          os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:      os.Getenv("GOOGLE_CLIENT_SECRET"),
		AllowedEmails:           splitList(env("ALLOWED_EMAILS", "lios80466@gmail.com")),
		DataDir:                 dataDir,
		ProjectsDir:             env("PROJECTS_DIR", dataDir+"/projects"),
		ConfigDir:               env("OPENCODE_CONFIG_DIR", dataDir+"/config"),
		StateDir:                env("OPENCODE_STATE_DIR", dataDir+"/state"),
		DBPath:                  env("DB_PATH", dataDir+"/app/app.db"),
		SSHKeyPath:              env("SSH_KEY_PATH", dataDir+"/ssh/id_ed25519"),
		OpenCodeBin:             env("OPENCODE_BIN", "/usr/local/bin/opencode"),
		OpenCodeModel:           env("OPENCODE_MODEL", ""),
		OpenCodeAgent:           env("OPENCODE_AGENT", ""),
		OpenCodeTimeout:         envDuration("OPENCODE_TIMEOUT", 30*time.Minute),
		GitHubAllowedOrg:        splitList(os.Getenv("GITHUB_ORGS")),
		OpenCodeServiceAddr:     os.Getenv("OPENCODE_SERVICE_ADDR"),
		OpenCodeServicePassword: os.Getenv("OPENCODE_SERVICE_PASSWORD"),
		OpenCodeServiceHost:     env("OPENCODE_SERVICE_HOST", "0.0.0.0"),
		OpenCodeServicePort:     Int("OPENCODE_SERVICE_PORT", 0),
		SkillsSrc:               env("SKILLS_SRC", dataDir+"/skills"),
		OpenCodeEnv:             parseEnv(os.Getenv("OPENCODE_ENV")),
		ProviderKeys:            parseEnv(os.Getenv("PROVIDER_KEYS")),
		GitAuthorName:           env("GIT_AUTHOR_NAME", "Mohammed Boukhal"),
		GitAuthorEmail:          env("GIT_AUTHOR_EMAIL", "lios80466@gmail.com"),
		ForceHTTPS:              os.Getenv("FORCE_HTTPS") == "1" || os.Getenv("FORCE_HTTPS") == "true",
		HSTSMaxAge:              Int("HSTS_MAX_AGE", 0),
		ExtraCSPOrigins:         env("EXTRA_CSP_ORIGINS", ""),
	}
	// A deployment served only over TLS should advertise HSTS by default.
	if c.HSTSMaxAge == 0 && strings.HasPrefix(c.PublicBaseURL, "https://") {
		c.HSTSMaxAge = 180 * 24 * 60 * 60 // 180 days
	}
	if c.HSTSMaxAge < 0 {
		c.HSTSMaxAge = 0
	}

	if c.OpenCodeModel == "" {
		c.OpenCodeModel = DefaultModel
	}

	if len(c.AllowedEmails) == 0 {
		return nil, fmt.Errorf("config: ALLOWED_EMAILS must list at least one email")
	}
	if c.GoogleClientID == "" || c.GoogleClientSecret == "" {
		return nil, fmt.Errorf("config: GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET are required")
	}
	return c, nil
}

// DefaultModel is used when OPENCODE_MODEL is unset.
const DefaultModel = "opencode/muse-spark-1.3"

// SeedEmail is the single user created on first boot.
func (c *Config) SeedEmail() string { return c.AllowedEmails[0] }

// RedirectURL is the OAuth callback Google will hit.
func (c *Config) RedirectURL() string { return c.PublicBaseURL + "/auth/google/callback" }

// PairOrigin is the origin phones use to reach the OpenCode server.
func (c *Config) PairOrigin() string {
	if c.MobilePairURL != "" {
		return c.MobilePairURL
	}
	return c.PublicBaseURL
}

// PairOriginIsApp reports whether the pairing origin points at this web app
// rather than at the OpenCode server. Pairing cannot succeed in that case
// unless something proxies /auth/connect and /api into the server.
func (c *Config) PairOriginIsApp() bool {
	return c.MobilePairURL == "" || c.MobilePairURL == c.PublicBaseURL
}

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
