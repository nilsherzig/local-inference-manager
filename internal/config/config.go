// Package config loads and validates the YAML configuration. Each model carries
// its own full llama-server command line; the only substitution is ${PORT},
// which the manager assigns per instance at start time.
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the full parsed configuration.
type Config struct {
	Manager Manager          `yaml:"manager"`
	Models  map[string]Model `yaml:"models"`

	// aliasIndex maps every model name and alias to its canonical model name.
	aliasIndex map[string]string
}

// Manager holds process-wide settings.
type Manager struct {
	Listen          string `yaml:"listen"`
	DBPath          string `yaml:"db_path"`
	DefaultTTL      int    `yaml:"default_ttl"`    // idle seconds before an instance is stopped
	HealthTimeout   int    `yaml:"health_timeout"` // max seconds to wait for /health
	LogRequestsBody bool   `yaml:"log_requests_body"`
}

// Model is a single llama-server configuration. Cmd is the exact command line a
// power user would run, with ${PORT} substituted per instance.
type Model struct {
	Cmd     string   `yaml:"cmd"`
	TTL     int      `yaml:"ttl"`
	Aliases []string `yaml:"aliases"`
}

// Load reads, parses and validates the config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	c.applyDefaults()

	if err := c.buildAliasIndex(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Manager.Listen == "" {
		c.Manager.Listen = "127.0.0.1:8080"
	}
	if c.Manager.DBPath == "" {
		c.Manager.DBPath = "./lim.db"
	}
	if c.Manager.DefaultTTL == 0 {
		c.Manager.DefaultTTL = 300
	}
	if c.Manager.HealthTimeout == 0 {
		c.Manager.HealthTimeout = 120
	}
}

func (c *Config) buildAliasIndex() error {
	c.aliasIndex = make(map[string]string)
	for name, m := range c.Models {
		if prev, ok := c.aliasIndex[name]; ok {
			return fmt.Errorf("name collision: %q already maps to %q", name, prev)
		}
		c.aliasIndex[name] = name
		for _, a := range m.Aliases {
			if prev, ok := c.aliasIndex[a]; ok {
				return fmt.Errorf("alias collision: %q used by %q and %q", a, prev, name)
			}
			c.aliasIndex[a] = name
		}
	}
	return nil
}

func (c *Config) validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("no models configured")
	}
	for name, m := range c.Models {
		if strings.TrimSpace(m.Cmd) == "" {
			return fmt.Errorf("model %q: empty cmd", name)
		}
	}
	return nil
}

// Resolve maps a model name or alias to its canonical name. ok is false when the
// name is unknown.
func (c *Config) Resolve(nameOrAlias string) (canonical string, ok bool) {
	canonical, ok = c.aliasIndex[nameOrAlias]
	return
}

// TTL returns the idle timeout for a model, falling back to the manager default.
func (c *Config) TTL(canonical string) int {
	if m, ok := c.Models[canonical]; ok && m.TTL > 0 {
		return m.TTL
	}
	return c.Manager.DefaultTTL
}

// Args substitutes ${PORT} and splits the model command into an argv slice.
// Splitting on whitespace is enough for llama-server flags (no shell quoting).
func (c *Config) Args(canonical, port string) ([]string, error) {
	m, ok := c.Models[canonical]
	if !ok {
		return nil, fmt.Errorf("unknown model %q", canonical)
	}
	cmd := strings.ReplaceAll(m.Cmd, "${PORT}", port)
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return nil, fmt.Errorf("model %q: empty cmd", canonical)
	}
	return fields, nil
}

// HFRepo returns the HuggingFace repo[:quant] a model downloads, parsed from its
// cmd (-hf / -hfr / --hf-repo). ok is false when the model loads from a local
// path or url instead, so preload can skip cache detection for it.
func (c *Config) HFRepo(canonical string) (repo string, ok bool) {
	m, exists := c.Models[canonical]
	if !exists {
		return "", false
	}
	fields := strings.Fields(m.Cmd)
	for i, f := range fields {
		switch f {
		case "-hf", "-hfr", "--hf-repo":
			if i+1 < len(fields) {
				return fields[i+1], true
			}
		}
	}
	return "", false
}

// ModelNames returns the canonical model names, unsorted.
func (c *Config) ModelNames() []string {
	names := make([]string, 0, len(c.Models))
	for n := range c.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
