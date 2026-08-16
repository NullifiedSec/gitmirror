package config

import "testing"

func TestValidateAllowsSameFullNameAcrossProviders(t *testing.T) {
	cfg := Config{Pairs: []Pair{
		{
			Name:  "cross-provider",
			Left:  Repo{Provider: ProviderGitHub, FullName: "owner/repo", URL: "git@github.com:owner/repo.git"},
			Right: Repo{Provider: ProviderGitea, FullName: "owner/repo", URL: "git@gitea.example:owner/repo.git"},
		},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsUnsupportedProvider(t *testing.T) {
	cfg := Config{Pairs: []Pair{
		{
			Name:  "bad-provider",
			Left:  Repo{Provider: "gitlab", FullName: "owner/repo", URL: "git@example:owner/repo.git"},
			Right: Repo{Provider: ProviderGitHub, FullName: "other/repo", URL: "git@github.com:other/repo.git"},
		},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted unsupported provider")
	}
}
