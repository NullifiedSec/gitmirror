package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitmirror.toml")
	content := `listen = ":9090"
data_dir = "/tmp/gitmirror"

[[pairs]]
name = "cross-provider"

[pairs.left]
provider = "github"
full_name = "owner/repo"
url = "git@github.com:owner/repo.git"

[pairs.right]
provider = "gitea"
full_name = "owner/repo"
url = "git@gitea.example:owner/repo.git"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9090" || cfg.DataDir != "/tmp/gitmirror" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if len(cfg.Pairs) != 1 || cfg.Pairs[0].Right.Provider != ProviderGitea {
		t.Fatalf("unexpected pairs: %#v", cfg.Pairs)
	}
}

func TestLoadJSONCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitmirror.json")
	content := `{
  "pairs": [
    {
      "name": "legacy",
      "left": {"full_name": "left/repo", "url": "git@github.com:left/repo.git"},
      "right": {"full_name": "right/repo", "url": "git@github.com:right/repo.git"}
    }
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8080" || cfg.DataDir != ".gitmirror" {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
	if cfg.Pairs[0].Left.Provider != ProviderGitHub || cfg.Pairs[0].Right.Provider != ProviderGitHub {
		t.Fatalf("legacy providers not defaulted: %#v", cfg.Pairs[0])
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		content string
	}{
		{
			name: "toml",
			file: "gitmirror.toml",
			content: `data_dri = ".gitmirror"
[[pairs]]
name = "example"
[pairs.left]
full_name = "left/repo"
url = "git@github.com:left/repo.git"
[pairs.right]
full_name = "right/repo"
url = "git@github.com:right/repo.git"
`,
		},
		{
			name: "json",
			file: "gitmirror.json",
			content: `{"data_dri":".gitmirror","pairs":[{"name":"example","left":{"full_name":"left/repo","url":"git@github.com:left/repo.git"},"right":{"full_name":"right/repo","url":"git@github.com:right/repo.git"}}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.file)
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load() accepted an unknown config field")
			}
		})
	}
}

func TestLoadRejectsTrailingJSONValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitmirror.json")
	content := `{"pairs":[{"name":"example","left":{"full_name":"left/repo","url":"git@github.com:left/repo.git"},"right":{"full_name":"right/repo","url":"git@github.com:right/repo.git"}}]} {}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted a trailing JSON value")
	}
}

func TestLoadRejectsUnknownExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitmirror.yaml")
	if err := os.WriteFile(path, []byte("pairs: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted unsupported config extension")
	}
}
