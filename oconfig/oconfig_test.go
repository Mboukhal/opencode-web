package oconfig

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// writeSkill creates a minimal skill folder under src.
func writeSkill(t *testing.T, src, name string) {
	t.Helper()
	dir := filepath.Join(src, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+name), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureWritesModelAndDeploysSkills(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "skills-src")
	writeSkill(t, src, "analytics")
	writeSkill(t, src, "backtest-expert")
	// A folder without SKILL.md is not a skill and must be skipped.
	os.MkdirAll(filepath.Join(src, "not-a-skill"), 0o755)

	c := &Config{
		ConfigDir: filepath.Join(root, "config"),
		StateDir:  filepath.Join(root, "state"),
		Home:      root,
		SkillsSrc: src,
		Model:     "opencode/muse-spark-1.3",
		Log:       testLogger(),
	}
	if err := c.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	var doc map[string]any
	b, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatalf("reading opencode.json: %v", err)
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parsing opencode.json: %v", err)
	}
	if doc["model"] != "opencode/muse-spark-1.3" {
		t.Errorf("model = %v", doc["model"])
	}

	for _, name := range []string{"analytics", "backtest-expert"} {
		if _, err := os.Stat(filepath.Join(c.SkillsDir(), name, "SKILL.md")); err != nil {
			t.Errorf("skill %s not deployed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(c.SkillsDir(), "not-a-skill")); err == nil {
		t.Error("folder without SKILL.md was deployed")
	}

	skills, _ := doc["skills"].(map[string]any)
	paths, _ := skills["paths"].([]any)
	if len(paths) != 1 || paths[0] != c.SkillsDir() {
		t.Errorf("skills.paths = %v, want [%s]", paths, c.SkillsDir())
	}
}

func TestEnsurePreservesUnknownKeys(t *testing.T) {
	root := t.TempDir()
	c := &Config{
		ConfigDir: filepath.Join(root, "config"),
		StateDir:  filepath.Join(root, "state"),
		Home:      root,
		Model:     "new/model",
		Log:       testLogger(),
	}
	os.MkdirAll(filepath.Dir(c.Path()), 0o755)
	os.WriteFile(c.Path(), []byte(`{"permission":{"question":"deny"},"model":"old/model"}`), 0o644)

	if err := c.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	var doc map[string]any
	b, _ := os.ReadFile(c.Path())
	json.Unmarshal(b, &doc)

	if doc["model"] != "new/model" {
		t.Errorf("model = %v, want new/model", doc["model"])
	}
	if _, ok := doc["permission"]; !ok {
		t.Error("unknown key permission was dropped")
	}
	if doc["$schema"] != "https://opencode.ai/config.json" {
		t.Errorf("$schema = %v", doc["$schema"])
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "skills-src")
	writeSkill(t, src, "one")
	c := &Config{
		ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"),
		Home: root, SkillsSrc: src, Model: "m", Log: testLogger(),
	}
	for i := 0; i < 3; i++ {
		if err := c.Ensure(context.Background()); err != nil {
			t.Fatalf("Ensure #%d: %v", i, err)
		}
	}
	var doc map[string]any
	b, _ := os.ReadFile(c.Path())
	json.Unmarshal(b, &doc)
	skills := doc["skills"].(map[string]any)
	paths := skills["paths"].([]any)
	if len(paths) != 1 {
		t.Fatalf("skills.paths duplicated across runs: %v", paths)
	}
}

// TestDeploySkillsFollowsSymlinks covers the common layout where the source
// skills directory contains symlinks into another checkout.
func TestDeploySkillsFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "real"), "alpha")
	os.WriteFile(filepath.Join(root, "real", "alpha", "SKILL.md"), []byte("# alpha"), 0o644)
	src := filepath.Join(root, "skills-src")
	os.MkdirAll(src, 0o755)
	if err := os.Symlink(filepath.Join(root, "real", "alpha"), filepath.Join(src, "alpha")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	c := &Config{
		ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"),
		Home: root, SkillsSrc: src, Model: "m", Log: testLogger(),
	}
	if err := c.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(c.SkillsDir(), "alpha", "SKILL.md"))
	if err != nil {
		t.Fatalf("symlinked skill not deployed: %v", err)
	}
	if string(b) != "# alpha" {
		t.Errorf("SKILL.md = %q", b)
	}
}
