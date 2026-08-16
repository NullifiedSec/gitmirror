package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NullifiedSec/gitmirror/internal/config"
)

func TestServeConfigPath(t *testing.T) {
	got, err := ServeConfigPath([]string{"--config", "/tmp/test.toml", "serve"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/test.toml" {
		t.Fatalf("config path = %q", got)
	}
}

func TestPairAddInteractiveWritesValidatedConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gitmirror.toml")
	initial := `listen = ":8080"
data_dir = "` + filepath.Join(root, "data") + `"

[[pairs]]
name = "existing"

[pairs.left]
provider = "github"
full_name = "acme/left"
url = "git@github.com:acme/left.git"

[pairs.right]
provider = "github"
full_name = "mirror/left"
url = "git@github.com:mirror/left.git"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		"new-pair",
		"", // left provider github
		"acme/new",
		"", // default GitHub SSH URL
		"y",
		"30",
		"main,release",
		"", // right provider github
		"mirror/new",
		"", // default URL
		"n",
		"",
		"y",
	}, "\n") + "\n"
	var out, errOut bytes.Buffer
	app := &App{In: strings.NewReader(input), Out: &out, ErrOut: &errOut}
	handled, code := app.Run([]string{"--config", path, "pair", "add"})
	if !handled || code != 0 {
		t.Fatalf("handled=%t code=%d stderr=%s stdout=%s", handled, code, errOut.String(), out.String())
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Pairs) != 2 {
		t.Fatalf("pairs=%d, want 2", len(cfg.Pairs))
	}
	p := cfg.Pairs[1]
	if p.Name != "new-pair" || p.Left.FullName != "acme/new" || p.Right.FullName != "mirror/new" {
		t.Fatalf("unexpected pair: %#v", p)
	}
	if p.Left.URL != "git@github.com:acme/new.git" || !p.Left.Polling || p.Left.PollingFrequency != 30 {
		t.Fatalf("unexpected left repo: %#v", p.Left)
	}
	if len(p.Left.HumanInLoopBranches) != 2 || p.Left.HumanInLoopBranches[0] != "main" || p.Left.HumanInLoopBranches[1] != "release" {
		t.Fatalf("unexpected HIL branches: %#v", p.Left.HumanInLoopBranches)
	}
}

func TestRunReturnsDaemonModeForLegacyConfigFlag(t *testing.T) {
	app := &App{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	handled, code := app.Run([]string{"-config", "/etc/gitmirror/gitmirror.toml"})
	if handled || code != 0 {
		t.Fatalf("legacy daemon invocation handled=%t code=%d", handled, code)
	}
}
