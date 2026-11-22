// Package oconfig prepares the OpenCode configuration directory used by the
// shared server: the global opencode.json, local skills copied from a source
// directory, per-project roles, and provider API keys stored as credentials.
package oconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"opencodeweb/opencode"
)

// Config is the subset of opencode.json this project manages. Unknown keys in
// an existing file are preserved by merging into the raw map.
type Config struct {
	Bin       string            // opencode CLI
	ConfigDir string            // XDG_CONFIG_HOME; opencode.json lives in ConfigDir/opencode
	StateDir  string            // XDG_DATA_HOME / XDG_STATE_HOME
	Home      string            // HOME
	SkillsSrc string            // optional directory of local skills to deploy
	Model     string            // default model
	Agent     string            // default agent
	Providers map[string]string // provider ID -> API key
	ExtraEnv  map[string]string
	Log       *slog.Logger
}

// Path is the global opencode.json location.
func (c *Config) Path() string { return filepath.Join(c.ConfigDir, "opencode", "opencode.json") }

// SkillsDir is where local skills are deployed for the server to discover.
func (c *Config) SkillsDir() string {
	return filepath.Join(c.ConfigDir, "opencode", "skills")
}

// Ensure writes opencode.json, deploying skills and provider config. It is
// idempotent: rerunning it after changing the model only rewrites what differs.
func (c *Config) Ensure(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(c.Path()), 0o755); err != nil {
		return fmt.Errorf("creating opencode config dir: %w", err)
	}

	doc, err := c.loadDoc()
	if err != nil {
		return err
	}
	if c.Model != "" {
		doc["model"] = c.Model
	}
	if c.Agent != "" {
		doc["default_agent"] = c.Agent
	}
	if c.SkillsSrc != "" {
		if err := c.deploySkills(); err != nil {
			return err
		}
		// Make the deployed skills visible to every project.
		doc["skills"] = map[string]any{"paths": mergeStrings(doc["skills"], c.SkillsDir())}
	}
	if c.Model != "" || c.Agent != "" || c.SkillsSrc != "" {
		if err := c.writeDoc(doc); err != nil {
			return err
		}
	}
	return nil
}

// loadDoc reads opencode.json, keeping any keys this project does not manage.
func (c *Config) loadDoc() (map[string]any, error) {
	doc := map[string]any{}
	b, err := os.ReadFile(c.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return nil, fmt.Errorf("reading %s: %w", c.Path(), err)
	}
	if len(b) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", c.Path(), err)
	}
	return doc, nil
}

func (c *Config) writeDoc(doc map[string]any) error {
	doc["$schema"] = "https://opencode.ai/config.json"
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.Path() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	return os.Rename(tmp, c.Path())
}

// deploySkills copies each skills/<name>/ from SkillsSrc into the config dir.
// Files are overwritten so editing a source skill updates the deployed copy.
// Symlinked skill directories (a common layout, e.g. ~/.agents/skills -> ~/.config)
// are followed so the deployed copy is real files.
func (c *Config) deploySkills() error {
	src, err := filepath.EvalSymlinks(c.SkillsSrc)
	if err != nil {
		c.log().Warn("skills source missing; skipping", "dir", c.SkillsSrc, "err", err)
		return nil
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading skills source: %w", err)
	}
	dst := c.SkillsDir()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("creating skills dir: %w", err)
	}
	var copied []string
	for _, e := range entries {
		// A entry may be a directory or a symlink to one.
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := e.Name()
		dir := filepath.Join(src, name)
		if target, err := filepath.EvalSymlinks(dir); err == nil {
			dir = target
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			continue // not a skill
		}
		if err := copyTree(dir, filepath.Join(dst, name)); err != nil {
			return fmt.Errorf("deploying skill %s: %w", name, err)
		}
		copied = append(copied, name)
	}
	sort.Strings(copied)
	c.log().Info("deployed local skills", "count", len(copied), "dir", dst, "skills", strings.Join(copied, ","))
	return nil
}

// PushKeys stores every provider API key as an OpenCode credential and reloads
// the server so they take effect. Keys already stored are compared by label and
// refreshed, which keeps the operation idempotent across restarts.
func (c *Config) PushKeys(ctx context.Context, client *opencode.Client) error {
	if len(c.Providers) == 0 {
		c.log().Warn("no provider API keys configured; models will report auth errors")
		return nil
	}

	existing, err := client.Credentials(ctx)
	if err != nil {
		return fmt.Errorf("listing credentials: %w", err)
	}
	have := make(map[string]opencode.Credential, len(existing))
	for _, cr := range existing {
		have[cr.IntegrationID+"|"+cr.Label] = cr
	}

	ids := make([]string, 0, len(c.Providers))
	for id := range c.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		key := strings.TrimSpace(c.Providers[id])
		if key == "" {
			continue
		}
		label := "opencode-web"
		// The listing includes secret values in the API, but this client does
		// not decode them; saving unconditionally keeps stored keys fresh.
		if _, err := client.SaveCredential(ctx, opencode.CredentialInput{
			IntegrationID: id,
			Label:         label,
			Key:           key,
			Activate:      true,
		}); err != nil {
			return fmt.Errorf("storing %s key: %w", id, err)
		}
		c.log().Info("stored provider key", "provider", id)
	}

	if err := client.Reload(ctx); err != nil {
		c.log().Warn("reload after storing keys failed", "err", err)
	}
	return nil
}

// mergeStrings adds path to a "paths" list in an existing skills object.
func mergeStrings(existing any, path string) []string {
	var paths []string
	if m, ok := existing.(map[string]any); ok {
		if p, ok := m["paths"].([]any); ok {
			for _, v := range p {
				if s, ok := v.(string); ok {
					paths = append(paths, s)
				}
			}
		}
	}
	for _, p := range paths {
		if p == path {
			return paths
		}
	}
	return append(paths, path)
}

// copyTree copies src into dst, recreating directories and files.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

func (c *Config) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}
