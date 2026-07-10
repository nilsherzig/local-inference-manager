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

func TestParseDownload(t *testing.T) {
	// Positive: repo:quant splits into both parts.
	if d := ParseDownload("unsloth/gemma-GGUF:Q4_K_M"); d.Repo != "unsloth/gemma-GGUF" || d.Quant != "Q4_K_M" {
		t.Errorf("ParseDownload(repo:quant) = %+v", d)
	}
	// Positive: unsloth UD- prefixed quant is preserved verbatim.
	if d := ParseDownload("unsloth/gemma-GGUF:UD-Q4_K_XL"); d.Quant != "UD-Q4_K_XL" {
		t.Errorf("ParseDownload UD quant = %q; want UD-Q4_K_XL", d.Quant)
	}
	// Negative: a bare repo yields an empty quant (whole-repo download).
	if d := ParseDownload("unsloth/gemma-GGUF"); d.Repo != "unsloth/gemma-GGUF" || d.Quant != "" {
		t.Errorf("ParseDownload(repo) = %+v; want empty quant", d)
	}
}

func TestDownloads(t *testing.T) {
	yaml := `
manager:
  models_dir: /data/models
models:
  main:
    cmd: "llama-server --port ${PORT} -m /data/models/unsloth/gemma-GGUF/gemma-Q4_K_M.gguf"
    downloads:
      - unsloth/gemma-GGUF:Q4_K_M
  draft:
    cmd: "llama-server --port ${PORT} -m a.gguf -md b.gguf"
    downloads:
      - unsloth/gemma-GGUF:Q4_K_M
      - unsloth/gemma-draft-GGUF:Q4_K_M
  local:
    cmd: "llama-server --port ${PORT} -m /models/gemma.gguf"
`
	c, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatal(err)
	}

	// Positive: a single download is parsed.
	if d := c.Downloads("main"); len(d) != 1 || d[0].Repo != "unsloth/gemma-GGUF" || d[0].Quant != "Q4_K_M" {
		t.Errorf("Downloads(main) = %+v", d)
	}
	// Positive: a model may declare several entries (main + draft), order kept.
	if d := c.Downloads("draft"); len(d) != 2 ||
		d[0].Repo != "unsloth/gemma-GGUF" || d[1].Repo != "unsloth/gemma-draft-GGUF" {
		t.Errorf("Downloads(draft) = %+v", d)
	}
	// Positive: LocalDir nests the repo under models_dir.
	if got := c.LocalDir("unsloth/gemma-GGUF"); got != "/data/models/unsloth/gemma-GGUF" {
		t.Errorf("LocalDir = %q", got)
	}
	// Negative: a model without a downloads list has none.
	if d := c.Downloads("local"); d != nil {
		t.Errorf("Downloads(local) = %+v; want nil", d)
	}
	// Negative: unknown model.
	if d := c.Downloads("nope"); d != nil {
		t.Errorf("Downloads(nope) = %+v; want nil", d)
	}
}

func TestDownloadRepoValidation(t *testing.T) {
	// Negative: a download entry that is not org/name is rejected at load.
	yaml := `
models:
  bad:
    cmd: "llama-server --port ${PORT} -m x.gguf"
    downloads:
      - notarepo:Q4_K_M
`
	if _, err := Load(writeTemp(t, yaml)); err == nil || !strings.Contains(err.Error(), "org/name") {
		t.Errorf("expected org/name validation error, got %v", err)
	}
}
