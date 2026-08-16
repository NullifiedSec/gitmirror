package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPollingConfigDefaultsAndWebhookRequirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitmirror.toml")
	content := `[[pairs]]
name = "polling"

[pairs.left]
provider = "github"
full_name = "owner/source"
url = "git@github.com:owner/source.git"
polling = true

[pairs.right]
provider = "github"
full_name = "owner/mirror"
url = "git@github.com:owner/mirror.git"
polling = true
polling_frequency = 30
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pairs[0].Left.PollingFrequency != DefaultPollingFrequency {
		t.Fatalf("left polling frequency = %d, want %d", cfg.Pairs[0].Left.PollingFrequency, DefaultPollingFrequency)
	}
	if cfg.Pairs[0].Right.PollingFrequency != 30 {
		t.Fatalf("right polling frequency = %d, want 30", cfg.Pairs[0].Right.PollingFrequency)
	}
	if cfg.RequiresWebhook(ProviderGitHub) {
		t.Fatal("polling-only GitHub pair unexpectedly requires a webhook secret")
	}
}

func TestWebhookRequiredWhenAnySideIsNotPolling(t *testing.T) {
	cfg := Config{Pairs: []Pair{{
		Name:  "mixed",
		Left:  Repo{Provider: ProviderGitHub, FullName: "owner/source", URL: "git@github.com:owner/source.git", Polling: true, PollingFrequency: 120},
		Right: Repo{Provider: ProviderGitHub, FullName: "owner/mirror", URL: "git@github.com:owner/mirror.git"},
	}}}
	if !cfg.RequiresWebhook(ProviderGitHub) {
		t.Fatal("mixed polling/webhook pair should require a GitHub webhook secret")
	}
}

func TestPollingFrequencyRejectsTooSmallValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitmirror.toml")
	content := `[[pairs]]
name = "too-fast"

[pairs.left]
full_name = "owner/source"
url = "git@github.com:owner/source.git"
polling = true
polling_frequency = 5

[pairs.right]
full_name = "owner/mirror"
url = "git@github.com:owner/mirror.git"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted polling_frequency below the minimum")
	}
}

func TestHumanInLoopBranchesLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitmirror.toml")
	content := `[[pairs]]
name = "hil"

[pairs.left]
full_name = "owner/source"
url = "git@github.com:owner/source.git"
human_in_loop_branches = ["main", "release"]

[pairs.right]
full_name = "owner/mirror"
url = "git@github.com:owner/mirror.git"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !RequiresHumanApproval(cfg.Pairs[0].Left, "main") || !RequiresHumanApproval(cfg.Pairs[0].Left, "release") {
		t.Fatalf("HIL branches not loaded: %#v", cfg.Pairs[0].Left.HumanInLoopBranches)
	}
	if RequiresHumanApproval(cfg.Pairs[0].Left, "dev") {
		t.Fatal("unexpected HIL match for dev")
	}
}
