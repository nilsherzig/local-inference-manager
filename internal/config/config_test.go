package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

const validYAML = `
manager:
  listen: "127.0.0.1:9090"
models:
  gemma:
    cmd: |
      llama-server --port ${PORT} -ngl 99
      -hf unsloth/gemma
    ttl: 60
    aliases: [g, gem]
`

func TestLoadValid(t *testing.T) {
	c, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Manager.Listen != "127.0.0.1:9090" {
		t.Errorf("listen = %q", c.Manager.Listen)
	}
	if c.Manager.DefaultTTL != 300 {
		t.Errorf("default ttl = %d, want 300", c.Manager.DefaultTTL)
	}
	// Macro must be expanded, ${PORT} preserved.
	cmd := c.Models["gemma"].Cmd
	if !strings.Contains(cmd, "-ngl 99") || !strings.Contains(cmd, "${PORT}") {
		t.Errorf("cmd not expanded correctly: %q", cmd)
	}
	if !strings.Contains(cmd, "-hf unsloth/gemma") {
		t.Errorf("model-specific args missing: %q", cmd)
	}
}

func TestResolveAlias(t *testing.T) {
	c, _ := Load(writeTemp(t, validYAML))
	for _, name := range []string{"gemma", "g", "gem"} {
		if got, ok := c.Resolve(name); !ok || got != "gemma" {
			t.Errorf("Resolve(%q) = %q, %v", name, got, ok)
		}
	}
	if _, ok := c.Resolve("nope"); ok {
		t.Errorf("Resolve(nope) should fail")
	}
}

func TestArgsSubstitutesPort(t *testing.T) {
	c, _ := Load(writeTemp(t, validYAML))
	args, err := Args(c, "gemma")
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "llama-server" {
		t.Errorf("argv[0] = %q", args[0])
	}
	for _, a := range args {
		if strings.Contains(a, "${PORT}") {
			t.Errorf("PORT not substituted: %v", args)
		}
		if a == "12345" {
			return
		}
	}
	t.Errorf("port value not found in args: %v", args)
}

// Args wraps c.Args with a fixed port for the test.
func Args(c *Config, model string) ([]string, error) { return c.Args(model, "12345") }

func TestTTLFallback(t *testing.T) {
	c, _ := Load(writeTemp(t, validYAML))
	if got := c.TTL("gemma"); got != 60 {
		t.Errorf("TTL(gemma) = %d, want 60", got)
	}
}

func TestAliasCollision(t *testing.T) {
	yaml := `
models:
  a:
    cmd: "x"
    aliases: [shared]
  b:
    cmd: "y"
    aliases: [shared]
`
	if _, err := Load(writeTemp(t, yaml)); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Errorf("expected collision error, got %v", err)
	}
}

func TestNoModels(t *testing.T) {
	if _, err := Load(writeTemp(t, "manager: {}")); err == nil {
		t.Errorf("expected error for no models")
	}
}

func TestHFRepos(t *testing.T) {
	yaml := `
models:
  hf:
    cmd: "llama-server --port ${PORT} -hf unsloth/gemma:Q4_K_M"
  draft:
    cmd: "llama-server --port ${PORT} -hf unsloth/gemma:Q4_K_M -hfd williamliao/gemma-DFlash:Q4_K_M"
  local:
    cmd: "llama-server --port ${PORT} -m /models/gemma.gguf"
`
	c, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatal(err)
	}

	// Positive: -hf value is extracted.
	if repos := c.HFRepos("hf"); len(repos) != 1 || repos[0] != "unsloth/gemma:Q4_K_M" {
		t.Errorf("HFRepos(hf) = %v; want [unsloth/gemma:Q4_K_M]", repos)
	}
	// Positive: both -hf and -hfd (draft) repos are extracted.
	if repos := c.HFRepos("draft"); len(repos) != 2 ||
		repos[0] != "unsloth/gemma:Q4_K_M" || repos[1] != "williamliao/gemma-DFlash:Q4_K_M" {
		t.Errorf("HFRepos(draft) = %v; want [unsloth/gemma:Q4_K_M williamliao/gemma-DFlash:Q4_K_M]", repos)
	}
	// Negative: a local model has no repos.
	if repos := c.HFRepos("local"); repos != nil {
		t.Errorf("HFRepos(local) = %v; want nil", repos)
	}
	// Negative: unknown model.
	if repos := c.HFRepos("nope"); repos != nil {
		t.Errorf("HFRepos(nope) = %v; want nil", repos)
	}
}
