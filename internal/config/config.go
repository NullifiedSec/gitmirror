package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Listen  string `json:"listen"`
	DataDir string `json:"data_dir"`
	Pairs   []Pair `json:"pairs"`
}

type Pair struct {
	Name  string `json:"name"`
	Left  Repo   `json:"left"`
	Right Repo   `json:"right"`
}

type Repo struct {
	FullName string `json:"full_name"`
	URL      string `json:"url"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DataDir == "" {
		c.DataDir = ".gitmirror"
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
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
			if strings.Count(r.FullName, "/") != 1 || strings.TrimSpace(r.URL) == "" {
				return fmt.Errorf("pairs[%d].%s requires full_name owner/repo and url", i, side)
			}
			key := strings.ToLower(r.FullName)
			if _, ok := seenRepos[key]; ok {
				return fmt.Errorf("repository %q appears in more than one pair", r.FullName)
			}
			seenRepos[key] = struct{}{}
		}
	}
	return nil
}
