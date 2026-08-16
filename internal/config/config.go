package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	ProviderGitHub = "github"
	ProviderGitea  = "gitea"
)

type Config struct {
	Listen  string `json:"listen" toml:"listen"`
	DataDir string `json:"data_dir" toml:"data_dir"`
	Pairs   []Pair `json:"pairs" toml:"pairs"`
}

type Pair struct {
	Name  string `json:"name" toml:"name"`
	Left  Repo   `json:"left" toml:"left"`
	Right Repo   `json:"right" toml:"right"`
}

type Repo struct {
	Provider string `json:"provider,omitempty" toml:"provider,omitempty"`
	FullName string `json:"full_name" toml:"full_name"`
	URL      string `json:"url" toml:"url"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		decoder := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			return Config{}, fmt.Errorf("decode TOML config: %w", err)
		}
	case ".json", "":
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			return Config{}, fmt.Errorf("decode JSON config: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return Config{}, fmt.Errorf("decode JSON config: trailing JSON value")
			}
			return Config{}, fmt.Errorf("decode JSON config: %w", err)
		}
	default:
		return Config{}, fmt.Errorf("unsupported config format %q; use .toml or .json", filepath.Ext(path))
	}
	applyDefaults(&c)
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func applyDefaults(c *Config) {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DataDir == "" {
		c.DataDir = ".gitmirror"
	}
	for i := range c.Pairs {
		if c.Pairs[i].Left.Provider == "" {
			c.Pairs[i].Left.Provider = ProviderGitHub
		}
		if c.Pairs[i].Right.Provider == "" {
			c.Pairs[i].Right.Provider = ProviderGitHub
		}
	}
}

func (c Config) Validate() error {
	if len(c.Pairs) == 0 {
		return fmt.Errorf("at least one repository pair is required")
	}
	seenNames := map[string]struct{}{}
	seenRepos := map[string]struct{}{}
	for i, p := range c.Pairs {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("pairs[%d].name is required", i)
		}
		if _, ok := seenNames[p.Name]; ok {
			return fmt.Errorf("duplicate pair name %q", p.Name)
		}
		seenNames[p.Name] = struct{}{}
		for side, r := range map[string]Repo{"left": p.Left, "right": p.Right} {
			provider := strings.ToLower(strings.TrimSpace(r.Provider))
			if provider == "" {
				provider = ProviderGitHub
			}
			if provider != ProviderGitHub && provider != ProviderGitea {
				return fmt.Errorf("pairs[%d].%s has unsupported provider %q", i, side, r.Provider)
			}
			if strings.Count(r.FullName, "/") != 1 || strings.TrimSpace(r.URL) == "" {
				return fmt.Errorf("pairs[%d].%s requires full_name owner/repo and url", i, side)
			}
			key := provider + ":" + strings.ToLower(r.FullName)
			if _, ok := seenRepos[key]; ok {
				return fmt.Errorf("repository %q on %s appears in more than one pair", r.FullName, provider)
			}
			seenRepos[key] = struct{}{}
		}
	}
	return nil
}

func (c Config) UsesProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, p := range c.Pairs {
		if strings.EqualFold(p.Left.Provider, provider) || strings.EqualFold(p.Right.Provider, provider) {
			return true
		}
	}
	return false
}
